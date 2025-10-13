package sync

import (
	"context"
	"encoding/binary"
	"fmt"
	"reflect"
	"strings"
	"time"

	// "github.com/OffchainLabs/prysm/v6/beacon-chain/blockchain"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/blockchain"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/core/blocks"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/core/feed"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/core/feed/operation"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/slasher/types"
	"github.com/OffchainLabs/prysm/v6/encoding/bytesutil"

	// "github.com/OffchainLabs/prysm/v6/beacon-chain/core/feed"
	// "github.com/OffchainLabs/prysm/v6/beacon-chain/core/feed/operation"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/core/helpers"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/p2p"

	// "github.com/OffchainLabs/prysm/v6/beacon-chain/slasher/types"
	"github.com/OffchainLabs/prysm/v6/beacon-chain/state"
	"github.com/OffchainLabs/prysm/v6/consensus-types/primitives"

	// "github.com/OffchainLabs/prysm/v6/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v6/monitoring/tracing"
	"github.com/OffchainLabs/prysm/v6/monitoring/tracing/trace"
	eth "github.com/OffchainLabs/prysm/v6/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v6/proto/prysm/v1alpha1/attestation"

	// "github.com/OffchainLabs/prysm/v6/proto/prysm/v1alpha1/attestation"
	"github.com/OffchainLabs/prysm/v6/runtime/version"
	"github.com/OffchainLabs/prysm/v6/time/slots"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"
)

// Validation
// - The block being voted for (attestation.data.beacon_block_root) passes validation.
// - The attestation's committee index (attestation.data.index) is for the correct subnet.
// - The attestation is unaggregated -- that is, it has exactly one participating validator (len(get_attesting_indices(state, attestation.data, attestation.aggregation_bits)) == 1).
// - attestation.data.slot is within the last ATTESTATION_PROPAGATION_SLOT_RANGE slots (attestation.data.slot + ATTESTATION_PROPAGATION_SLOT_RANGE >= current_slot >= attestation.data.slot).
// - The signature of attestation is valid.
func (s *Service) validateCommitteeIndexBeaconAttestation(
	ctx context.Context,
	pid peer.ID,
	msg *pubsub.Message,
) (pubsub.ValidationResult, error) {
	start := time.Now()
	defer func() {
		attestationVerificationGossipSummary.Observe(float64(time.Since(start).Milliseconds()))
	}()

	if pid == s.cfg.p2p.PeerID() {
		return pubsub.ValidationAccept, nil
	}
	// Attestation processing requires the target block to be present in the database, so we'll skip
	// validating or processing attestations until fully synced.
	if s.cfg.initialSync.Syncing() {
		return pubsub.ValidationIgnore, nil
	}

	ctx, span := trace.StartSpan(ctx, "sync.validateCommitteeIndexBeaconAttestation")
	defer span.End()

	// 1. Time to decode message

	decodeStartTime := time.Now()
	if msg.Topic == nil {
		return pubsub.ValidationReject, p2p.ErrInvalidTopic
	}

	m, err := s.decodePubsubMessage(msg)
	if err != nil {
		tracing.AnnotateError(span, err)
		return pubsub.ValidationReject, err
	}

	att, ok := m.(eth.Att)
	if !ok {
		return pubsub.ValidationReject, errWrongMessage
	}
	if err := helpers.ValidateNilAttestation(att); err != nil {
		return pubsub.ValidationReject, err
	}

	// 1. End Time to decode message
	timeToDecodeAttestation.Observe(float64(time.Since(decodeStartTime).Microseconds()))

	data := att.GetData()

	// Do not process slot 0 attestations.
	if data.Slot == 0 {
		return pubsub.ValidationIgnore, nil
	}

	// 2. Time to validate attestation slot and epoch
	validateStartTime := time.Now()
	// Attestation's slot is within ATTESTATION_PROPAGATION_SLOT_RANGE and early attestation
	// processing tolerance.
	if err := helpers.ValidateAttestationTime(data.Slot, s.cfg.clock.GenesisTime(), earlyAttestationProcessingTolerance); err != nil {
		tracing.AnnotateError(span, err)
		return pubsub.ValidationIgnore, err
	}
	if err := helpers.ValidateSlotTargetEpoch(data); err != nil {
		return pubsub.ValidationReject, err
	}

	// 2. End Time to validate attestation slot and epoch
	timeToValidateAttestationSlotAndEpoch.Observe(float64(time.Since(validateStartTime).Microseconds()))
	
	// 3. Time to generate attestation cache key
	generateAttestationCacheKeyStartTime := time.Now()
	committeeIndex := att.GetCommitteeIndex()

	// Generate cache key for unaggregated attestation tracking
	attKey, err := generateUnaggregatedAttCacheKey(att)
	if err != nil {
		log.WithError(err).Error("Could not generate cache key for attestation tracking")
		return pubsub.ValidationIgnore, nil
	}

	// 3. End Time to generate attestation cache key
	timeToGenerateAttestationCacheKey.Observe(float64(time.Since(generateAttestationCacheKeyStartTime).Microseconds()))

	// 4. Time to verify if attestation references a bad block
	timeToVerifyAttestationReferencesBadBlockStartTime := time.Now()
	if !s.slasherEnabled {
		// Verify this the first attestation received for the participating validator for the slot.
		if s.hasSeenUnaggregatedAtt(attKey) {
			return pubsub.ValidationIgnore, nil
		}
		// Reject an attestation if it references an invalid block.
		if s.hasBadBlock(bytesutil.ToBytes32(data.BeaconBlockRoot)) ||
			s.hasBadBlock(bytesutil.ToBytes32(data.Target.Root)) ||
			s.hasBadBlock(bytesutil.ToBytes32(data.Source.Root)) {
			attBadBlockCount.Inc()
			return pubsub.ValidationReject, errors.New("attestation data references bad block root")
		}
		// 4. End Time to verify if attestation references a bad block
		timeToVerifyAttestationReferencesBadBlock.Observe(float64(time.Since(timeToVerifyAttestationReferencesBadBlockStartTime).Microseconds()))
	}

	// 5. Time to verify if attestation is in fork choice
	timeToVerifyAttestationInForkChoiceStartTime := time.Now()
	// Verify the block being voted and the processed state is in beaconDB and the block has passed validation if it's in the beaconDB.
	blockRoot := bytesutil.ToBytes32(data.BeaconBlockRoot)
	if !s.hasBlockAndState(ctx, blockRoot) {
		s.savePendingAtt(att)
	}
	if !s.cfg.chain.InForkchoice(blockRoot) {
		tracing.AnnotateError(span, blockchain.ErrNotDescendantOfFinalized)
		return pubsub.ValidationIgnore, blockchain.ErrNotDescendantOfFinalized
	}
	// 5. End Time to verify if attestation is in fork choice
	timeToVerifyAttestationInForkChoice.Observe(float64(time.Since(timeToVerifyAttestationInForkChoiceStartTime).Microseconds()))
	// 6. Time to verify LMD GHOST and FFG consistency
	timeToVerifyLmdFfgConsistencyStartTime := time.Now()
	if err = s.cfg.chain.VerifyLmdFfgConsistency(ctx, att); err != nil {
		tracing.AnnotateError(span, err)
		attBadLmdConsistencyCount.Inc()
		return pubsub.ValidationReject, err
	}
	// 6. End Time to verify LMD GHOST and FFG consistency
	timeToVerifyLmdFfgConsistency.Observe(float64(time.Since(timeToVerifyLmdFfgConsistencyStartTime).Microseconds()))
	// 7. Time to retrieve attestation target state

	timeToRetrieveAttestationTargetStateStartTime := time.Now()
	preState, err := s.cfg.chain.AttestationTargetState(ctx, data.Target)
	if err != nil {
		tracing.AnnotateError(span, err)
		return pubsub.ValidationIgnore, err
	}
	// 7. End Time to retrieve attestation target state
	timeToRetrieveAttestationTargetState.Observe(float64(time.Since(timeToRetrieveAttestationTargetStateStartTime).Microseconds()))

	// 8. Time to validate unaggregated attestation topic
	timeToValidateUnaggregatedAttTopicStartTime := time.Now()
	validationRes, err := s.validateUnaggregatedAttTopic(ctx, att, preState, *msg.Topic)
	if validationRes != pubsub.ValidationAccept {
		return validationRes, err
	}
	// 8. End Time to validate unaggregated attestation topic
	timeToValidateUnaggregatedAttTopic.Observe(float64(time.Since(timeToValidateUnaggregatedAttTopicStartTime).Microseconds()))

	// 9. Time to get attestation committee
	timeToGetAttestationCommitteeStartTime := time.Now()
	committee, err := helpers.BeaconCommitteeFromState(ctx, preState, data.Slot, committeeIndex)
	if err != nil {
		tracing.AnnotateError(span, err)
		return pubsub.ValidationIgnore, err
	}
	// 9. End Time to get attestation committee
	timeToGetAttestationCommittee.Observe(float64(time.Since(timeToGetAttestationCommitteeStartTime).Microseconds()))

	// 10. Time to validate attester data
	timeToValidateAttesterDataStartTime := time.Now()
	validationRes, err = validateAttesterData(ctx, att, committee)
	if validationRes != pubsub.ValidationAccept {
		return validationRes, err
	}
	// 10. End Time to validate attester data
	timeToValidateAttesterData.Observe(float64(time.Since(timeToValidateAttesterDataStartTime).Microseconds()))
	// 11. Time to consoliate electra attestation
	// Consolidated handling of Electra SingleAttestation vs Phase0 unaggregated attestation
	timeToConsolidateElectraAttestationStartTime := time.Now()
	var (
		attForValidation eth.Att // what we'll pass to further validation
		eventType        feed.EventType
		eventData        interface{}
	)

	if att.Version() >= version.Electra {
		singleAtt, ok := att.(*eth.SingleAttestation)
		if !ok {
			return pubsub.ValidationIgnore, fmt.Errorf(
				"attestation has wrong type (expected %T, got %T)",
				&eth.SingleAttestation{}, att,
			)
		}
		// Convert Electra SingleAttestation to unaggregated ElectraAttestation. This is needed because many parts of the codebase assume that attestations have a certain structure and SingleAttestation validates these assumptions.
		attForValidation = singleAtt.ToAttestationElectra(committee)
		eventType = operation.SingleAttReceived
		eventData = &operation.SingleAttReceivedData{
			Attestation: singleAtt,
		}
	} else {
		// Phase0 unaggregated attestation
		attForValidation = att
		eventType = operation.UnaggregatedAttReceived
		eventData = &operation.UnAggregatedAttReceivedData{
			Attestation: att,
		}
	}
	// 11. End Time to consoliate electra attestation
	timeToConsolidateElectraAttestation.Observe(float64(time.Since(timeToConsolidateElectraAttestationStartTime).Microseconds()))
	// 12. Time to validate unaggregated attestation with state
	timeToValidateUnaggregatedAttWithStateStartTime := time.Now()
	validationRes, err = s.validateUnaggregatedAttWithState(ctx, attForValidation, preState)
	if validationRes != pubsub.ValidationAccept {
		return validationRes, err
	}
	// 12. End Time to validate unaggregated attestation with state
	timeToValidateUnaggregatedAttWithState.Observe(float64(time.Since(timeToValidateUnaggregatedAttWithStateStartTime).Microseconds()))
	if s.slasherEnabled {
		// Feed the indexed attestation to slasher if enabled. This action
		// is done in the background to avoid adding more load to this critical code path.
		go func() {
			// Using a different context to prevent timeouts as this operation can be expensive
			// and we want to avoid affecting the critical code path.
			ctx := context.TODO()
			preState, err := s.cfg.chain.AttestationTargetState(ctx, data.Target)
			if err != nil {
				log.WithError(err).Error("Could not retrieve pre state")
				tracing.AnnotateError(span, err)
				return
			}
			committee, err := helpers.BeaconCommitteeFromState(ctx, preState, data.Slot, committeeIndex)
			if err != nil {
				log.WithError(err).Error("Could not get attestation committee")
				tracing.AnnotateError(span, err)
				return
			}
			indexedAtt, err := attestation.ConvertToIndexed(ctx, attForValidation, committee)
			if err != nil {
				log.WithError(err).Error("Could not convert to indexed attestation")
				tracing.AnnotateError(span, err)
				return
			}
			s.cfg.slasherAttestationsFeed.Send(&types.WrappedIndexedAtt{IndexedAtt: indexedAtt})
		}()
	}

	// // Notify other services in the beacon node
	s.cfg.attestationNotifier.OperationFeed().Send(&feed.Event{
		Type: eventType,
		Data: eventData,
	})

	s.setSeenUnaggregatedAtt(attKey)

	// // Attach final validated attestation to the message for further pipeline use
	msg.ValidatorData = attForValidation

	return pubsub.ValidationAccept, nil
}

// This validates beacon unaggregated attestation has correct topic string.
func (s *Service) validateUnaggregatedAttTopic(ctx context.Context, a eth.Att, bs state.ReadOnlyBeaconState, t string) (pubsub.ValidationResult, error) {
	ctx, span := trace.StartSpan(ctx, "sync.validateUnaggregatedAttTopic")
	defer span.End()

	_, valCount, result, err := s.validateCommitteeIndexAndCount(ctx, a, bs)
	if result != pubsub.ValidationAccept {
		return result, err
	}
	subnet := helpers.ComputeSubnetForAttestation(valCount, a)
	format := p2p.GossipTypeMapping[reflect.TypeOf(&eth.Attestation{})]
	digest, err := s.currentForkDigest()
	if err != nil {
		tracing.AnnotateError(span, err)
		return pubsub.ValidationIgnore, err
	}
	if !strings.HasPrefix(t, fmt.Sprintf(format, digest, subnet)) {
		return pubsub.ValidationReject, errors.New("attestation's subnet does not match with pubsub topic")
	}

	return pubsub.ValidationAccept, nil
}

func (s *Service) validateCommitteeIndexAndCount(
	ctx context.Context,
	a eth.Att,
	bs state.ReadOnlyBeaconState,
) (primitives.CommitteeIndex, uint64, pubsub.ValidationResult, error) {
	// - [REJECT] attestation.data.index == 0
	if a.Version() >= version.Electra && a.GetData().CommitteeIndex != 0 {
		return 0, 0, pubsub.ValidationReject, errors.New("attestation data's committee index must be 0")
	}
	valCount, err := helpers.ActiveValidatorCount(ctx, bs, slots.ToEpoch(a.GetData().Slot))
	if err != nil {
		return 0, 0, pubsub.ValidationIgnore, err
	}
	count := helpers.SlotCommitteeCount(valCount)
	var ci primitives.CommitteeIndex
	if a.Version() >= version.Electra && !a.IsSingle() {
		bitCount := a.CommitteeBitsVal().Count()
		if bitCount == 0 {
			return 0, 0, pubsub.ValidationReject, fmt.Errorf("committee bits have no bit set")
		}
		if bitCount != 1 {
			return 0, 0, pubsub.ValidationReject, fmt.Errorf("expected 1 committee bit indice got %d", bitCount)
		}
		ci = primitives.CommitteeIndex(a.CommitteeBitsVal().BitIndices()[0])
	} else {
		ci = a.GetCommitteeIndex()
	}
	if uint64(ci) > count {
		return 0, 0, pubsub.ValidationReject, fmt.Errorf("committee index %d > %d", ci, count)
	}
	return ci, valCount, pubsub.ValidationAccept, nil
}

func validateAttesterData(
	ctx context.Context,
	a eth.Att,
	committee []primitives.ValidatorIndex,
) (pubsub.ValidationResult, error) {
	if a.Version() >= version.Electra {
		singleAtt, ok := a.(*eth.SingleAttestation)
		if !ok {
			return pubsub.ValidationIgnore, fmt.Errorf("attestation has wrong type (expected %T, got %T)", &eth.SingleAttestation{}, a)
		}
		return validateAttestingIndex(ctx, singleAtt.AttesterIndex, committee)
	}

	// Verify number of aggregation bits matches the committee size.
	if err := helpers.VerifyBitfieldLength(a.GetAggregationBits(), uint64(len(committee))); err != nil {
		return pubsub.ValidationReject, err
	}
	// Attestation must be unaggregated and the bit index must exist in the range of committee indices.
	// Note: The Ethereum Beacon chain spec suggests (len(get_attesting_indices(state, attestation.data, attestation.aggregation_bits)) == 1)
	// however this validation can be achieved without use of get_attesting_indices which is an O(n) lookup.
	if a.GetAggregationBits().Count() != 1 || a.GetAggregationBits().BitIndices()[0] >= len(committee) {
		return pubsub.ValidationReject, errors.New("attestation bitfield is invalid")
	}

	return pubsub.ValidationAccept, nil
}

// This validates beacon unaggregated attestation using the given state, the validation consists of signature verification.
func (s *Service) validateUnaggregatedAttWithState(ctx context.Context, a eth.Att, bs state.ReadOnlyBeaconState) (pubsub.ValidationResult, error) {
	ctx, span := trace.StartSpan(ctx, "sync.validateUnaggregatedAttWithState")
	defer span.End()

	set, err := blocks.AttestationSignatureBatch(ctx, bs, []eth.Att{a})
	if err != nil {
		tracing.AnnotateError(span, err)
		attBadSignatureBatchCount.Inc()
		return pubsub.ValidationReject, err
	}

	return s.validateWithBatchVerifier(ctx, "attestation", set)
}

func validateAttestingIndex(
	ctx context.Context,
	attestingIndex primitives.ValidatorIndex,
	committee []primitives.ValidatorIndex,
) (pubsub.ValidationResult, error) {
	_, span := trace.StartSpan(ctx, "sync.validateAttestingIndex")
	defer span.End()

	// _[REJECT]_ The attester is a member of the committee -- i.e.
	//  `attestation.attester_index in get_beacon_committee(state, attestation.data.slot, index)`.
	inCommittee := false
	for _, ix := range committee {
		if attestingIndex == ix {
			inCommittee = true
			break
		}
	}
	if !inCommittee {
		return pubsub.ValidationReject, errors.New("attester is not a member of the committee")
	}

	return pubsub.ValidationAccept, nil
}

// generateUnaggregatedAttCacheKey generates the cache key for unaggregated attestation tracking.
func generateUnaggregatedAttCacheKey(att eth.Att) (string, error) {
	var attester uint64
	if att.Version() >= version.Electra {
		if !att.IsSingle() {
			return "", errors.New("non-single Electra attestation")
		}
		attester = uint64(att.GetAttestingIndex())
	} else {
		aggBits := att.GetAggregationBits()
		if aggBits.Count() != 1 {
			return "", errors.New("attestation does not have exactly 1 bit set")
		}
		attester = uint64(att.GetAggregationBits().BitIndices()[0])
	}

	b := make([]byte, 24)
	binary.LittleEndian.PutUint64(b, uint64(att.GetData().Slot))
	binary.LittleEndian.PutUint64(b[8:16], uint64(att.GetCommitteeIndex()))
	binary.LittleEndian.PutUint64(b[16:], attester)
	return string(b), nil
}

// Returns true if the attestation was already seen for the participating validator for the slot.
func (s *Service) hasSeenUnaggregatedAtt(key string) bool {
	s.seenUnAggregatedAttestationLock.RLock()
	defer s.seenUnAggregatedAttestationLock.RUnlock()

	_, seen := s.seenUnAggregatedAttestationCache.Get(key)
	return seen
}

// Set an incoming attestation as seen for the participating validator for the slot.
func (s *Service) setSeenUnaggregatedAtt(key string) {
	s.seenUnAggregatedAttestationLock.Lock()
	defer s.seenUnAggregatedAttestationLock.Unlock()

	s.seenUnAggregatedAttestationCache.Add(key, true)
}

// hasBlockAndState returns true if the beacon node knows about a block and associated state in the
// database or cache.
func (s *Service) hasBlockAndState(ctx context.Context, blockRoot [32]byte) bool {
	hasStateSummary := s.cfg.beaconDB.HasStateSummary(ctx, blockRoot)
	hasState := hasStateSummary || s.cfg.beaconDB.HasState(ctx, blockRoot)
	return hasState && s.cfg.chain.HasBlock(ctx, blockRoot)
}
