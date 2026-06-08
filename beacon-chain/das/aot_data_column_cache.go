package das

import (
	"sync"

	"github.com/OffchainLabs/prysm/v7/async/event"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/ssz"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
)

var (
	errAotColumnIndexTooHigh = errors.New("AOT data column index too high")
	errAotMissingColumn      = errors.New("no AOT data column in cache for bundle root and index")
)

// aotBundleRetentionSlots is how many slots past a bundle's target slot we keep it
// before pruning. AOT data columns propagate at most AOT_PROPAGATION_WINDOW_SLOTS
// ahead of their target slot, so a small grace beyond the target slot is enough.
const aotBundleRetentionSlots = primitives.Slot(2)

// aotBundleKey is the hash tree root of an AOT bundle's KZG commitment list. It is
// equal to the HTR of the corresponding entry in bid.aot_blob_kzg_commitments (the
// per-ticket commitment list), so a block's bid addresses staged AOT columns at
// data-availability time by hashing each of its AOT commitment lists.
type aotBundleKey = [32]byte

// AotDataColumnsIdent identifies the AOT data columns staged for a single bundle. It
// is published on the cache feed each time a column is stashed, so subscribers (e.g.
// the DA check) can wait until the indices they need have been seen.
type AotDataColumnsIdent struct {
	// CommitmentsRoot is the hash tree root of the bundle's KZG commitment list (the
	// cache key, equal to the HTR of an entry in bid.aot_blob_kzg_commitments).
	CommitmentsRoot [32]byte
	// Indices are all AOT column indices seen so far for this bundle.
	Indices []uint64
}

// AotDataColumnCache stages AOTDataColumnSidecars received via gossip ahead of
// block time. Columns are grouped into bundles keyed by aotBundleKey (the HTR of
// the bundle's KZG commitment list). It is a transient, pre-block holding area:
// once the block/envelope arrives the relevant columns are merged into the standard
// block-root data column store and the bundle is evicted. (Merging and validation
// are implemented separately.)
//
// ticket_id is deliberately NOT used as a key: it is only a gossip-propagation
// construct and is not observable on-chain. The bid references AOT bundles by their
// commitment lists (bid.aot_blob_kzg_commitments); the key is the HTR of each list.
//
// TODO - The AotDataColumnCache must be persisted in disk to survive node restarts since
// the DA check for AOT data columns is important for attesters. 
type AotDataColumnCache struct {
	mu      sync.RWMutex
	bundles map[aotBundleKey]*aotBundle
	feed    *event.Feed
}

// aotBundle holds the staged columns for a single AOT bundle (one ticket's blob
// set). All columns in a bundle share the same KZG commitment list and target slot.
type aotBundle struct {
	targetSlot  primitives.Slot
	commitments [][]byte
	columns     map[uint64]*ethpb.AOTDataColumnSidecar
}

func NewAotDataColumnCache() *AotDataColumnCache {
	return &AotDataColumnCache{
		bundles: make(map[aotBundleKey]*aotBundle),
		feed:    new(event.Feed),
	}
}

// aotBundleKeyFor computes the cache key for a sidecar: the hash tree root of its
// KZG commitment list.
//
// NOTE: ssz.KzgCommitmentsRoot merkleizes with limit MAX_BLOB_COMMITMENTS_PER_BLOCK.
// The bid's inner AOT commitment lists are merkleized with the JIT limit
// (MAX_JIT_BLOB_COMMITMENTS_PER_BLOCK); these are equal under the current placeholder
// (4096), so the key matches htr(bid.aot_blob_kzg_commitments[i]). If the AOT inner
// limit ever diverges from MAX_BLOB_COMMITMENTS_PER_BLOCK, replace this with a
// merkleization using the matching limit.
func aotBundleKeyFor(sidecar *ethpb.AOTDataColumnSidecar) (aotBundleKey, error) {
	return ssz.KzgCommitmentsRoot(sidecar.GetKzgCommitments())
}

// stash adds an AOT data column sidecar to the cache, grouped by its bundle key, and
// notifies subscribers with the indices seen so far for that bundle. Re-stashing the
// same (bundle, index) overwrites the previous entry.
func (c *AotDataColumnCache) Stash(sidecar *ethpb.AOTDataColumnSidecar) error {
	index := sidecar.GetIndex()
	if index >= fieldparams.NumberOfColumns {
		return errors.Wrapf(errAotColumnIndexTooHigh, "index=%d", index)
	}
	key, err := aotBundleKeyFor(sidecar)
	if err != nil {
		return errors.Wrap(err, "compute AOT bundle key")
	}

	c.mu.Lock()
	bundle, ok := c.bundles[key]
	if !ok {
		bundle = &aotBundle{
			targetSlot:  sidecar.GetTargetSlot(),
			commitments: sidecar.GetKzgCommitments(),
			columns:     make(map[uint64]*ethpb.AOTDataColumnSidecar),
		}
		c.bundles[key] = bundle
	}
	bundle.columns[index] = sidecar

	// Snapshot the indices seen so far while holding the lock so we can notify
	// subscribers outside of it (feed.Send may block on slow subscribers).
	indices := make([]uint64, 0, len(bundle.columns))
	for idx := range bundle.columns {
		indices = append(indices, idx)
	}
	c.mu.Unlock()

	c.feed.Send(AotDataColumnsIdent{
		CommitmentsRoot: key,
		Indices:         indices,
	})
	return nil
}

// Subscribe subscribes to the AOT data column feed. It returns the subscription and
// a 1-buffer channel that receives an AotDataColumnsIdent every time a column is
// stashed (carrying the bundle's commitment-list root and all indices seen so far).
// The DA check uses this to wait for the AOT columns it needs.
//
// The caller must call subscription.Unsubscribe when done and drain the channel
// promptly: a stash buffers a value, and if the buffer is full the stash (and other
// subscribers) block until it can be delivered.
func (c *AotDataColumnCache) Subscribe() (event.Subscription, <-chan AotDataColumnsIdent) {
	identsChan := make(chan AotDataColumnsIdent, 1)
	subscription := c.feed.Subscribe(identsChan)
	return subscription, identsChan
}

// get returns the staged AOT columns for the given bundle key and indices. If the
// bundle is unknown or any requested index is missing, it returns an error and no
// columns (the returned slice is left untouched so callers can ignore it on error).
func (c *AotDataColumnCache) get(key aotBundleKey, indices []uint64) ([]*ethpb.AOTDataColumnSidecar, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	bundle, ok := c.bundles[key]
	if !ok {
		return nil, errors.Wrapf(errAotMissingColumn, "root=%#x (bundle not found)", key)
	}
	// Check everything is present before allocating so a missing index is a clean failure.
	for _, index := range indices {
		if _, ok := bundle.columns[index]; !ok {
			return nil, errors.Wrapf(errAotMissingColumn, "root=%#x, index=%d", key, index)
		}
	}
	out := make([]*ethpb.AOTDataColumnSidecar, 0, len(indices))
	for _, index := range indices {
		out = append(out, bundle.columns[index])
	}
	return out, nil
}

// storedIndices returns the set of column indices currently staged for a bundle, or
// nil if the bundle is unknown.
func (c *AotDataColumnCache) storedIndices(key aotBundleKey) map[uint64]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	bundle, ok := c.bundles[key]
	if !ok {
		return nil
	}
	out := make(map[uint64]bool, len(bundle.columns))
	for index := range bundle.columns {
		out[index] = true
	}
	return out
}

// evict removes a bundle from the cache, e.g. after its columns have been merged
// into the block-root store.
func (c *AotDataColumnCache) evict(key aotBundleKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.bundles, key)
}

// prune drops bundles whose target slot is more than aotBundleRetentionSlots in the
// past relative to currentSlot.
func (c *AotDataColumnCache) prune(currentSlot primitives.Slot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, bundle := range c.bundles {
		if bundle.targetSlot+aotBundleRetentionSlots < currentSlot {
			delete(c.bundles, key)
		}
	}
}
