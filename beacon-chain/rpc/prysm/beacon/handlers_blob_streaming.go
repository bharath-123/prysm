package beacon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/kzg"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/peerdas"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/signing"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	"github.com/OffchainLabs/prysm/v7/network/httputil"
	enginev1 "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// kzgCommitmentLength is the byte length of a KZG commitment and of a single
// cell KZG proof.
const kzgCommitmentLength = 48

// GetActiveBlobStreamingTickets returns the set of currently active
// blob-streaming tickets cached from the EL's most recent
// engine_forkchoiceUpdatedV5 response. Tickets are returned ordered by ID for
// stable diffing across calls.
//
// Response: 200 with a JSON list. 503 when the cache is not wired (Heze
// feature disabled).
func (s *Server) GetActiveBlobStreamingTickets(w http.ResponseWriter, r *http.Request) {
	_, span := trace.StartSpan(r.Context(), "beacon.GetActiveBlobStreamingTickets")
	defer span.End()

	if s.TicketCache == nil {
		httputil.HandleError(w, "Blob-streaming ticket cache is not enabled", http.StatusServiceUnavailable)
		return
	}

	tickets := s.TicketCache.All()
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })

	out := make([]*structs.ActiveBlobStreamingTicket, len(tickets))
	for i, t := range tickets {
		owner := common.Address(t.Owner)
		pubkey := hexutil.Encode(t.BLSPubkey[:])
		out[i] = &structs.ActiveBlobStreamingTicket{
			TicketID:   strconv.FormatUint(t.ID, 10),
			TargetSlot: strconv.FormatUint(uint64(t.TargetSlot), 10),
			Owner:      owner.Hex(),
			BLSPubkey:  pubkey,
			BlobCount:  strconv.FormatUint(t.BlobCount, 10),
		}
	}

	httputil.WriteJson(w, &structs.GetActiveBlobStreamingTicketsResponse{Data: out})
}

// GetAotDataColumns returns a cell-free dump of the AOT data column cache: one entry
// per staged bundle with its commitments root, ticket, target slot, commitments, and
// the column indices stashed so far. This is a debugging endpoint; it deliberately
// omits the (large) column cell data. Bundles are returned ordered by commitments root
// for stable diffing across calls.
//
// Response: 200 with a JSON list. 503 when the cache is not wired (Heze feature disabled).
func (s *Server) GetAotDataColumns(w http.ResponseWriter, r *http.Request) {
	_, span := trace.StartSpan(r.Context(), "beacon.GetAotDataColumns")
	defer span.End()

	if s.AotDataColumnCache == nil {
		httputil.HandleError(w, "AOT data column cache is not enabled", http.StatusServiceUnavailable)
		return
	}

	summaries := s.AotDataColumnCache.Summaries()
	sort.Slice(summaries, func(i, j int) bool {
		return bytes.Compare(summaries[i].CommitmentsRoot[:], summaries[j].CommitmentsRoot[:]) < 0
	})

	out := make([]*structs.AotDataColumnCacheBundle, len(summaries))
	for i, b := range summaries {
		commitments := make([]string, len(b.Commitments))
		for j, c := range b.Commitments {
			commitments[j] = hexutil.Encode(c)
		}
		indices := make([]string, len(b.StoredIndices))
		for j, idx := range b.StoredIndices {
			indices[j] = strconv.FormatUint(idx, 10)
		}
		out[i] = &structs.AotDataColumnCacheBundle{
			CommitmentsRoot:     hexutil.Encode(b.CommitmentsRoot[:]),
			TicketID:            strconv.FormatUint(b.TicketID, 10),
			TargetSlot:          strconv.FormatUint(uint64(b.TargetSlot), 10),
			BlobCount:           strconv.FormatUint(uint64(len(b.Commitments)), 10),
			Commitments:         commitments,
			StoredColumnIndices: indices,
		}
	}

	httputil.WriteJson(w, &structs.GetAotDataColumnsResponse{Data: out})
}

// SubmitAotBlobs accepts a set of full blobs (with their KZG commitments and
// cell proofs) for a single active ticket, converts them to ahead-of-time (AOT)
// data column sidecars, stages them in the AOT data column cache, and propagates
// each on its gossip subnet.
//
// Both the ticket owner's blob-info signature and the submitted cell KZG proofs
// are verified here so that an invalid submission is rejected with a 400 before
// anything is staged or broadcast to the network.
//
// Response: 200 on success. 400 on a malformed body or failed signature/proof
// verification. 404 when the ticket is unknown. 503 when the caches are not
// wired (Heze feature disabled).
func (s *Server) SubmitAotBlobs(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "beacon.SubmitAotBlobs")
	defer span.End()

	if s.TicketCache == nil || s.AotDataColumnCache == nil {
		httputil.HandleError(w, "Blob-streaming caches are not enabled", http.StatusServiceUnavailable)
		return
	}

	var req structs.SubmitAotBlobsRequest
	switch err := json.NewDecoder(r.Body).Decode(&req); {
	case errors.Is(err, io.EOF):
		httputil.HandleError(w, "No data submitted", http.StatusBadRequest)
		return
	case err != nil:
		httputil.HandleError(w, "Could not decode request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Blobs) == 0 {
		httputil.HandleError(w, "No blobs submitted", http.StatusBadRequest)
		return
	}

	ticketID, err := strconv.ParseUint(req.TicketID, 10, 64)
	if err != nil {
		httputil.HandleError(w, "Invalid ticket_id: "+err.Error(), http.StatusBadRequest)
		return
	}

	ticket, ok := s.TicketCache.ByID(ticketID)
	if !ok {
		httputil.HandleError(w, fmt.Sprintf("Unknown ticket_id %d", ticketID), http.StatusNotFound)
		return
	}

	signature, err := decodeFixed(req.BlobInfoSignature, fieldparams.BLSSignatureLength, "blob_info_signature")
	if err != nil {
		httputil.HandleError(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Decode the blobs and compute their extended cells. The cell KZG proofs come
	// from the request; only the cells are derived here.
	commitments := make([][]byte, len(req.Blobs))
	cellsPerBlob := make([][]kzg.Cell, len(req.Blobs))
	proofsPerBlob := make([][]kzg.Proof, len(req.Blobs))
	for i, in := range req.Blobs {
		commitment, err := decodeFixed(in.KzgCommitment, kzgCommitmentLength, fmt.Sprintf("blobs[%d].kzg_commitment", i))
		if err != nil {
			httputil.HandleError(w, err.Error(), http.StatusBadRequest)
			return
		}
		commitments[i] = commitment

		blobBytes, err := decodeFixed(in.Blob, kzg.BytesPerBlob, fmt.Sprintf("blobs[%d].blob", i))
		if err != nil {
			httputil.HandleError(w, err.Error(), http.StatusBadRequest)
			return
		}
		var blob kzg.Blob
		copy(blob[:], blobBytes)
		cells, err := kzg.ComputeCells(&blob)
		if err != nil {
			httputil.HandleError(w, fmt.Sprintf("Could not compute cells for blobs[%d]: %s", i, err.Error()), http.StatusBadRequest)
			return
		}
		cellsPerBlob[i] = cells

		if len(in.KzgCellProofs) != fieldparams.NumberOfColumns {
			httputil.HandleError(w, fmt.Sprintf("blobs[%d].kzg_cell_proofs must have %d entries, got %d", i, fieldparams.NumberOfColumns, len(in.KzgCellProofs)), http.StatusBadRequest)
			return
		}
		proofs := make([]kzg.Proof, len(in.KzgCellProofs))
		for j, p := range in.KzgCellProofs {
			proofBytes, err := decodeFixed(p, kzgCommitmentLength, fmt.Sprintf("blobs[%d].kzg_cell_proofs[%d]", i, j))
			if err != nil {
				httputil.HandleError(w, err.Error(), http.StatusBadRequest)
				return
			}
			copy(proofs[j][:], proofBytes)
		}
		proofsPerBlob[i] = proofs
	}

	// Verify the ticket owner's signature over the reconstructed AOTBlobInfo.
	if err := verifyAotBlobInfoSignature(ticketID, ticket.TargetSlot, commitments, ticket.BLSPubkey[:], signature); err != nil {
		httputil.HandleError(w, "Invalid blob_info_signature: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify the cell KZG proofs of every submitted blob in a single batch.
	if err := verifyAotCellProofs(commitments, cellsPerBlob, proofsPerBlob); err != nil {
		httputil.HandleError(w, "Invalid KZG proofs: "+err.Error(), http.StatusBadRequest)
		return
	}

	sidecars, err := peerdas.AotDataColumnSidecars(cellsPerBlob, proofsPerBlob, commitments, ticketID, ticket.TargetSlot, signature)
	if err != nil {
		httputil.HandleError(w, "Could not build AOT data column sidecars: "+err.Error(), http.StatusInternalServerError)
		return
	}

	for _, sidecar := range sidecars {
		if err := s.AotDataColumnCache.Stash(sidecar); err != nil {
			httputil.HandleError(w, "Could not stage AOT data column: "+err.Error(), http.StatusInternalServerError)
			return
		}
		subnet := peerdas.ComputeSubnetForDataColumnSidecar(sidecar.GetIndex())
		if err := s.Broadcaster.BroadcastAotDataColumnSidecar(ctx, subnet, sidecar); err != nil {
			httputil.HandleError(w, "Could not broadcast AOT data column: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

// verifyAotBlobInfoSignature reconstructs the AOTBlobInfo signed by the ticket
// owner and checks the signature against the ticket's BLS public key, using the
// plain DOMAIN_AOT_BLOB domain (no fork or genesis mixing), per the spec
// is_valid_aot_blob_info_signature.
func verifyAotBlobInfoSignature(ticketID uint64, targetSlot primitives.Slot, commitments [][]byte, pubkey, signature []byte) error {
	info := &enginev1.AOTBlobInfo{
		TicketId:           ticketID,
		TargetSlot:         targetSlot,
		BlobKzgCommitments: commitments,
	}
	domain, err := signing.ComputeDomain(params.BeaconConfig().DomainAotBlob, nil, nil)
	if err != nil {
		return errors.New("could not compute domain")
	}
	signingRoot, err := signing.ComputeSigningRoot(info, domain)
	if err != nil {
		return errors.New("could not compute signing root")
	}
	pub, err := bls.PublicKeyFromBytes(pubkey)
	if err != nil {
		return errors.New("invalid ticket public key")
	}
	valid, err := bls.VerifySignature(signature, signingRoot, pub)
	if err != nil {
		return errors.New("could not verify signature")
	}
	if !valid {
		return errors.New("signature does not verify")
	}
	return nil
}

// verifyAotCellProofs batch-verifies the cell KZG proofs for every submitted
// blob. For each blob, all NUMBER_OF_COLUMNS cells are checked against that
// blob's commitment, with the cell index equal to the column index.
func verifyAotCellProofs(commitments [][]byte, cellsPerBlob [][]kzg.Cell, proofsPerBlob [][]kzg.Proof) error {
	var (
		commitmentBatch []kzg.Bytes48
		cellIndices     []uint64
		cellBatch       []kzg.Cell
		proofBatch      []kzg.Bytes48
	)
	for blob, cells := range cellsPerBlob {
		var commitment kzg.Bytes48
		copy(commitment[:], commitments[blob])
		for col := range cells {
			var proof kzg.Bytes48
			copy(proof[:], proofsPerBlob[blob][col][:])
			commitmentBatch = append(commitmentBatch, commitment)
			cellIndices = append(cellIndices, uint64(col))
			cellBatch = append(cellBatch, cells[col])
			proofBatch = append(proofBatch, proof)
		}
	}
	valid, err := kzg.VerifyCellKZGProofBatch(commitmentBatch, cellIndices, cellBatch, proofBatch)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("cell KZG proof batch does not verify")
	}
	return nil
}

// decodeFixed decodes a 0x-prefixed hex string and asserts its byte length.
func decodeFixed(s string, wantLen int, name string) ([]byte, error) {
	b, err := hexutil.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %s", name, err.Error())
	}
	if len(b) != wantLen {
		return nil, fmt.Errorf("invalid %s: expected %d bytes, got %d", name, wantLen, len(b))
	}
	return b, nil
}
