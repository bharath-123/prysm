package execution

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/cache/ticketcache"
	"github.com/OffchainLabs/prysm/v7/config/features"
	"github.com/OffchainLabs/prysm/v7/config/params"
	payloadattribute "github.com/OffchainLabs/prysm/v7/consensus-types/payload-attribute"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	pb "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
)

// fcuMethodObserver runs an httptest JSON-RPC endpoint that records the
// engine_forkchoiceUpdatedVN method observed in the request body, and
// replies with the given response.
func fcuMethodObserver(t *testing.T, resp *ForkchoiceUpdatedResponse) (*Service, *string, func()) {
	var observed string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, r.Body.Close())
		switch {
		case strings.Contains(string(body), `"method":"`+ForkchoiceUpdatedMethodV5+`"`):
			observed = ForkchoiceUpdatedMethodV5
		case strings.Contains(string(body), `"method":"`+ForkchoiceUpdatedMethodV4+`"`):
			observed = ForkchoiceUpdatedMethodV4
		default:
			observed = "unknown"
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  resp,
		}))
	}))
	rpcClient, err := rpc.DialHTTP(srv.URL)
	require.NoError(t, err)
	s := &Service{}
	s.rpcClient = rpcClient
	return s, &observed, srv.Close
}

func TestForkchoiceUpdated_V5_RouteFlagOff(t *testing.T) {
	resetFeatures := setTicketCacheFeature(false)
	t.Cleanup(resetFeatures)

	resp := validForkchoiceResponse()
	s, observed, cleanup := fcuMethodObserver(t, resp)
	t.Cleanup(cleanup)
	cache := ticketcache.New(time.Unix(1_700_000_000, 0))
	s.ticketCache = cache

	attrs := mustGloasAttrs(t)
	_, _, err := s.ForkchoiceUpdated(context.Background(), &pb.ForkchoiceState{}, attrs)
	require.NoError(t, err)
	require.Equal(t, ForkchoiceUpdatedMethodV4, *observed)
	require.Equal(t, 0, cache.Len(), "cache must stay empty when flag is off")
}

func TestForkchoiceUpdated_V5_RouteAndCachePopulate(t *testing.T) {
	resetFeatures := setTicketCacheFeature(true)
	t.Cleanup(resetFeatures)

	pubkey := makePubkey(0x42)
	owner := common.HexToAddress("0x8943545177806ED17B9F23F0a21ee5948eCaa776")
	genesis := time.Unix(1_700_000_000, 0)
	sellingTimestamp := uint64(genesis.Unix() + int64(params.BeaconConfig().SecondsPerSlot*5))

	resp := validForkchoiceResponse()
	resp.ActiveTickets = []*TicketInfoV1{{
		TicketID:              hexutil.Uint64(7),
		SellingBlockTimestamp: hexutil.Uint64(sellingTimestamp),
		Owner:                 owner,
		BLSPubkey:             pubkey[:],
		BlobCount:             hexutil.Uint64(2),
	}}

	s, observed, cleanup := fcuMethodObserver(t, resp)
	t.Cleanup(cleanup)
	cache := ticketcache.New(genesis)
	s.ticketCache = cache

	attrs := mustGloasAttrs(t)
	_, _, err := s.ForkchoiceUpdated(context.Background(), &pb.ForkchoiceState{}, attrs)
	require.NoError(t, err)
	require.Equal(t, ForkchoiceUpdatedMethodV5, *observed)
	require.Equal(t, 1, cache.Len())

	got, ok := cache.ByID(7)
	require.Equal(t, true, ok)
	require.Equal(t, uint64(7), got.ID)
	require.Equal(t, primitives.Slot(6), got.TargetSlot)
	require.Equal(t, owner, common.Address(got.Owner))
	require.Equal(t, pubkey, got.BLSPubkey)
	require.Equal(t, uint64(2), got.BlobCount)
}

func TestForkchoiceUpdated_V5_NilCacheIsNoop(t *testing.T) {
	resetFeatures := setTicketCacheFeature(true)
	t.Cleanup(resetFeatures)

	pk := makePubkey(0x01)
	resp := validForkchoiceResponse()
	resp.ActiveTickets = []*TicketInfoV1{{
		TicketID:              1,
		SellingBlockTimestamp: 1,
		BLSPubkey:             pk[:],
		BlobCount:             1,
	}}

	s, observed, cleanup := fcuMethodObserver(t, resp)
	t.Cleanup(cleanup)
	// No ticketCache wired.
	s.ticketCache = nil

	attrs := mustGloasAttrs(t)
	_, _, err := s.ForkchoiceUpdated(context.Background(), &pb.ForkchoiceState{}, attrs)
	require.NoError(t, err)
	require.Equal(t, ForkchoiceUpdatedMethodV5, *observed)
}

func TestForkchoiceUpdated_V5_SkipsMalformedPubkey(t *testing.T) {
	resetFeatures := setTicketCacheFeature(true)
	t.Cleanup(resetFeatures)

	resp := validForkchoiceResponse()
	resp.ActiveTickets = []*TicketInfoV1{
		{
			TicketID:              1,
			SellingBlockTimestamp: 1_700_000_000,
			BLSPubkey:             []byte{0x01, 0x02}, // too short, must be 48 bytes
			BlobCount:             1,
		},
		{
			TicketID:              2,
			SellingBlockTimestamp: 1_700_000_000,
			BLSPubkey:             pk2Bytes(0xaa),
			BlobCount:             3,
		},
	}

	s, _, cleanup := fcuMethodObserver(t, resp)
	t.Cleanup(cleanup)
	cache := ticketcache.New(time.Unix(1_700_000_000, 0))
	s.ticketCache = cache

	attrs := mustGloasAttrs(t)
	_, _, err := s.ForkchoiceUpdated(context.Background(), &pb.ForkchoiceState{}, attrs)
	require.NoError(t, err)
	require.Equal(t, 1, cache.Len(), "malformed ticket must be dropped")
	got, ok := cache.ByID(2)
	require.Equal(t, true, ok)
	require.Equal(t, uint64(3), got.BlobCount)
}

func setTicketCacheFeature(on bool) func() {
	prev := features.Get()
	cp := *prev
	cp.EnableHezeTicketCache = on
	features.Init(&cp)
	return func() { features.Init(prev) }
}

func mustGloasAttrs(t *testing.T) payloadattribute.Attributer {
	attrs, err := payloadattribute.New(&pb.PayloadAttributesV4{
		Timestamp:             1,
		PrevRandao:            []byte("random"),
		SuggestedFeeRecipient: []byte("suggestedFeeRecipient"),
		Withdrawals:           []*pb.Withdrawal{{ValidatorIndex: 1, Amount: 1}},
		ParentBeaconBlockRoot: []byte("parentBeaconBlockRoot"),
		SlotNumber:            1,
	})
	require.NoError(t, err)
	return attrs
}

func validForkchoiceResponse() *ForkchoiceUpdatedResponse {
	id := pb.PayloadIDBytes([8]byte{1, 2, 3, 4, 5, 6, 7, 8})
	return &ForkchoiceUpdatedResponse{
		Status: &pb.PayloadStatus{
			Status:          pb.PayloadStatus_VALID,
			LatestValidHash: []byte("validHash"),
		},
		PayloadId: &id,
	}
}

func makePubkey(seed byte) [ticketcache.PubkeyLength]byte {
	var p [ticketcache.PubkeyLength]byte
	for i := range p {
		p[i] = seed
	}
	return p
}

func pk2Bytes(seed byte) []byte {
	p := makePubkey(seed)
	return p[:]
}
