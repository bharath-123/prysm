package structs

import (
	"encoding/json"
)

type BlockRootResponse struct {
	Data                *BlockRoot `json:"data"`
	ExecutionOptimistic bool       `json:"execution_optimistic"`
	Finalized           bool       `json:"finalized"`
}

type BlockRoot struct {
	Root string `json:"root"`
}

type GetCommitteesResponse struct {
	Data                []*Committee `json:"data"`
	ExecutionOptimistic bool         `json:"execution_optimistic"`
	Finalized           bool         `json:"finalized"`
}

type ListAttestationsResponse struct {
	Version string          `json:"version,omitempty"`
	Data    json.RawMessage `json:"data"`
}

type SubmitAttestationsRequest struct {
	Data json.RawMessage `json:"data"`
}

type ListVoluntaryExitsResponse struct {
	Data []*SignedVoluntaryExit `json:"data"`
}

type SubmitSyncCommitteeSignaturesRequest struct {
	Data []*SyncCommitteeMessage `json:"data"`
}

type GetStateForkResponse struct {
	Data                *Fork `json:"data"`
	ExecutionOptimistic bool  `json:"execution_optimistic"`
	Finalized           bool  `json:"finalized"`
}

type GetFinalityCheckpointsResponse struct {
	ExecutionOptimistic bool                 `json:"execution_optimistic"`
	Finalized           bool                 `json:"finalized"`
	Data                *FinalityCheckpoints `json:"data"`
}

type FinalityCheckpoints struct {
	PreviousJustified *Checkpoint `json:"previous_justified"`
	CurrentJustified  *Checkpoint `json:"current_justified"`
	Finalized         *Checkpoint `json:"finalized"`
}

type GetGenesisResponse struct {
	Data *Genesis `json:"data"`
}

type Genesis struct {
	GenesisTime           string `json:"genesis_time"`
	GenesisValidatorsRoot string `json:"genesis_validators_root"`
	GenesisForkVersion    string `json:"genesis_fork_version"`
}

type GetBlockHeadersResponse struct {
	Data                []*SignedBeaconBlockHeaderContainer `json:"data"`
	ExecutionOptimistic bool                                `json:"execution_optimistic"`
	Finalized           bool                                `json:"finalized"`
}

type GetBlockHeaderResponse struct {
	ExecutionOptimistic bool                              `json:"execution_optimistic"`
	Finalized           bool                              `json:"finalized"`
	Data                *SignedBeaconBlockHeaderContainer `json:"data"`
}

type GetValidatorsRequest struct {
	Ids      []string `json:"ids,omitempty"`
	Statuses []string `json:"statuses,omitempty"`
}

type GetValidatorsResponse struct {
	ExecutionOptimistic bool                  `json:"execution_optimistic"`
	Finalized           bool                  `json:"finalized"`
	Data                []*ValidatorContainer `json:"data"`
}

type GetValidatorResponse struct {
	ExecutionOptimistic bool                `json:"execution_optimistic"`
	Finalized           bool                `json:"finalized"`
	Data                *ValidatorContainer `json:"data"`
}

type GetValidatorBalancesResponse struct {
	ExecutionOptimistic bool                `json:"execution_optimistic"`
	Finalized           bool                `json:"finalized"`
	Data                []*ValidatorBalance `json:"data"`
}

type GetValidatorIdentitiesResponse struct {
	ExecutionOptimistic bool                 `json:"execution_optimistic"`
	Finalized           bool                 `json:"finalized"`
	Data                []*ValidatorIdentity `json:"data"`
}

type ValidatorContainer struct {
	Index     string     `json:"index"`
	Balance   string     `json:"balance"`
	Status    string     `json:"status"`
	Validator *Validator `json:"validator"`
}

type ValidatorBalance struct {
	Index   string `json:"index"`
	Balance string `json:"balance"`
}

type ValidatorIdentity struct {
	Index           string `json:"index"`
	Pubkey          string `json:"pubkey"`
	ActivationEpoch string `json:"activation_epoch"`
}

type GetBlockResponse struct {
	Data *SignedBlock `json:"data"`
}

type GetBlockV2Response struct {
	Version             string       `json:"version"`
	ExecutionOptimistic bool         `json:"execution_optimistic"`
	Finalized           bool         `json:"finalized"`
	Data                *SignedBlock `json:"data"`
}

type SignedBlock struct {
	Message   json.RawMessage `json:"message"` // represents the block values based on the version
	Signature string          `json:"signature"`
}

type GetBlockAttestationsResponse struct {
	ExecutionOptimistic bool           `json:"execution_optimistic"`
	Finalized           bool           `json:"finalized"`
	Data                []*Attestation `json:"data"`
}

type GetBlockAttestationsV2Response struct {
	Version             string          `json:"version"`
	ExecutionOptimistic bool            `json:"execution_optimistic"`
	Finalized           bool            `json:"finalized"`
	Data                json.RawMessage `json:"data"` // Accepts both `Attestation` and `AttestationElectra` types
}

type GetStateRootResponse struct {
	ExecutionOptimistic bool       `json:"execution_optimistic"`
	Finalized           bool       `json:"finalized"`
	Data                *StateRoot `json:"data"`
}

type StateRoot struct {
	Root string `json:"root"`
}

type GetRandaoResponse struct {
	ExecutionOptimistic bool    `json:"execution_optimistic"`
	Finalized           bool    `json:"finalized"`
	Data                *Randao `json:"data"`
}

type Randao struct {
	Randao string `json:"randao"`
}

type GetSyncCommitteeResponse struct {
	ExecutionOptimistic bool                     `json:"execution_optimistic"`
	Finalized           bool                     `json:"finalized"`
	Data                *SyncCommitteeValidators `json:"data"`
}

type SyncCommitteeValidators struct {
	Validators          []string   `json:"validators"`
	ValidatorAggregates [][]string `json:"validator_aggregates"`
}

type BLSToExecutionChangesPoolResponse struct {
	Data []*SignedBLSToExecutionChange `json:"data"`
}

type GetAttesterSlashingsResponse struct {
	Version string          `json:"version,omitempty"`
	Data    json.RawMessage `json:"data"` // Accepts both `[]*AttesterSlashing` and `[]*AttesterSlashingElectra` types
}

type GetProposerSlashingsResponse struct {
	Data []*ProposerSlashing `json:"data"`
}

type GetWeakSubjectivityResponse struct {
	Data *WeakSubjectivityData `json:"data"`
}

type WeakSubjectivityData struct {
	WsCheckpoint *Checkpoint `json:"ws_checkpoint"`
	StateRoot    string      `json:"state_root"`
}

type GetIndividualVotesRequest struct {
	Epoch      string   `json:"epoch"`
	PublicKeys []string `json:"public_keys,omitempty"`
	Indices    []string `json:"indices,omitempty"`
}

type GetIndividualVotesResponse struct {
	IndividualVotes []*IndividualVote `json:"individual_votes"`
}

type IndividualVote struct {
	Epoch                            string `json:"epoch"`
	PublicKey                        string `json:"public_keys,omitempty"`
	ValidatorIndex                   string `json:"validator_index"`
	IsSlashed                        bool   `json:"is_slashed"`
	IsWithdrawableInCurrentEpoch     bool   `json:"is_withdrawable_in_current_epoch"`
	IsActiveInCurrentEpoch           bool   `json:"is_active_in_current_epoch"`
	IsActiveInPreviousEpoch          bool   `json:"is_active_in_previous_epoch"`
	IsCurrentEpochAttester           bool   `json:"is_current_epoch_attester"`
	IsCurrentEpochTargetAttester     bool   `json:"is_current_epoch_target_attester"`
	IsPreviousEpochAttester          bool   `json:"is_previous_epoch_attester"`
	IsPreviousEpochTargetAttester    bool   `json:"is_previous_epoch_target_attester"`
	IsPreviousEpochHeadAttester      bool   `json:"is_previous_epoch_head_attester"`
	CurrentEpochEffectiveBalanceGwei string `json:"current_epoch_effective_balance_gwei"`
	InclusionSlot                    string `json:"inclusion_slot"`
	InclusionDistance                string `json:"inclusion_distance"`
	InactivityScore                  string `json:"inactivity_score"`
}

type ChainHead struct {
	HeadSlot                   string `json:"head_slot"`
	HeadEpoch                  string `json:"head_epoch"`
	HeadBlockRoot              string `json:"head_block_root"`
	FinalizedSlot              string `json:"finalized_slot"`
	FinalizedEpoch             string `json:"finalized_epoch"`
	FinalizedBlockRoot         string `json:"finalized_block_root"`
	JustifiedSlot              string `json:"justified_slot"`
	JustifiedEpoch             string `json:"justified_epoch"`
	JustifiedBlockRoot         string `json:"justified_block_root"`
	PreviousJustifiedSlot      string `json:"previous_justified_slot"`
	PreviousJustifiedEpoch     string `json:"previous_justified_epoch"`
	PreviousJustifiedBlockRoot string `json:"previous_justified_block_root"`
	OptimisticStatus           bool   `json:"optimistic_status"`
}

type GetPendingConsolidationsResponse struct {
	Version             string                  `json:"version"`
	ExecutionOptimistic bool                    `json:"execution_optimistic"`
	Finalized           bool                    `json:"finalized"`
	Data                []*PendingConsolidation `json:"data"`
}

type GetPendingDepositsResponse struct {
	Version             string            `json:"version"`
	ExecutionOptimistic bool              `json:"execution_optimistic"`
	Finalized           bool              `json:"finalized"`
	Data                []*PendingDeposit `json:"data"`
}

type GetPendingPartialWithdrawalsResponse struct {
	Version             string                      `json:"version"`
	ExecutionOptimistic bool                        `json:"execution_optimistic"`
	Finalized           bool                        `json:"finalized"`
	Data                []*PendingPartialWithdrawal `json:"data"`
}

type GetProposerLookaheadResponse struct {
	Version             string   `json:"version"`
	ExecutionOptimistic bool     `json:"execution_optimistic"`
	Finalized           bool     `json:"finalized"`
	Data                []string `json:"data"` // validator indexes
}

type GetBlobsResponse struct {
	ExecutionOptimistic bool     `json:"execution_optimistic"`
	Finalized           bool     `json:"finalized"`
	Data                []string `json:"data"` //blobs
}

type GetExecutionPayloadEnvelopeResponse struct {
	Version             string                          `json:"version"`
	ExecutionOptimistic bool                            `json:"execution_optimistic"`
	Finalized           bool                            `json:"finalized"`
	Data                *SignedExecutionPayloadEnvelope `json:"data"`
}

type SSZQueryRequest struct {
	Query        string `json:"query"`
	IncludeProof bool   `json:"include_proof,omitempty"`
}

// ActiveBlobStreamingTicket is a single ticket as exposed via the Heze
// blob-streaming active-tickets endpoint.
type ActiveBlobStreamingTicket struct {
	TicketID   string `json:"ticket_id"`
	TargetSlot string `json:"target_slot"`
	Owner      string `json:"owner"`
	BLSPubkey  string `json:"bls_pubkey"`
	BlobCount  string `json:"blob_count"`
}

type GetActiveBlobStreamingTicketsResponse struct {
	Data []*ActiveBlobStreamingTicket `json:"data"`
}

// SubmitAotBlobsRequest is the body of the Heze blob-streaming AOT blob
// submission endpoint. A submitter provides the full blobs (with their KZG
// commitments and cell proofs) for a single active ticket; the node converts
// them to AOT data columns, stages them, and propagates them to peers.
type SubmitAotBlobsRequest struct {
	// TicketID is the active ticket the blobs are submitted under.
	TicketID string `json:"ticket_id"`
	// BlobInfoSignature is the ticket owner's BLS signature (96 bytes, hex) over
	// the AOTBlobInfo reconstructed from TicketID, the ticket's target slot, and
	// the submitted KZG commitments.
	BlobInfoSignature string `json:"blob_info_signature"`
	// Blobs is the set of blobs in the bundle, in commitment order.
	Blobs []*AotBlobInput `json:"blobs"`
}

// AotDataColumnCacheBundle is a cell-free view of one staged AOT bundle, exposed by
// the debugging endpoint that dumps the AOT data column cache.
type AotDataColumnCacheBundle struct {
	// CommitmentsRoot is the bundle key (HTR of the KZG commitment list), hex.
	CommitmentsRoot string `json:"commitments_root"`
	// TicketID is the ticket the columns were submitted under.
	TicketID string `json:"ticket_id"`
	// TargetSlot is the slot the bundle's blobs target.
	TargetSlot string `json:"target_slot"`
	// BlobCount is the number of blobs (KZG commitments) in the bundle.
	BlobCount string `json:"blob_count"`
	// Commitments are the bundle's blob KZG commitments, hex-encoded.
	Commitments []string `json:"commitments"`
	// StoredColumnIndices are the column indices currently staged for the bundle.
	StoredColumnIndices []string `json:"stored_column_indices"`
}

type GetAotDataColumnsResponse struct {
	Data []*AotDataColumnCacheBundle `json:"data"`
}

// AotBlobInput is a single full blob plus the cryptographic material needed to
// build its share of every AOT data column.
type AotBlobInput struct {
	// Blob is the full blob (BytesPerBlob), hex-encoded.
	Blob string `json:"blob"`
	// KzgCommitment is the blob's KZG commitment (48 bytes), hex-encoded.
	KzgCommitment string `json:"kzg_commitment"`
	// KzgCellProofs are the per-cell KZG proofs (NumberOfColumns proofs, 48 bytes
	// each), hex-encoded, ordered by cell/column index.
	KzgCellProofs []string `json:"kzg_cell_proofs"`
}
