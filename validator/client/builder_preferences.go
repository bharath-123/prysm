package client

import (
	"context"
	"fmt"

	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/time/slots"
	"github.com/sirupsen/logrus"
)

// buildBuilderPreferences builds signed per-builder preferences
// (Gloas/ePBS Builder API submitBuilderPreferences) for every validator that has
// a proposer duty in the NEXT epoch, so builders receive them in the epoch prior
// to the proposal as the spec recommends (driven by the proposer lookahead).
//
// One SubmitBuilderPreferencesRequest is produced per proposing validator; it
// carries one BuilderPreferencesRequestV1 per configured builder URL, each
// authenticated by a SignedRequestAuthV1 whose message data is that builder's
// URL. The result is empty unless builder URLs are configured.
//
// The mid-epoch timing gate mirrors the next-epoch branch of
// buildProposerPreferences: preferences are built at or after mid-epoch so the
// beacon node has processed the epoch transition. Pass force=true to bypass the
// gate (e.g. after a reorg changes duties). Already-submitted slots are tracked
// to avoid duplicate signing and RPC calls within an epoch.
func (v *validator) buildBuilderPreferences(
	ctx context.Context,
	slot primitives.Slot,
	force bool,
) []*ethpb.SubmitBuilderPreferencesRequest {
	if len(v.builderURLs) == 0 {
		return nil
	}
	currentEpoch := slots.ToEpoch(slot)
	gloasEpoch := params.BeaconConfig().GloasForkEpoch
	// Next-epoch proposals are only relevant once we are in (or one epoch before)
	// the Gloas fork, matching buildProposerPreferences.
	if currentEpoch+1 < gloasEpoch {
		return nil
	}
	epochStart, err := slots.EpochStart(currentEpoch)
	if err != nil {
		return nil
	}
	midEpoch := epochStart + params.BeaconConfig().SlotsPerEpoch/2
	if !force && slot < midEpoch {
		return nil
	}

	if force {
		v.submittedBuilderPrefSlots = make(map[primitives.Slot]bool)
	} else {
		for s := range v.submittedBuilderPrefSlots {
			if s < epochStart {
				delete(v.submittedBuilderPrefSlots, s)
			}
		}
	}

	v.dutiesLock.RLock()
	defer v.dutiesLock.RUnlock()
	if !v.duties.IsInitialized() {
		return nil
	}

	var reqs []*ethpb.SubmitBuilderPreferencesRequest
	var sigFailCount int
	for pk, duty := range v.duties.NextEpochDuties() {
		if len(duty.ProposerSlots) == 0 {
			continue
		}
		if duty.Status != ethpb.ValidatorStatus_ACTIVE && duty.Status != ethpb.ValidatorStatus_EXITING {
			continue
		}
		// Preferences are per-validator (independent of slot); submit once per
		// epoch using the validator's first proposal slot for the auth message.
		proposalSlot := duty.ProposerSlots[0]
		if v.submittedBuilderPrefSlots[proposalSlot] {
			continue
		}

		prefs := make([]*ethpb.BuilderPreferencesRequestV1, 0, len(v.builderURLs))
		signOK := true
		for _, url := range v.builderURLs {
			log.WithFields(logrus.Fields{
				"builderUrl":          url,
				"proposalSlot":        proposalSlot,
				"validatorIndex":      duty.ValidatorIndex,
				"maxExecutionPayment": v.builderMaxExecutionPayment,
			}).Info("BHARATH: Creating and signing builder preferences for builder")
			auth, err := v.signRequestAuth(ctx, pk, &ethpb.RequestAuthV1{
				Data: []byte(url),
				Slot: proposalSlot,
			})
			if err != nil {
				sigFailCount++
				signOK = false
				break
			}
			prefs = append(prefs, &ethpb.BuilderPreferencesRequestV1{
				Preferences: &ethpb.BuilderPreferencesV1{
					// Set from --builder-max-execution-payment (defaults to
					// MAX_EXECUTION_PAYMENT = 2**64-1, accept-any). A single global
					// value is applied to every builder. TODO(gloas): per-builder.
					MaxExecutionPayment: v.builderMaxExecutionPayment,
				},
				Auth: auth,
			})
		}
		if !signOK || len(prefs) == 0 {
			continue
		}

		pkCopy := pk
		reqs = append(reqs, &ethpb.SubmitBuilderPreferencesRequest{
			ValidatorPubkey: pkCopy[:],
			Preferences:     prefs,
		})
		v.submittedBuilderPrefSlots[proposalSlot] = true
		log.WithFields(logrus.Fields{
			"proposalSlot":   proposalSlot,
			"validatorIndex": duty.ValidatorIndex,
			"builders":       len(prefs),
		}).Info("BHARATH: Built builder preferences for proposing validator")
	}

	if sigFailCount > 0 {
		log.WithField("count", sigFailCount).Warn("Failed to sign builder preferences request auth")
	}
	log.WithFields(logrus.Fields{
		"slot":             slot,
		"epoch":            currentEpoch,
		"builders":         len(v.builderURLs),
		"validatorsBuilt":  len(reqs),
		"alreadySubmitted": len(v.submittedBuilderPrefSlots),
	}).Debug("Build builder preferences result")
	return reqs
}

// submitBuilderPreferences submits each proposing validator's builder
// preferences to the beacon node, which forwards them to the target builders.
// Submission is delayed to mid-slot so the block for this slot is processed
// first, mirroring the proposer-preferences submission.
func (v *validator) submitBuilderPreferences(ctx context.Context, reqs []*ethpb.SubmitBuilderPreferencesRequest) {
	log.WithField("validators", len(reqs)).Info("BHARATH: Sending builder preferences to beacon node")
	for _, req := range reqs {
		log.WithFields(logrus.Fields{
			"validatorPubkey": fmt.Sprintf("%#x", req.ValidatorPubkey),
			"builders":        len(req.Preferences),
		}).Info("BHARATH: Submitting builder preferences for validator")
		if _, err := v.validatorClient.SubmitBuilderPreferences(ctx, req); err != nil {
			log.WithError(err).Warn("Failed to submit builder preferences")
		}
	}
}
