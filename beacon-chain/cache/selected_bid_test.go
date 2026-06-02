package cache

import (
	"testing"

	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func TestSelectedBidCache_SetGetDelete(t *testing.T) {
	c := NewSelectedBidCache()
	root := [32]byte{1, 2, 3}

	// Missing entry.
	_, ok := c.Get(root)
	require.Equal(t, false, ok)

	bid := &BidType{
		ExecutionPayloadBid: &ethpb.SignedExecutionPayloadBid{Message: &ethpb.ExecutionPayloadBid{Value: 42}},
		IsBuilderApiBid:     true,
		BuilderUrl:          "http://builder-a:18550",
	}
	c.Set(root, bid)

	got, ok := c.Get(root)
	require.Equal(t, true, ok)
	require.Equal(t, true, got.IsBuilderApiBid)
	require.Equal(t, "http://builder-a:18550", got.BuilderUrl)
	require.Equal(t, primitives.Gwei(42), got.ExecutionPayloadBid.Message.Value)

	c.Delete(root)
	_, ok = c.Get(root)
	require.Equal(t, false, ok)
}

func TestSelectedBidCache_NilSafe(t *testing.T) {
	var c *SelectedBidCache
	c.Set([32]byte{}, &BidType{})
	_, ok := c.Get([32]byte{})
	require.Equal(t, false, ok)
	c.Delete([32]byte{})
}
