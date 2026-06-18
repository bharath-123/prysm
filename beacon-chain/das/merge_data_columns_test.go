package das

import (
	"testing"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/kzg"
	"github.com/OffchainLabs/prysm/v7/crypto/random"
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

func randBlob(seed int64) kzg.Blob {
	r := random.GetRandBlob(seed)
	var b kzg.Blob
	copy(b[:], r[:])
	return b
}

// TestMergeDataColumnSidecars_VerifiesKzgProofs builds a JIT column and two AOT columns
// from real blobs at the same column index, merges them, and checks that the merged
// cells verify against their commitments. This proves the merge keeps cell<->proof
// alignment (JIT blob first, then AOT blobs in order): if the merge reordered or
// mismatched cells and proofs, the batch verification would fail.
func TestMergeDataColumnSidecars_VerifiesKzgProofs(t *testing.T) {
	require.NoError(t, kzg.Start())

	// Column index shared by every sidecar; for data columns the cell index equals
	// the column (sidecar) index.
	const colIdx = uint64(5)

	// Three blobs: one JIT, two AOT. The merged column at colIdx holds one cell per
	// blob, with the proof for that blob's cell alongside it.
	blobs := []kzg.Blob{randBlob(1), randBlob(2), randBlob(3)}
	cellBytes := make([][]byte, len(blobs))
	proofBytes := make([][]byte, len(blobs))
	commitments := make([]kzg.Bytes48, len(blobs))
	for i := range blobs {
		cells, proofs, err := kzg.ComputeCellsAndKZGProofs(&blobs[i])
		require.NoError(t, err)
		commitment, err := kzg.BlobToKZGCommitment(&blobs[i])
		require.NoError(t, err)
		cellBytes[i] = bytesutil.SafeCopyBytes(cells[colIdx][:])
		proofBytes[i] = bytesutil.SafeCopyBytes(proofs[colIdx][:])
		copy(commitments[i][:], commitment[:])
	}

	jit := jitColumn(colIdx, [][]byte{cellBytes[0]}, [][]byte{proofBytes[0]})
	aotA := aotColumn(colIdx, [][]byte{cellBytes[1]}, [][]byte{proofBytes[1]})
	aotB := aotColumn(colIdx, [][]byte{cellBytes[2]}, [][]byte{proofBytes[2]})

	merged, err := MergeDataColumnSidecars(jit, []*ethpb.AOTDataColumnSidecar{aotA, aotB})
	require.NoError(t, err)
	require.Equal(t, len(blobs), len(merged.GetColumn()))
	require.Equal(t, len(blobs), len(merged.GetKzgProofs()))

	// Verify the merged column's cells against the per-blob commitments, in merged order.
	mergedCells := make([]kzg.Cell, len(merged.GetColumn()))
	for i, c := range merged.GetColumn() {
		copy(mergedCells[i][:], c)
	}
	mergedProofs := make([]kzg.Bytes48, len(merged.GetKzgProofs()))
	for i, p := range merged.GetKzgProofs() {
		copy(mergedProofs[i][:], p)
	}
	cellIndices := []uint64{colIdx, colIdx, colIdx}

	valid, err := kzg.VerifyCellKZGProofBatch(commitments, cellIndices, mergedCells, mergedProofs)
	require.NoError(t, err)
	require.Equal(t, true, valid)
}
