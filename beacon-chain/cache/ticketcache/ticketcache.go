// Package ticketcache holds the set of active blob-streaming tickets returned
// by engine_forkchoiceUpdatedV5 (Heze EIP-Blob-Streaming).
//
// The execution layer is the source of truth: its TicketStore mints one ticket
// per chain head and evicts after TICKET_LOOKAHEAD blocks. On every successful
// FCUv5, the consensus client replaces this cache with the EL's current ticket
// set so AOT data column sidecar gossip validation can look up the ticket's
// registered BLS pubkey, target slot, and blob count.
package ticketcache

import (
	"sync"
	"time"

	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/time/slots"
)

// PubkeyLength is the BLS pubkey size carried in a ticket.
const PubkeyLength = 48

// Ticket is the cached view of one active blob-streaming ticket.
//
// TargetSlot is pre-computed from the ticket's selling-block timestamp: it is
// the slot immediately following the slot whose start contains that timestamp.
// This matches the POC behaviour of the EL ticketstore, which mints a ticket
// at chain head H and expects the corresponding AOT data columns to land in
// the next slot's block.
type Ticket struct {
	ID         uint64
	TargetSlot primitives.Slot
	Owner      [20]byte
	BLSPubkey  [PubkeyLength]byte
	BlobCount  uint64
}

// Cache stores the active ticket set in memory. It is replace-only: each
// successful FCUv5 response overwrites the prior set atomically. Reads (used
// by AOT gossip validation) take a read lock.
type Cache struct {
	mu      sync.RWMutex
	genesis time.Time
	byID    map[uint64]Ticket
}

// New returns a Cache. The genesis time is used to derive TargetSlot from the
// EL's selling-block timestamp; passing the zero value disables that
// derivation (TargetSlot will be left at 0).
func New(genesis time.Time) *Cache {
	return &Cache{
		genesis: genesis,
		byID:    make(map[uint64]Ticket),
	}
}

// SetGenesis assigns the genesis time used for TargetSlot derivation. It is
// safe to call after construction (e.g., once genesis is known at startup).
func (c *Cache) SetGenesis(genesis time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.genesis = genesis
}

// Replace overwrites the active set with the provided tickets. Each ticket's
// TargetSlot is computed from its SellingTimestamp; pass tickets exactly as
// returned by the EL.
func (c *Cache) Replace(tickets []Replacement) {
	next := make(map[uint64]Ticket, len(tickets))
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range tickets {
		next[t.ID] = Ticket{
			ID:         t.ID,
			TargetSlot: c.targetSlotLocked(t.SellingTimestamp),
			Owner:      t.Owner,
			BLSPubkey:  t.BLSPubkey,
			BlobCount:  t.BlobCount,
		}
	}
	c.byID = next
}

// Replacement is the wire-shape the cache accepts from the engine client.
// It mirrors engine.TicketInfoV1 after JSON decoding.
type Replacement struct {
	ID               uint64
	SellingTimestamp uint64
	Owner            [20]byte
	BLSPubkey        [PubkeyLength]byte
	BlobCount        uint64
}

// ByID returns the ticket with the given id and a boolean indicating presence.
func (c *Cache) ByID(id uint64) (Ticket, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.byID[id]
	return t, ok
}

// All returns a snapshot of every active ticket. The order is unspecified.
func (c *Cache) All() []Ticket {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Ticket, 0, len(c.byID))
	for _, t := range c.byID {
		out = append(out, t)
	}
	return out
}

// Len returns the number of active tickets.
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.byID)
}

// targetSlotLocked converts a selling-block timestamp to the slot at which the
// ticket's AOT blobs are expected to land. The caller must hold c.mu.
//
// Derivation: target = slot_of(sellingTimestamp) + 1. The POC EL ticketstore
// mints a ticket per chain head; at slot S+1's propagation window
// [S+1, S+1+AOT_PROPAGATION_WINDOW_SLOTS] the freshly-minted ticket from slot
// S is in range.
func (c *Cache) targetSlotLocked(sellingTimestamp uint64) primitives.Slot {
	if c.genesis.IsZero() {
		return 0
	}
	at := slots.At(c.genesis, time.Unix(int64(sellingTimestamp), 0))
	return at + 1
}
