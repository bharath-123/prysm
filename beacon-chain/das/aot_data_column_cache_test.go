package das

import (
	"testing"

	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/encoding/ssz"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func aotTestCommitment(b byte) []byte {
	return bytesutil.PadTo([]byte{b}, 48)
}

func aotTestSidecar(index uint64, targetSlot primitives.Slot, commitments [][]byte) *ethpb.AOTDataColumnSidecar {
	return &ethpb.AOTDataColumnSidecar{
		Index:          index,
		KzgCommitments: commitments,
		TargetSlot:     targetSlot,
	}
}

func TestAotDataColumnCache_StashAndGet(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(1), aotTestCommitment(2)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commits)))
	require.NoError(t, c.Stash(aotTestSidecar(7, 10, commits)))

	got, err := c.get(key, []uint64{0, 7})
	require.NoError(t, err)
	require.Equal(t, 2, len(got))

	// A missing index fails cleanly with no partial result.
	_, err = c.get(key, []uint64{0, 1})
	require.ErrorIs(t, err, errAotMissingColumn)

	// An unknown bundle key fails.
	_, err = c.get([32]byte{0xff}, []uint64{0})
	require.ErrorIs(t, err, errAotMissingColumn)
}

func TestAotDataColumnCache_StashIndexTooHigh(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(1)}
	err := c.Stash(aotTestSidecar(fieldparams.NumberOfColumns, 10, commits))
	require.ErrorIs(t, err, errAotColumnIndexTooHigh)
}

func TestAotDataColumnCache_StoredIndices(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(3)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commits)))
	require.NoError(t, c.Stash(aotTestSidecar(3, 10, commits)))
	require.NoError(t, c.Stash(aotTestSidecar(5, 10, commits)))

	stored := c.storedIndices(key)
	require.DeepEqual(t, map[uint64]bool{0: true, 3: true, 5: true}, stored)

	// Unknown bundle returns nil.
	require.DeepEqual(t, map[uint64]bool(nil), c.storedIndices([32]byte{0xab}))
}

func TestAotDataColumnCache_GroupsByCommitmentsRoot(t *testing.T) {
	c := NewAotDataColumnCache()
	commitsA := [][]byte{aotTestCommitment(1), aotTestCommitment(2)}
	commitsB := [][]byte{aotTestCommitment(9)}
	keyA, err := ssz.KzgCommitmentsRoot(commitsA)
	require.NoError(t, err)
	keyB, err := ssz.KzgCommitmentsRoot(commitsB)
	require.NoError(t, err)
	require.Equal(t, false, keyA == keyB)

	// Same column index, different bundles, must not collide.
	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commitsA)))
	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commitsB)))

	require.DeepEqual(t, map[uint64]bool{0: true}, c.storedIndices(keyA))
	require.DeepEqual(t, map[uint64]bool{0: true}, c.storedIndices(keyB))
}

func TestAotDataColumnCache_StashOverwrite(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(4)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	first := aotTestSidecar(0, 10, commits)
	second := aotTestSidecar(0, 10, commits)
	second.BlobInfoSignature = bytesutil.PadTo([]byte{0xee}, 96)

	require.NoError(t, c.Stash(first))
	require.NoError(t, c.Stash(second))

	got, err := c.get(key, []uint64{0})
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	require.DeepEqual(t, second.BlobInfoSignature, got[0].GetBlobInfoSignature())
}

func TestAotDataColumnCache_Evict(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(6)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commits)))
	c.evict(key)
	require.DeepEqual(t, map[uint64]bool(nil), c.storedIndices(key))

	// Evicting an unknown key is a no-op.
	c.evict([32]byte{0x01})
}

func TestAotDataColumnCache_Prune(t *testing.T) {
	commits := [][]byte{aotTestCommitment(7)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	// targetSlot=10, retention=2 → kept while currentSlot <= 12, dropped at 13.
	c := NewAotDataColumnCache()
	require.NoError(t, c.Stash(aotTestSidecar(0, 10, commits)))
	c.prune(12)
	require.DeepEqual(t, map[uint64]bool{0: true}, c.storedIndices(key))

	c.prune(13)
	require.DeepEqual(t, map[uint64]bool(nil), c.storedIndices(key))
}

func TestAotDataColumnCache_SubscribeNotifiesOnStash(t *testing.T) {
	c := NewAotDataColumnCache()
	commits := [][]byte{aotTestCommitment(8)}
	key, err := ssz.KzgCommitmentsRoot(commits)
	require.NoError(t, err)

	sub, ch := c.Subscribe()
	defer sub.Unsubscribe()

	require.NoError(t, c.Stash(aotTestSidecar(2, 10, commits)))

	ident := <-ch
	require.Equal(t, key, ident.CommitmentsRoot)
	require.DeepEqual(t, []uint64{2}, ident.Indices)
}
