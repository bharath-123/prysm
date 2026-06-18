package peerdas

import (
	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/kzg"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
)

// AotDataColumnSidecars builds the full set of NUMBER_OF_COLUMNS AOTDataColumnSidecars
// for a single AOT bundle (one ticket's blobs) from the per-blob cells and proofs.
//
// cellsPerBlob and proofsPerBlob are parallel slices indexed by blob; each holds that
// blob's NUMBER_OF_COLUMNS extended cells and matching cell KZG proofs. commitments holds
// the blob KZG commitments in the same blob order. The rows (blobs) are rotated into
// columns so that the sidecar at index i carries cell/proof i of every blob, and every
// sidecar shares the full commitment list and ticket fields (ticket_id, target_slot,
// blob_info_signature). This is the AOT counterpart to DataColumnSidecarsGloas; the merged,
// block-bound DataColumnSidecar is produced later by das.MergeDataColumnSidecars.
func AotDataColumnSidecars(
	cellsPerBlob [][]kzg.Cell,
	proofsPerBlob [][]kzg.Proof,
	commitments [][]byte,
	ticketID uint64,
	targetSlot primitives.Slot,
	blobInfoSignature []byte,
) ([]*ethpb.AOTDataColumnSidecar, error) {
	const numberOfColumns = uint64(fieldparams.NumberOfColumns)

	if len(cellsPerBlob) == 0 {
		return nil, nil
	}
	if len(cellsPerBlob) != len(commitments) {
		return nil, errors.Errorf("blobs (%d) and commitments (%d) length mismatch", len(cellsPerBlob), len(commitments))
	}

	cells, proofs, err := rotateRowsToCols(cellsPerBlob, proofsPerBlob, numberOfColumns)
	if err != nil {
		return nil, errors.Wrap(err, "rotate cells and proofs")
	}

	// Copy the commitments and signature once; every sidecar shares the same values, but
	// each gets its own copy so callers can mutate or release the inputs safely.
	sharedCommitments := bytesutil.SafeCopy2dBytes(commitments)
	sharedSignature := bytesutil.SafeCopyBytes(blobInfoSignature)

	sidecars := make([]*ethpb.AOTDataColumnSidecar, 0, numberOfColumns)
	for idx := range numberOfColumns {
		sidecar := &ethpb.AOTDataColumnSidecar{
			Index:             idx,
			Column:            cells[idx],
			KzgCommitments:    sharedCommitments,
			KzgProofs:         proofs[idx],
			TicketId:          ticketID,
			TargetSlot:        targetSlot,
			BlobInfoSignature: sharedSignature,
		}
		if len(sidecar.Column) != len(sidecar.KzgCommitments) || len(sidecar.Column) != len(sidecar.KzgProofs) {
			return nil, ErrSizeMismatch
		}
		sidecars = append(sidecars, sidecar)
	}
	return sidecars, nil
}
