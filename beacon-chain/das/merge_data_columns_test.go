package das

import (
	"testing"

	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func cell(b byte) []byte  { return bytesutil.PadTo([]byte{b}, 2048) }
func proof(b byte) []byte { return bytesutil.PadTo([]byte{b}, 48) }

func jitColumn(index uint64, cells, proofs [][]byte) *ethpb.DataColumnSidecarGloas {
	return &ethpb.DataColumnSidecarGloas{
		Index:           index,
		Column:          cells,
		KzgProofs:       proofs,
		Slot:            42,
		BeaconBlockRoot: bytesutil.PadTo([]byte{0xbb}, 32),
	}
}

func aotColumn(index uint64, cells, proofs [][]byte) *ethpb.AOTDataColumnSidecar {
	return &ethpb.AOTDataColumnSidecar{
		Index:             index,
		Column:            cells,
		KzgCommitments:    [][]byte{proof(0x99)}, // dropped by merge
		KzgProofs:         proofs,
		TicketId:          7,
		BlobInfoSignature: bytesutil.PadTo([]byte{0x11}, 96),
	}
}

func TestMergeDataColumnSidecars_OrderAndFields(t *testing.T) {
	jit := jitColumn(3, [][]byte{cell(1), cell(2)}, [][]byte{proof(1), proof(2)})
	aotA := aotColumn(3, [][]byte{cell(3)}, [][]byte{proof(3)})
	aotB := aotColumn(3, [][]byte{cell(4), cell(5)}, [][]byte{proof(4), proof(5)})

	merged, err := MergeDataColumnSidecars(jit, []*ethpb.AOTDataColumnSidecar{aotA, aotB})
	require.NoError(t, err)

	// JIT cells first, then AOT cells in slice order.
	wantCells := [][]byte{cell(1), cell(2), cell(3), cell(4), cell(5)}
	wantProofs := [][]byte{proof(1), proof(2), proof(3), proof(4), proof(5)}
	require.DeepEqual(t, wantCells, merged.GetColumn())
	require.DeepEqual(t, wantProofs, merged.GetKzgProofs())

	// Inherits JIT index/slot/root; no ticket fields on the merged type.
	require.Equal(t, uint64(3), merged.GetIndex())
	require.Equal(t, jit.GetSlot(), merged.GetSlot())
	require.DeepEqual(t, jit.GetBeaconBlockRoot(), merged.GetBeaconBlockRoot())
}

func TestMergeDataColumnSidecars_NoAot(t *testing.T) {
	jit := jitColumn(0, [][]byte{cell(1)}, [][]byte{proof(1)})

	merged, err := MergeDataColumnSidecars(jit, nil)
	require.NoError(t, err)
	require.DeepEqual(t, [][]byte{cell(1)}, merged.GetColumn())
	require.DeepEqual(t, [][]byte{proof(1)}, merged.GetKzgProofs())
}

func TestMergeDataColumnSidecars_IndexMismatch(t *testing.T) {
	jit := jitColumn(3, [][]byte{cell(1)}, [][]byte{proof(1)})
	aot := aotColumn(4, [][]byte{cell(2)}, [][]byte{proof(2)})

	_, err := MergeDataColumnSidecars(jit, []*ethpb.AOTDataColumnSidecar{aot})
	require.ErrorIs(t, err, errAotColumnIndexMismatch)
}

func TestMergeDataColumnSidecars_NilInputs(t *testing.T) {
	_, err := MergeDataColumnSidecars(nil, nil)
	require.ErrorIs(t, err, errNilJitColumn)

	jit := jitColumn(0, [][]byte{cell(1)}, [][]byte{proof(1)})
	_, err = MergeDataColumnSidecars(jit, []*ethpb.AOTDataColumnSidecar{nil})
	require.ErrorIs(t, err, errNilAotColumn)
}

func TestMergeDataColumnSidecars_DoesNotAliasInputs(t *testing.T) {
	jitCells := [][]byte{cell(1)}
	jit := jitColumn(0, jitCells, [][]byte{proof(1)})
	aot := aotColumn(0, [][]byte{cell(2)}, [][]byte{proof(2)})

	merged, err := MergeDataColumnSidecars(jit, []*ethpb.AOTDataColumnSidecar{aot})
	require.NoError(t, err)

	// Mutating a merged cell must not affect the JIT input (deep copy).
	merged.Column[0][0] = 0xff
	require.Equal(t, byte(1), jit.GetColumn()[0][0])
}
