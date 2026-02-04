package builder

import (
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls"
	fssz "github.com/prysmaticlabs/fastssz"
)

// XRequestAuthHeader is the HTTP header name for the GetHeader request auth signature.
const XRequestAuthHeader = "X-Request-Auth"

// GetHeaderAuthData is the SSZ container signed for X-Request-Auth.
// It contains slot, parent_hash, and pubkey. The signature is over HashTreeRoot of this container only (no domain).
type GetHeaderAuthData struct {
	Slot       primitives.Slot
	ParentHash [32]byte
	Pubkey     [48]byte
}

// HashTreeRoot returns the SSZ hash tree root of the container.
func (g *GetHeaderAuthData) HashTreeRoot() ([32]byte, error) {
	return fssz.HashWithDefaultHasher(g)
}

// HashTreeRootWith appends the container's fields to the hasher for SSZ merkleization.
func (g *GetHeaderAuthData) HashTreeRootWith(hh *fssz.Hasher) error {
	indx := hh.Index()
	hh.PutUint64(uint64(g.Slot))
	hh.PutBytes(g.ParentHash[:])
	hh.PutBytes(g.Pubkey[:])
	hh.Merkleize(indx)
	return nil
}

// SignGetHeaderAuth signs the HashTreeRoot of (slot, parentHash, pubkey) with the given BLS secret key.
// The signature is the raw 96-byte BLS signature (no domain). Returns nil if key is nil.
func SignGetHeaderAuth(key bls.SecretKey, slot primitives.Slot, parentHash [32]byte, pubkey [48]byte) []byte {
	if key == nil {
		return nil
	}
	data := &GetHeaderAuthData{Slot: slot, ParentHash: parentHash, Pubkey: pubkey}
	root, err := data.HashTreeRoot()
	if err != nil {
		return nil
	}
	sig := key.Sign(root[:])
	if sig == nil {
		return nil
	}
	return sig.Marshal()
}
