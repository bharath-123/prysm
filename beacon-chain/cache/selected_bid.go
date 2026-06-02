package cache

import (
	"sync"

	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
)

// BidType describes the execution payload bid selected for a block, along with
// enough provenance to act on it after the block is proposed. It lives in the
// cache package (rather than the validator RPC package) so that
// SelectedBidCache can store it without an import cycle.
type BidType struct {
	// ExecutionPayloadBid is the selected signed execution payload bid.
	ExecutionPayloadBid *ethpb.SignedExecutionPayloadBid
	// IsBuilderApiBid is true when the bid came from a builder via the Builder API.
	IsBuilderApiBid bool
	// BuilderUrl is the URL of the builder that served the bid; empty unless
	// IsBuilderApiBid is true.
	BuilderUrl string
	// SelfBuild is true when the proposer is building the payload itself. The
	// caller uses this to decide whether to store the execution payload envelope.
	SelfBuild bool
}

// SelectedBidCache maps a block root to the BidType selected for that block, so
// the proposer can act on the selected bid's provenance after the block is
// proposed (e.g. submit the block back to the builder that served a Builder-API
// bid).
type SelectedBidCache struct {
	mu      sync.RWMutex
	entries map[[32]byte]*BidType
}

// NewSelectedBidCache initializes a selected-bid cache.
func NewSelectedBidCache() *SelectedBidCache {
	return &SelectedBidCache{entries: make(map[[32]byte]*BidType)}
}

// Set stores the selected bid for the given block root.
func (c *SelectedBidCache) Set(blockRoot [32]byte, bid *BidType) {
	if c == nil || bid == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[blockRoot] = bid
}

// Get returns the selected bid for the given block root.
func (c *SelectedBidCache) Get(blockRoot [32]byte) (*BidType, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	bid, ok := c.entries[blockRoot]
	return bid, ok
}

// Delete removes the selected bid for the given block root.
func (c *SelectedBidCache) Delete(blockRoot [32]byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, blockRoot)
}
