package das

import (
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
)

var (
	errNilJitColumn           = errors.New("nil JIT data column sidecar")
	errNilAotColumn           = errors.New("nil AOT data column sidecar")
	errAotColumnIndexMismatch = errors.New("AOT data column index does not match JIT column index")
)

// MergeDataColumnSidecars merges a JIT DataColumnSidecarGloas with the AOT data column
// sidecars for the SAME column index into a single standard DataColumnSidecarGloas (the
// merged, block-bound form stored and served by the node). [Blob Streaming, das-core
// merge_data_column_sidecars]
//
// The merged column places the JIT cells first, then each AOT sidecar's cells in the order
// the aots slice is given. The caller MUST order aots to match the block bid's
// aot_blob_kzg_commitments so the merged cells align with
// bid.jit_blob_kzg_commitments + bid.aot_blob_kzg_commitments (flattened). kzg_proofs are
// concatenated in the same order.
//
// The result inherits the JIT column's index, slot, and beacon block root; the AOT ticket
// fields (ticket_id, target_slot, blob_info_signature) and per-bundle kzg_commitments are
// dropped. All inputs must share the JIT column's index.
func MergeDataColumnSidecars(jit *ethpb.DataColumnSidecarGloas, aots []*ethpb.AOTDataColumnSidecar) (*ethpb.DataColumnSidecarGloas, error) {
	if jit == nil {
		return nil, errNilJitColumn
	}
	for _, aot := range aots {
		if aot == nil {
			return nil, errNilAotColumn
		}
		if aot.GetIndex() != jit.GetIndex() {
			return nil, errors.Wrapf(errAotColumnIndexMismatch, "jit=%d aot=%d", jit.GetIndex(), aot.GetIndex())
		}
	}

	// JIT cells/proofs first, then AOT cells/proofs in the given (bid) order. Inputs are
	// deep-copied so the merged sidecar does not alias gossip-received buffers.
	column := bytesutil.SafeCopy2dBytes(jit.GetColumn())
	proofs := bytesutil.SafeCopy2dBytes(jit.GetKzgProofs())
	for _, aot := range aots {
		column = append(column, bytesutil.SafeCopy2dBytes(aot.GetColumn())...)
		proofs = append(proofs, bytesutil.SafeCopy2dBytes(aot.GetKzgProofs())...)
	}

	return &ethpb.DataColumnSidecarGloas{
		Index:           jit.GetIndex(),
		Column:          column,
		KzgProofs:       proofs,
		Slot:            jit.GetSlot(),
		BeaconBlockRoot: bytesutil.SafeCopyBytes(jit.GetBeaconBlockRoot()),
	}, nil
}
