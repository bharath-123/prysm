package beacon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/OffchainLabs/prysm/v7/api"
	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/gloas"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/peerdas"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/db"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/rpc/eth/shared"
	consensusblocks "github.com/OffchainLabs/prysm/v7/consensus-types/blocks"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	"github.com/OffchainLabs/prysm/v7/network/httputil"
	eth "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/runtime/version"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/pkg/errors"
)

// GetExecutionPayloadEnvelope retrieves a full execution payload envelope by beacon block root.
// The blinded envelope is fetched from the DB and the full execution payload is reconstructed
// from the EL via eth_getBlockByHash.
// PublishExecutionPayloadBid broadcasts a signed execution payload bid to the p2p network.
func (s *Server) PublishExecutionPayloadBid(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "beacon.PublishExecutionPayloadBid")
	defer span.End()

	if s.SyncChecker.Syncing() {
		httputil.HandleError(w, "Beacon node is syncing", http.StatusServiceUnavailable)
		return
	}

	versionHeader := r.Header.Get(api.VersionHeader)
	if versionHeader != version.String(version.Gloas) {
		httputil.HandleError(w, "Eth-Consensus-Version header must be \""+version.String(version.Gloas)+"\"", http.StatusBadRequest)
		return
	}

	var signedBid *eth.SignedExecutionPayloadBid
	if httputil.IsRequestSsz(r) {
		body, err := readRequestBody(r)
		if err != nil {
			httputil.HandleError(w, "could not read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		pb := &eth.SignedExecutionPayloadBid{}
		if err := pb.UnmarshalSSZ(body); err != nil {
			httputil.HandleError(w, "could not decode SSZ request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		signedBid = pb
	} else {
		var req structs.SignedExecutionPayloadBid
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.HandleError(w, "could not decode JSON request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		pb, err := req.ToConsensus()
		if err != nil {
			httputil.HandleError(w, "could not convert request to consensus type: "+err.Error(), http.StatusBadRequest)
			return
		}
		signedBid = pb
	}

	if signedBid.Message == nil {
		httputil.HandleError(w, "signed execution payload bid message is nil", http.StatusBadRequest)
		return
	}

	if err := s.Broadcaster.Broadcast(ctx, signedBid); err != nil {
		httputil.HandleError(w, "could not broadcast signed execution payload bid: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// GetExecutionPayloadEnvelope retrieves a full execution payload envelope by beacon block root.
// The blinded envelope is fetched from the DB and the full execution payload is reconstructed
// from the EL via eth_getBlockByHash.
func (s *Server) GetExecutionPayloadEnvelope(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "beacon.GetExecutionPayloadEnvelope")
	defer span.End()

	blockID := r.PathValue("block_id")
	if blockID == "" {
		httputil.HandleError(w, "block_id is required in URL params", http.StatusBadRequest)
		return
	}

	root, err := s.Blocker.BlockRoot(ctx, []byte(blockID))
	if !shared.WriteBlockRootFetchError(w, err) {
		return
	}

	blinded, err := s.BeaconDB.ExecutionPayloadEnvelope(ctx, root)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			httputil.HandleError(w, "execution payload envelope not found", http.StatusNotFound)
			return
		}
		httputil.HandleError(w, "could not retrieve execution payload envelope: "+err.Error(), http.StatusInternalServerError)
		return
	}
	full, err := s.ExecutionReconstructor.ReconstructExecutionPayloadEnvelope(ctx, blinded)
	if err != nil {
		httputil.HandleError(w, "could not reconstruct execution payload envelope: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set(api.VersionHeader, version.String(version.Gloas))

	if httputil.RespondWithSsz(r) {
		sszBytes, err := full.MarshalSSZ()
		if err != nil {
			httputil.HandleError(w, "could not marshal envelope to SSZ: "+err.Error(), http.StatusInternalServerError)
			return
		}
		httputil.WriteSsz(w, sszBytes)
		return
	}

	isOptimistic, err := s.OptimisticModeFetcher.IsOptimisticForRoot(ctx, root)
	if err != nil {
		httputil.HandleError(w, "could not check optimistic status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	finalized := s.FinalizationFetcher.IsFinalized(ctx, root)

	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(full)
	if err != nil {
		httputil.HandleError(w, "could not convert envelope to JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	httputil.WriteJson(w, &structs.GetExecutionPayloadEnvelopeResponse{
		Version:             version.String(version.Gloas),
		ExecutionOptimistic: isOptimistic,
		Finalized:           finalized,
		Data:                jsonEnvelope,
	})
}

// PublishExecutionPayloadEnvelope broadcasts a signed execution payload envelope to the p2p network.
// If blobs and cell_proofs are provided in the JSON body, data column sidecars are computed and
// broadcast to the network before the envelope is broadcast.
// SSZ requests may only carry the envelope (no blobs).
//
// POST /eth/v1/beacon/execution_payload_envelope
func (s *Server) PublishExecutionPayloadEnvelope(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "beacon.PublishExecutionPayloadEnvelope")
	defer span.End()

	if s.SyncChecker.Syncing() {
		httputil.HandleError(w, "Beacon node is syncing", http.StatusServiceUnavailable)
		return
	}

	versionHeader := r.Header.Get(api.VersionHeader)
	if versionHeader != version.String(version.Gloas) {
		httputil.HandleError(w, "Eth-Consensus-Version header must be \""+version.String(version.Gloas)+"\"", http.StatusBadRequest)
		return
	}

	var signedEnvelope *eth.SignedExecutionPayloadEnvelope
	var rawBlobs [][]byte
	var rawCellProofs [][]byte

	if httputil.IsRequestSsz(r) {
		body, err := readRequestBody(r)
		if err != nil {
			httputil.HandleError(w, "could not read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		pb := &eth.SignedExecutionPayloadEnvelope{}
		if err := pb.UnmarshalSSZ(body); err != nil {
			httputil.HandleError(w, "could not decode SSZ request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		signedEnvelope = pb
	} else {
		var req structs.PublishExecutionPayloadEnvelopeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.HandleError(w, "could not decode JSON request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		pb, err := req.SignedExecutionPayloadEnvelope.ToConsensus()
		if err != nil {
			httputil.HandleError(w, "could not convert request to consensus type: "+err.Error(), http.StatusBadRequest)
			return
		}
		signedEnvelope = pb

		// Decode optional blobs.
		for i, b := range req.Blobs {
			blob, err := hexutil.Decode(b)
			if err != nil {
				httputil.HandleError(w, fmt.Sprintf("invalid blob at index %d: %s", i, err.Error()), http.StatusBadRequest)
				return
			}
			rawBlobs = append(rawBlobs, blob)
		}
		for i, p := range req.CellProofs {
			proof, err := hexutil.Decode(p)
			if err != nil {
				httputil.HandleError(w, fmt.Sprintf("invalid cell_proof at index %d: %s", i, err.Error()), http.StatusBadRequest)
				return
			}
			rawCellProofs = append(rawCellProofs, proof)
		}
	}

	if signedEnvelope.Message == nil {
		httputil.HandleError(w, "signed execution payload envelope message is nil", http.StatusBadRequest)
		return
	}

	// If blobs were provided, compute and broadcast data columns first.
	// Data column availability must be ensured before the envelope is processed.
	if len(rawBlobs) > 0 {
		if err := s.broadcastEnvelopeDataColumns(ctx, w, signedEnvelope, rawBlobs, rawCellProofs); err != nil {
			return // broadcastEnvelopeDataColumns already wrote the HTTP error
		}
	}

	if err := s.Broadcaster.Broadcast(ctx, signedEnvelope); err != nil {
		httputil.HandleError(w, "could not broadcast signed execution payload envelope: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Process the envelope locally. libp2p's seen-message cache prevents the node from
	// re-receiving its own broadcast via gossip, so we must import it into forkchoice explicitly.
	if s.ExecutionPayloadEnvelopeReceiver != nil {
		roSigned, err := consensusblocks.WrappedROSignedExecutionPayloadEnvelope(signedEnvelope)
		if err != nil {
			httputil.HandleError(w, "could not wrap signed envelope for local processing: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := s.ExecutionPayloadEnvelopeReceiver.ReceiveExecutionPayloadEnvelope(ctx, roSigned); err != nil {
			httputil.HandleError(w, "could not process envelope locally: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// broadcastEnvelopeDataColumns computes data column sidecars from the supplied blobs+cellProofs
// and broadcasts them, making the data available before the envelope is received by peers.
func (s *Server) broadcastEnvelopeDataColumns(
	ctx context.Context,
	w http.ResponseWriter,
	envelope *eth.SignedExecutionPayloadEnvelope,
	rawBlobs [][]byte,
	rawCellProofs [][]byte,
) error {
	cellsPerBlob, proofsPerBlob, err := peerdas.ComputeCellsAndProofsFromFlat(rawBlobs, rawCellProofs)
	if err != nil {
		httputil.HandleError(w, "could not compute cells and proofs from blobs: "+err.Error(), http.StatusBadRequest)
		return errors.New("compute cells and proofs failed")
	}

	beaconBlockRoot := bytesutil.ToBytes32(envelope.Message.BeaconBlockRoot)
	roSidecars, err := peerdas.DataColumnSidecarsGloas(cellsPerBlob, proofsPerBlob, envelope.Message.Slot, beaconBlockRoot)
	if err != nil {
		httputil.HandleError(w, "could not build data column sidecars: "+err.Error(), http.StatusInternalServerError)
		return errors.New("build data column sidecars failed")
	}

	verifiedSidecars := make([]consensusblocks.VerifiedRODataColumn, 0, len(roSidecars))
	for _, sc := range roSidecars {
		verifiedSidecars = append(verifiedSidecars, consensusblocks.NewVerifiedRODataColumn(sc))
	}

	if err := s.Broadcaster.BroadcastDataColumnSidecars(ctx, verifiedSidecars); err != nil {
		httputil.HandleError(w, "could not broadcast data column sidecars: "+err.Error(), http.StatusInternalServerError)
		return errors.New("broadcast data column sidecars failed")
	}

	if s.DataColumnReceiver != nil {
		if err := s.DataColumnReceiver.ReceiveDataColumns(verifiedSidecars); err != nil {
			log.WithError(err).Warn("Failed to receive data columns locally after broadcast")
		}
	}

	return nil
}

// ConstructExecutionPayloadEnvelope accepts an execution payload and execution requests from a
// builder, computes the resulting post-envelope state root, and returns the complete
// ExecutionPayloadEnvelope ready for the builder to sign and broadcast.
//
// POST /eth/v1/builder/execution_payload_envelope
func (s *Server) ConstructExecutionPayloadEnvelope(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "beacon.ConstructExecutionPayloadEnvelope")
	defer span.End()

	versionHeader := r.Header.Get(api.VersionHeader)
	if versionHeader != version.String(version.Gloas) {
		httputil.HandleError(w, "Eth-Consensus-Version header must be \""+version.String(version.Gloas)+"\"", http.StatusBadRequest)
		return
	}

	var req structs.ConstructExecutionPayloadEnvelopeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.HandleError(w, "could not decode request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ExecutionPayload == nil {
		httputil.HandleError(w, "execution_payload is required", http.StatusBadRequest)
		return
	}
	if req.ExecutionRequests == nil {
		httputil.HandleError(w, "execution_requests is required", http.StatusBadRequest)
		return
	}

	rootBytes, err := hexutil.Decode(req.BeaconBlockRoot)
	if err != nil {
		httputil.HandleError(w, "invalid beacon_block_root: "+err.Error(), http.StatusBadRequest)
		return
	}
	blockRoot := bytesutil.ToBytes32(rootBytes)

	// Fetch the pre-envelope state (state after beacon block processing, before envelope).
	if !s.FinalizationFetcher.InForkchoice(blockRoot) {
		httputil.HandleError(w, fmt.Sprintf("beacon block root %#x not found in forkchoice", blockRoot), http.StatusBadRequest)
		return
	}
	preSt, err := s.StateGenService.StateByRoot(ctx, blockRoot)
	if err != nil {
		httputil.HandleError(w, "could not fetch pre-state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if preSt == nil || preSt.IsNil() {
		httputil.HandleError(w, "nil pre-state for beacon block root", http.StatusInternalServerError)
		return
	}

	execPayload, err := req.ExecutionPayload.ToConsensus()
	if err != nil {
		httputil.HandleError(w, "invalid execution_payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	execRequests, err := req.ExecutionRequests.ToConsensus()
	if err != nil {
		httputil.HandleError(w, "invalid execution_requests: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Derive builder_index and slot from the committed bid in the pre-state.
	bid, err := preSt.LatestExecutionPayloadBid()
	if err != nil {
		httputil.HandleError(w, "could not get latest execution payload bid from state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if bid == nil {
		httputil.HandleError(w, "no committed execution payload bid found in state", http.StatusBadRequest)
		return
	}

	// Build a partial envelope proto (no state_root yet) so ApplyExecutionPayload can validate it.
	envelopeProto := &eth.ExecutionPayloadEnvelope{
		Payload:           execPayload,
		ExecutionRequests: execRequests,
		BuilderIndex:      bid.BuilderIndex(),
		BeaconBlockRoot:   blockRoot[:],
		Slot:              preSt.Slot(),
		StateRoot:         make([]byte, 32), // placeholder; computed below
	}
	envelopeRO, err := consensusblocks.WrappedROExecutionPayloadEnvelope(envelopeProto)
	if err != nil {
		httputil.HandleError(w, "could not wrap envelope: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Apply the payload to the pre-state (validates bid consistency and mutates state).
	if err := gloas.ApplyExecutionPayload(ctx, preSt, envelopeRO); err != nil {
		httputil.HandleError(w, "invalid execution payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Compute the post-envelope state root.
	stateRoot, err := preSt.HashTreeRoot(ctx)
	if err != nil {
		httputil.HandleError(w, "could not compute state root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	envelopeProto.StateRoot = stateRoot[:]

	payloadJSON, err := structs.ExecutionPayloadDenebFromConsensus(execPayload)
	if err != nil {
		httputil.HandleError(w, "could not convert payload to JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}
	requestsJSON := structs.ExecutionRequestsFromConsensus(execRequests)

	w.Header().Set(api.VersionHeader, version.String(version.Gloas))
	httputil.WriteJson(w, &structs.ConstructExecutionPayloadEnvelopeResponse{
		Version: version.String(version.Gloas),
		Data: &structs.ExecutionPayloadEnvelope{
			Payload:           payloadJSON,
			ExecutionRequests: requestsJSON,
			BuilderIndex:      fmt.Sprintf("%d", envelopeProto.BuilderIndex),
			BeaconBlockRoot:   hexutil.Encode(envelopeProto.BeaconBlockRoot),
			Slot:              fmt.Sprintf("%d", envelopeProto.Slot),
			StateRoot:         hexutil.Encode(envelopeProto.StateRoot),
		},
	})
}
