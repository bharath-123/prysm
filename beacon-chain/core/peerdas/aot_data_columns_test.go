package peerdas

import (
	"testing"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/kzg"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

// TestAotDataColumnSidecars_TransposeAndFields verifies that the per-blob cells and
// proofs are rotated into per-column sidecars (one cell/proof per blob at each column
// index) and that every sidecar carries the shared commitment list and ticket fields.
func TestAotDataColumnSidecars_TransposeAndFields(t *testing.T) {
	const numBlobs = 2
	const numCols = fieldparams.NumberOfColumns

	// cellsPerBlob[blob][col] is marked with {blob, col} so the transpose is checkable.
	cellsPerBlob := make([][]kzg.Cell, numBlobs)
	proofsPerBlob := make([][]kzg.Proof, numBlobs)
	for blob := range numBlobs {
		cells := make([]kzg.Cell, numCols)
		proofs := make([]kzg.Proof, numCols)
		for col := range numCols {
			cells[col][0], cells[col][1] = byte(blob), byte(col)
			proofs[col][0], proofs[col][1] = byte(blob), byte(col)
		}
		cellsPerBlob[blob] = cells
		proofsPerBlob[blob] = proofs
	}

	commitments := [][]byte{make([]byte, 48), make([]byte, 48)}
	commitments[0][0], commitments[1][0] = 0xaa, 0xbb
	signature := make([]byte, 96)
	signature[0] = 0xcc

	sidecars, err := AotDataColumnSidecars(cellsPerBlob, proofsPerBlob, commitments, 7, primitives.Slot(99), signature)
	require.NoError(t, err)
	require.Equal(t, numCols, len(sidecars))

	for col, sidecar := range sidecars {
		require.Equal(t, uint64(col), sidecar.GetIndex())
		require.Equal(t, numBlobs, len(sidecar.GetColumn()))
		require.Equal(t, numBlobs, len(sidecar.GetKzgProofs()))
		for blob := range numBlobs {
			require.Equal(t, byte(blob), sidecar.GetColumn()[blob][0])
			require.Equal(t, byte(col), sidecar.GetColumn()[blob][1])
			require.Equal(t, byte(blob), sidecar.GetKzgProofs()[blob][0])
			require.Equal(t, byte(col), sidecar.GetKzgProofs()[blob][1])
		}
		require.DeepEqual(t, commitments, sidecar.GetKzgCommitments())
		require.Equal(t, uint64(7), sidecar.GetTicketId())
		require.Equal(t, primitives.Slot(99), sidecar.GetTargetSlot())
		require.DeepEqual(t, signature, sidecar.GetBlobInfoSignature())
	}
}

func TestAotDataColumnSidecars_Empty(t *testing.T) {
	sidecars, err := AotDataColumnSidecars(nil, nil, nil, 0, 0, nil)
	require.NoError(t, err)
	require.Equal(t, 0, len(sidecars))
}

func TestAotDataColumnSidecars_CommitmentCountMismatch(t *testing.T) {
	cells := [][]kzg.Cell{make([]kzg.Cell, fieldparams.NumberOfColumns)}
	proofs := [][]kzg.Proof{make([]kzg.Proof, fieldparams.NumberOfColumns)}
	// One blob but two commitments.
	_, err := AotDataColumnSidecars(cells, proofs, [][]byte{make([]byte, 48), make([]byte, 48)}, 0, 0, nil)
	require.ErrorContains(t, "length mismatch", err)
}
