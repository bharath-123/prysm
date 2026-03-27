package sync

import (
	"context"
	"fmt"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/transition"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/verification"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	"github.com/OffchainLabs/prysm/v7/time/slots"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

func (s *Service) validateSignedProposerPreferencesGossip(ctx context.Context, pid peer.ID, msg *pubsub.Message) (pubsub.ValidationResult, error) {
	if pid == s.cfg.p2p.PeerID() {
		return pubsub.ValidationAccept, nil
	}
	if s.cfg.initialSync.Syncing() {
		return pubsub.ValidationIgnore, nil
	}

	ctx, span := trace.StartSpan(ctx, "sync.validateSignedProposerPreferencesGossip")
	defer span.End()

	if msg.Topic == nil {
		return pubsub.ValidationReject, p2p.ErrInvalidTopic
	}

	m, err := s.decodePubsubMessage(msg)
	if err != nil {
		return pubsub.ValidationReject, err
	}

	signedPreferences, ok := m.(*ethpb.SignedProposerPreferences)
	if !ok {
		return pubsub.ValidationReject, errWrongMessage
	}
	if signedPreferences.Message == nil {
		return pubsub.ValidationReject, errNilMessage
	}

	v := s.newSignedProposerPreferencesVerifier(signedPreferences, verification.SignedProposerPreferencesGossipRequirements)
	// [IGNORE] preferences.proposal_slot is in the next epoch.
	// Uses head state for the epoch check only.
	headStateRO, err := s.cfg.chain.HeadStateReadOnly(ctx)
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	if err := v.VerifyNextEpoch(headStateRO); err != nil {
		return pubsub.ValidationIgnore, err
	}

	// Advance the state to the first slot of the proposal epoch so that
	// the proposer lookahead is up to date. The head state may be in the
	// previous epoch and its lookahead has not yet been rotated.
	proposalEpochStart, err := slots.EpochStart(slots.ToEpoch(signedPreferences.Message.ProposalSlot))
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	headRoot, err := s.cfg.chain.HeadRoot(ctx)
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	headState, err := s.cfg.chain.HeadState(ctx)
	if err != nil {
		return pubsub.ValidationIgnore, err
	}
	st, err := transition.ProcessSlotsUsingNextSlotCache(ctx, headState, headRoot, proposalEpochStart)
	if err != nil {
		return pubsub.ValidationIgnore, err
	}

	// [REJECT] preferences.validator_index is present at the correct slot in the
	// next epoch's portion of state.proposer_lookahead.
	if err := v.VerifyValidProposalSlot(st); err != nil {
		return pubsub.ValidationReject, err
	}

	slot := signedPreferences.Message.ProposalSlot
	// [IGNORE] This is the first valid signed proposer preferences message
	// received for the given proposal slot.
	if s.proposerPreferencesCache.Has(slot) {
		return pubsub.ValidationIgnore, nil
	}
	// [REJECT] signed_proposer_preferences.signature is valid with respect to the
	// validator's public key.
	if err := v.VerifySignature(st); err != nil {
		return pubsub.ValidationReject, err
	}

	s.proposerPreferencesCache.Add(slot, signedPreferences.Message.FeeRecipient, signedPreferences.Message.GasLimit)
	msg.ValidatorData = signedPreferences
	log.WithFields(logrus.Fields{
		"proposalSlot":   slot,
		"proposalEpoch":  slots.ToEpoch(slot),
		"validatorIndex": signedPreferences.Message.ValidatorIndex,
		"feeRecipient":   fmt.Sprintf("%#x", signedPreferences.Message.FeeRecipient),
		"gasLimit":       signedPreferences.Message.GasLimit,
	}).Debug("Validated and accepted signed proposer preferences")
	return pubsub.ValidationAccept, nil
}

func (s *Service) signedProposerPreferencesSubscriber(_ context.Context, msg proto.Message) error {
	_, ok := msg.(*ethpb.SignedProposerPreferences)
	if !ok {
		return errWrongMessage
	}
	return nil
}
