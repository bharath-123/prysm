package beacon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/cache/ticketcache"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/ethereum/go-ethereum/common"
)

func TestGetActiveBlobStreamingTickets_CacheNotWired(t *testing.T) {
	s := &Server{TicketCache: nil}

	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/active_tickets", nil)
	w := httptest.NewRecorder()
	s.GetActiveBlobStreamingTickets(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGetActiveBlobStreamingTickets_Empty(t *testing.T) {
	s := &Server{TicketCache: ticketcache.New(time.Unix(1_700_000_000, 0))}

	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/active_tickets", nil)
	w := httptest.NewRecorder()
	s.GetActiveBlobStreamingTickets(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp structs.GetActiveBlobStreamingTicketsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, 0, len(resp.Data))
}

func TestGetActiveBlobStreamingTickets_Populated(t *testing.T) {
	genesis := time.Unix(1_700_000_000, 0)
	cache := ticketcache.New(genesis)

	secondsPerSlot := uint64(params.BeaconConfig().SecondsPerSlot)
	pubA := [ticketcache.PubkeyLength]byte{0x01, 0x02, 0x03}
	pubB := [ticketcache.PubkeyLength]byte{0xff, 0xee}
	ownerA := [20]byte{0xaa}
	ownerB := common.HexToAddress("0x8943545177806ED17B9F23F0a21ee5948eCaa776")

	cache.Replace([]ticketcache.Replacement{
		// Intentionally not sorted by ID — handler must sort.
		{ID: 9, SellingTimestamp: uint64(genesis.Unix()) + secondsPerSlot*3, Owner: ownerB, BLSPubkey: pubB, BlobCount: 5},
		{ID: 2, SellingTimestamp: uint64(genesis.Unix()) + secondsPerSlot*1, Owner: ownerA, BLSPubkey: pubA, BlobCount: 1},
	})

	s := &Server{TicketCache: cache}

	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/active_tickets", nil)
	w := httptest.NewRecorder()
	s.GetActiveBlobStreamingTickets(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp structs.GetActiveBlobStreamingTicketsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, 2, len(resp.Data))

	// Sorted ascending by ticket id.
	require.Equal(t, "2", resp.Data[0].TicketID)
	require.Equal(t, "9", resp.Data[1].TicketID)

	// First entry checks out field-by-field.
	require.Equal(t, "2", resp.Data[0].TargetSlot) // slot_of(genesis+1*slot) + 1 = 2
	require.Equal(t, "1", resp.Data[0].BlobCount)
	require.Equal(t, common.Address(ownerA).Hex(), resp.Data[0].Owner)
	require.Equal(t, "0x"+toHex(pubA[:]), resp.Data[0].BLSPubkey)

	// Second entry sanity-checks target slot and owner-address checksum.
	require.Equal(t, "4", resp.Data[1].TargetSlot)
	require.Equal(t, ownerB.Hex(), resp.Data[1].Owner)
}

// toHex is a tiny helper that avoids pulling hexutil into the test set; common.Address.Hex is checksummed.
func toHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}
