package ticketcache

import (
	"testing"
	"time"

	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func TestCache_ReplaceAndLookup(t *testing.T) {
	genesis := time.Unix(1_700_000_000, 0)
	c := New(genesis)

	pubA := [PubkeyLength]byte{0x01, 0x02}
	pubB := [PubkeyLength]byte{0x0a, 0x0b}
	ownerA := [20]byte{0xaa}
	ownerB := [20]byte{0xbb}

	secondsPerSlot := uint64(params.BeaconConfig().SecondsPerSlot)
	sellingSlot5 := genesis.Unix() + int64(secondsPerSlot*5)
	sellingSlot9 := genesis.Unix() + int64(secondsPerSlot*9)

	c.Replace([]Replacement{
		{ID: 7, SellingTimestamp: uint64(sellingSlot5), Owner: ownerA, BLSPubkey: pubA, BlobCount: 2},
		{ID: 9, SellingTimestamp: uint64(sellingSlot9), Owner: ownerB, BLSPubkey: pubB, BlobCount: 3},
	})

	require.Equal(t, 2, c.Len())

	got, ok := c.ByID(7)
	require.Equal(t, true, ok)
	require.Equal(t, uint64(7), got.ID)
	require.Equal(t, primitives.Slot(6), got.TargetSlot) // slot_of(t) + 1
	require.Equal(t, ownerA, got.Owner)
	require.Equal(t, pubA, got.BLSPubkey)
	require.Equal(t, uint64(2), got.BlobCount)

	got, ok = c.ByID(9)
	require.Equal(t, true, ok)
	require.Equal(t, primitives.Slot(10), got.TargetSlot)

	_, ok = c.ByID(123)
	require.Equal(t, false, ok)
}

func TestCache_ReplaceOverwrites(t *testing.T) {
	c := New(time.Unix(1_700_000_000, 0))

	c.Replace([]Replacement{{ID: 1, SellingTimestamp: 1_700_000_000, BlobCount: 1}})
	require.Equal(t, 1, c.Len())

	c.Replace([]Replacement{
		{ID: 2, SellingTimestamp: 1_700_000_000, BlobCount: 1},
		{ID: 3, SellingTimestamp: 1_700_000_000, BlobCount: 1},
	})
	require.Equal(t, 2, c.Len())

	_, ok := c.ByID(1)
	require.Equal(t, false, ok, "ticket 1 should be evicted by Replace")
	_, ok = c.ByID(2)
	require.Equal(t, true, ok)
	_, ok = c.ByID(3)
	require.Equal(t, true, ok)
}

func TestCache_ReplaceEmptyClears(t *testing.T) {
	c := New(time.Unix(1_700_000_000, 0))
	c.Replace([]Replacement{{ID: 1, SellingTimestamp: 1_700_000_000, BlobCount: 1}})
	require.Equal(t, 1, c.Len())

	c.Replace(nil)
	require.Equal(t, 0, c.Len())
}

func TestCache_AllSnapshot(t *testing.T) {
	c := New(time.Unix(1_700_000_000, 0))
	c.Replace([]Replacement{
		{ID: 1, SellingTimestamp: 1_700_000_000, BlobCount: 1},
		{ID: 2, SellingTimestamp: 1_700_000_000, BlobCount: 1},
	})

	all := c.All()
	require.Equal(t, 2, len(all))

	// Snapshot must not mutate cache on caller-side changes.
	all[0].BlobCount = 999
	got, _ := c.ByID(all[0].ID)
	require.Equal(t, uint64(1), got.BlobCount)
}

func TestCache_ZeroGenesisLeavesTargetSlotZero(t *testing.T) {
	c := New(time.Time{}) // zero genesis
	c.Replace([]Replacement{{ID: 1, SellingTimestamp: 1_700_000_000, BlobCount: 1}})
	got, ok := c.ByID(1)
	require.Equal(t, true, ok)
	require.Equal(t, primitives.Slot(0), got.TargetSlot)
}

func TestCache_SetGenesisLater(t *testing.T) {
	c := New(time.Time{})
	c.Replace([]Replacement{{ID: 1, SellingTimestamp: 1_700_000_000, BlobCount: 1}})
	got, _ := c.ByID(1)
	require.Equal(t, primitives.Slot(0), got.TargetSlot)

	genesis := time.Unix(1_700_000_000, 0)
	c.SetGenesis(genesis)
	c.Replace([]Replacement{{ID: 1, SellingTimestamp: uint64(genesis.Unix() + int64(params.BeaconConfig().SecondsPerSlot*3)), BlobCount: 1}})
	got, _ = c.ByID(1)
	require.Equal(t, primitives.Slot(4), got.TargetSlot)
}
