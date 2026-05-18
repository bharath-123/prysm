package beacon

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	"github.com/OffchainLabs/prysm/v7/network/httputil"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// GetActiveBlobStreamingTickets returns the set of currently active
// blob-streaming tickets cached from the EL's most recent
// engine_forkchoiceUpdatedV5 response. Tickets are returned ordered by ID for
// stable diffing across calls.
//
// Response: 200 with a JSON list. 503 when the cache is not wired (Heze
// feature disabled).
func (s *Server) GetActiveBlobStreamingTickets(w http.ResponseWriter, r *http.Request) {
	_, span := trace.StartSpan(r.Context(), "beacon.GetActiveBlobStreamingTickets")
	defer span.End()

	if s.TicketCache == nil {
		httputil.HandleError(w, "Blob-streaming ticket cache is not enabled", http.StatusServiceUnavailable)
		return
	}

	tickets := s.TicketCache.All()
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].ID < tickets[j].ID })

	out := make([]*structs.ActiveBlobStreamingTicket, len(tickets))
	for i, t := range tickets {
		owner := common.Address(t.Owner)
		pubkey := hexutil.Encode(t.BLSPubkey[:])
		out[i] = &structs.ActiveBlobStreamingTicket{
			TicketID:   strconv.FormatUint(t.ID, 10),
			TargetSlot: strconv.FormatUint(uint64(t.TargetSlot), 10),
			Owner:      owner.Hex(),
			BLSPubkey:  pubkey,
			BlobCount:  strconv.FormatUint(t.BlobCount, 10),
		}
	}

	httputil.WriteJson(w, &structs.GetActiveBlobStreamingTicketsResponse{Data: out})
}
