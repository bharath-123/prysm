package sync

import (
	"context"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/verification"
	"github.com/OffchainLabs/prysm/v7/consensus-types/blocks"
	"github.com/OffchainLabs/prysm/v7/consensus-types/interfaces"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/time/slots"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"google.golang.org/protobuf/proto"
)

// validateExecutionPayloadBidGossip validates execution payload bids on gossip.
// The following validations MUST pass before forwarding the signed_execution_payload_bid
// on the network, assuming the alias bid = signed_execution_payload_bid.message:
func (s *Service) validateExecutionPayloadBidGossip(ctx context.Context, pid peer.ID, msg *pubsub.Message) (pubsub.ValidationResult, error) {
	if pid == s.cfg.p2p.PeerID() {
		return pubsub.ValidationAccept, nil
	}
	if s.cfg.initialSync.Syncing() {
		return pubsub.ValidationIgnore, nil
	}

	ctx, span := trace.StartSpan(ctx, "sync.validateExecutionPayloadBidGossip")
	defer span.End()

	if msg.Topic == nil {
		return pubsub.ValidationReject, p2p.ErrInvalidTopic
	}

	m, err := s.decodePubsubMessage(msg)
	if err != nil {
		return pubsub.ValidationReject, err
	}

	signedBid, ok := m.(*ethpb.SignedExecutionPayloadBid)
	if !ok {
		return pubsub.ValidationReject, errWrongMessage
	}
	b, err := blocks.WrappedROSignedExecutionPayloadBid(signedBid)
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	v := s.newExecutionPayloadBidVerifier(b, verification.GossipExecutionPayloadBidRequirements)
	plog := log.WithField("peer", pid)
	plog.Debug("BHARATH: created execution payload bid verifier")
	bid, err := b.Bid()
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	plog.WithField("slot", bid.Slot()).WithField("builder_index", bid.BuilderIndex()).Debug("BHARATH: unwrapped bid from signed payload")

	// [IGNORE] bid.slot is the current slot or the next slot.
	if err := v.VerifyCurrentOrNextSlot(); err != nil {
		plog.WithField("slot", bid.Slot()).WithError(err).Debug("BHARATH: VerifyCurrentOrNextSlot failed")
		return pubsub.ValidationIgnore, err
	}
	plog.WithField("slot", bid.Slot()).Debug("BHARATH: VerifyCurrentOrNextSlot passed")
	st, err := s.cfg.chain.HeadStateReadOnly(ctx)
	if err != nil {
		plog.WithError(err).Debug("BHARATH: HeadStateReadOnly failed")
		return pubsub.ValidationIgnore, err
	}
	plog.Debug("BHARATH: fetched head state")
	// [IGNORE] matching SignedProposerPreferences seen, keyed on the proposer
	// dep root anchored to bid.parent_block_root.
	parentBlockRoot := bid.ParentBlockRoot()
	priorEpoch, _ := slots.ToEpoch(bid.Slot()).SafeSub(1)
	dependentRoot, err := s.cfg.chain.DependentRootForEpoch(parentBlockRoot, priorEpoch)
	if err != nil {
		plog.WithError(err).Debug("BHARATH: DependentRootForEpoch failed")
		return pubsub.ValidationIgnore, err
	}
	plog.WithField("dependent_root", dependentRoot).Debug("BHARATH: computed dependent root")
	pref, ok := s.proposerPreferencesCache.Get(dependentRoot, bid.Slot())
	if !ok {
		plog.WithField("slot", bid.Slot()).WithField("dependent_root", dependentRoot).Debug("BHARATH: no proposer preference found in cache, ignoring")
		return pubsub.ValidationIgnore, nil
	}
	plog.WithField("slot", bid.Slot()).Debug("BHARATH: found proposer preference in cache")
	// [REJECT] bid.builder_index is a valid/active builder index.
	if err := v.VerifyBuilderActive(st); err != nil {
		plog.WithField("builder_index", bid.BuilderIndex()).WithError(err).Debug("BHARATH: VerifyBuilderActive failed")
		return pubsub.ValidationReject, err
	}
	plog.WithField("builder_index", bid.BuilderIndex()).Debug("BHARATH: VerifyBuilderActive passed")
	// [REJECT] bid.execution_payment is zero.
	if err := v.VerifyExecutionPaymentZero(); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifyExecutionPaymentZero failed")
		return pubsub.ValidationReject, err
	}
	plog.Debug("BHARATH: VerifyExecutionPaymentZero passed")
	// [REJECT] bid.fee_recipient matches the fee_recipient from the proposer's SignedProposerPreferences associated with bid.slot.
	if err := v.VerifyFeeRecipientMatches(pref.FeeRecipient[:]); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifyFeeRecipientMatches failed")
		return pubsub.ValidationReject, err
	}
	plog.Debug("BHARATH: VerifyFeeRecipientMatches passed")
	// The spec lists signature validation later, but the "first signed bid seen
	// with a valid signature" gate below depends on knowing validity first.
	if err := v.VerifySignature(st); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifySignature failed")
		return pubsub.ValidationReject, err
	}
	plog.Debug("BHARATH: VerifySignature passed")

	// [IGNORE] this is the first signed bid seen with a valid signature from the given builder for this slot.
	builderKey := executionPayloadBidBuilderKey(bid.Slot(), bid.BuilderIndex())
	if s.hasSeenExecutionPayloadBidBuilder(builderKey) {
		plog.WithField("slot", bid.Slot()).WithField("builder_index", bid.BuilderIndex()).Debug("BHARATH: already seen bid from this builder for this slot, ignoring")
		return pubsub.ValidationIgnore, nil
	}
	plog.WithField("slot", bid.Slot()).WithField("builder_index", bid.BuilderIndex()).Debug("BHARATH: first time seeing bid from this builder for this slot")
	s.setSeenExecutionPayloadBidBuilder(bid.Slot(), builderKey)
	// [IGNORE] this bid is the highest value bid seen for the tuple (bid.slot, bid.parent_block_hash, bid.parent_block_root).
	if !s.isHighestExecutionPayloadBid(bid) {
		plog.WithField("slot", bid.Slot()).Debug("BHARATH: not the highest value bid, ignoring")
		return pubsub.ValidationIgnore, nil
	}
	plog.WithField("slot", bid.Slot()).Debug("BHARATH: bid is the highest value seen for this slot/parent tuple")
	// [IGNORE] bid.value is less or equal than the builder's excess balance.
	if err := v.VerifyBuilderCanCoverBid(st); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifyBuilderCanCoverBid failed")
		return pubsub.ValidationIgnore, err
	}
	plog.Debug("BHARATH: VerifyBuilderCanCoverBid passed")
	// [IGNORE] bid.parent_block_hash is the block hash of a known execution payload in fork choice
	// and bid.gas_limit is compatible with parent_gas_limit and the proposer's target.
	if err := v.VerifyParentBlockHash(s.cfg.chain.BlockHash); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifyParentBlockHash failed")
		return pubsub.ValidationIgnore, err
	}
	plog.Debug("BHARATH: VerifyParentBlockHash passed")
	parentGasLimit, err := s.cfg.chain.GasLimit(parentBlockRoot)
	if err != nil {
		plog.WithError(err).Debug("BHARATH: GasLimit lookup failed")
		return pubsub.ValidationIgnore, err
	}
	plog.WithField("parent_gas_limit", parentGasLimit).Debug("BHARATH: fetched parent gas limit")
	if err := v.VerifyGasLimitTargetCompatible(parentGasLimit, pref.TargetGasLimit); err != nil {
		plog.WithField("parent_gas_limit", parentGasLimit).WithField("target_gas_limit", pref.TargetGasLimit).WithError(err).Debug("BHARATH: VerifyGasLimitTargetCompatible failed")
		return pubsub.ValidationIgnore, err
	}
	plog.WithField("parent_gas_limit", parentGasLimit).WithField("target_gas_limit", pref.TargetGasLimit).Debug("BHARATH: VerifyGasLimitTargetCompatible passed")
	// [IGNORE] bid.parent_block_root is the hash tree root of a known beacon block in fork choice.
	if err := v.VerifyParentBlockRootSeen(s.cfg.chain.InForkchoice); err != nil {
		plog.WithError(err).Debug("BHARATH: VerifyParentBlockRootSeen failed")
		return pubsub.ValidationIgnore, err
	}
	plog.Debug("BHARATH: VerifyParentBlockRootSeen passed")
	// [REJECT] signed_execution_payload_bid.signature is valid with respect to the bid.builder_index.
	// Verified earlier to satisfy the "first valid signed bid seen" condition.
	msg.ValidatorData = signedBid
	plog.WithField("slot", bid.Slot()).WithField("builder_index", bid.BuilderIndex()).Debug("BHARATH: all validations passed, accepting bid")
	return pubsub.ValidationAccept, nil
}

func (s *Service) executionPayloadBidSubscriber(_ context.Context, msg proto.Message) error {
	signedBid, ok := msg.(*ethpb.SignedExecutionPayloadBid)
	if !ok {
		return errWrongMessage
	}
	if signedBid.Message == nil {
		return errNilMessage
	}
	s.setHighestExecutionPayloadBid(signedBid)
	return nil
}

func executionPayloadBidBuilderKey(slot primitives.Slot, builderIndex primitives.BuilderIndex) string {
	b := append(bytesutil.Bytes32(uint64(slot)), bytesutil.Bytes32(uint64(builderIndex))...)
	return string(b)
}

func (s *Service) hasSeenExecutionPayloadBidBuilder(key string) bool {
	_, seen := s.seenExecutionPayloadBidCache.Get(key)
	return seen
}

func (s *Service) setSeenExecutionPayloadBidBuilder(slot primitives.Slot, key string) {
	s.seenExecutionPayloadBidCache.Add(slot, key, true)
}

func (s *Service) isHighestExecutionPayloadBid(bid interfaces.ROExecutionPayloadBid) bool {
	cached, ok := s.highestExecutionPayloadBidCache.Get(bid.Slot(), bid.ParentBlockHash(), bid.ParentBlockRoot())
	if !ok {
		return true
	}
	return bid.Value() > cached.Message.Value
}

func (s *Service) setHighestExecutionPayloadBid(signedBid *ethpb.SignedExecutionPayloadBid) {
	s.highestExecutionPayloadBidCache.SetIfHigher(signedBid)
}
