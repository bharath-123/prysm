package builder

import (
	"testing"

	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

// TestSignGetHeaderAuth_Verify tests that SignGetHeaderAuth produces a signature that verifies
// against the HashTreeRoot of GetHeaderAuthData(slot, parentHash, pubkey).
func TestSignGetHeaderAuth_Verify(t *testing.T) {
	key, err := bls.RandKey()
	require.NoError(t, err)
	pubkey := bytesutil.ToBytes48(key.PublicKey().Marshal())

	slot := primitives.Slot(100)
	parentHash := [32]byte{}
	parentHash[0] = 0xde
	parentHash[31] = 0xad

	sigBytes := SignGetHeaderAuth(key, slot, parentHash, pubkey)
	require.NotNil(t, sigBytes)
	require.Equal(t, 96, len(sigBytes))

	data := &GetHeaderAuthData{Slot: slot, ParentHash: parentHash, Pubkey: pubkey}
	root, err := data.HashTreeRoot()
	require.NoError(t, err)

	pk := key.PublicKey()
	valid, err := bls.VerifySignature(sigBytes, root, pk)
	require.NoError(t, err)
	require.Equal(t, true, valid)
}

// TestGetHeaderAuthData_HashTreeRoot ensures the SSZ root is deterministic.
func TestGetHeaderAuthData_HashTreeRoot(t *testing.T) {
	data := &GetHeaderAuthData{
		Slot:       1,
		ParentHash: [32]byte{1, 2, 3},
		Pubkey:     [48]byte{4, 5, 6},
	}
	root1, err := data.HashTreeRoot()
	require.NoError(t, err)
	root2, err := data.HashTreeRoot()
	require.NoError(t, err)
	require.DeepEqual(t, root1, root2)
}
