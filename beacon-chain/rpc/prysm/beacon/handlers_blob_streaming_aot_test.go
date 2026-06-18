package beacon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/kzg"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/cache/ticketcache"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/signing"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/das"
	mockp2p "github.com/OffchainLabs/prysm/v7/beacon-chain/p2p/testing"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/config/params"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls"
	"github.com/OffchainLabs/prysm/v7/crypto/random"
	enginev1 "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const aotBlobsPath = "/prysm/v1/beacon/blob_streaming/aot_blobs"

// aotTestFixture is a fully-valid submission: a ticket cache holding one ticket whose
// BLS pubkey owns the bundle, the matching request, and the wired server.
type aotTestFixture struct {
	server  *Server
	cache   *das.AotDataColumnCache
	request *structs.SubmitAotBlobsRequest
	numBlob int
}

func newAotTestFixture(t *testing.T) *aotTestFixture {
	require.NoError(t, kzg.Start())

	const ticketID = uint64(2)
	const numBlobs = 2

	genesis := time.Unix(1_700_000_000, 0)
	ticketCache := ticketcache.New(genesis)
	key, err := bls.RandKey()
	require.NoError(t, err)
	var pubkey [ticketcache.PubkeyLength]byte
	copy(pubkey[:], key.PublicKey().Marshal())
	ticketCache.Replace([]ticketcache.Replacement{
		{ID: ticketID, SellingTimestamp: uint64(genesis.Unix()), Owner: [20]byte{0xaa}, BLSPubkey: pubkey, BlobCount: numBlobs},
	})
	ticket, ok := ticketCache.ByID(ticketID)
	require.Equal(t, true, ok)

	// Build real blobs, commitments, and cell proofs.
	commitments := make([][]byte, numBlobs)
	blobInputs := make([]*structs.AotBlobInput, numBlobs)
	for i := range numBlobs {
		blob := randKzgBlob(int64(i + 1))
		commitment, err := kzg.BlobToKZGCommitment(&blob)
		require.NoError(t, err)
		_, proofs, err := kzg.ComputeCellsAndKZGProofs(&blob)
		require.NoError(t, err)
		require.Equal(t, fieldparams.NumberOfColumns, len(proofs))

		commitments[i] = commitment[:]
		cellProofs := make([]string, len(proofs))
		for j := range proofs {
			cellProofs[j] = hexutil.Encode(proofs[j][:])
		}
		blobInputs[i] = &structs.AotBlobInput{
			Blob:          hexutil.Encode(blob[:]),
			KzgCommitment: hexutil.Encode(commitment[:]),
			KzgCellProofs: cellProofs,
		}
	}

	signature := signAotBlobInfo(t, key, ticketID, ticket.TargetSlot, commitments)

	cache := das.NewAotDataColumnCache()
	return &aotTestFixture{
		server: &Server{
			TicketCache:        ticketCache,
			AotDataColumnCache: cache,
			Broadcaster:        &mockp2p.MockBroadcaster{},
		},
		cache:   cache,
		numBlob: numBlobs,
		request: &structs.SubmitAotBlobsRequest{
			TicketID:          "2",
			BlobInfoSignature: hexutil.Encode(signature),
			Blobs:             blobInputs,
		},
	}
}

func (f *aotTestFixture) submit(t *testing.T) *httptest.ResponseRecorder {
	body, err := json.Marshal(f.request)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, aotBlobsPath, bytes.NewReader(body))
	w := httptest.NewRecorder()
	f.server.SubmitAotBlobs(w, req)
	return w
}

func TestSubmitAotBlobs_Success(t *testing.T) {
	f := newAotTestFixture(t)
	w := f.submit(t)
	require.Equal(t, http.StatusOK, w.Code)

	// All NUMBER_OF_COLUMNS columns staged for the single bundle: a full-custody query
	// returns one bundle with one versioned hash per blob.
	fullCustody := make(map[uint64]bool, fieldparams.NumberOfColumns)
	for i := range uint64(fieldparams.NumberOfColumns) {
		fullCustody[i] = true
	}
	available := f.cache.AvailableAotBlobVersionedHashes(fullCustody)
	require.Equal(t, 1, len(available))
	require.Equal(t, f.numBlob, len(available[0]))
}

func TestSubmitAotBlobs_InvalidSignature(t *testing.T) {
	f := newAotTestFixture(t)
	// Flip a byte of the otherwise-valid signature.
	bad := hexutil.MustDecode(f.request.BlobInfoSignature)
	bad[0] ^= 0xff
	f.request.BlobInfoSignature = hexutil.Encode(bad)

	w := f.submit(t)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSubmitAotBlobs_InvalidProof(t *testing.T) {
	f := newAotTestFixture(t)
	// Corrupt one cell proof so the KZG batch verification fails.
	bad := hexutil.MustDecode(f.request.Blobs[0].KzgCellProofs[0])
	bad[0] ^= 0xff
	f.request.Blobs[0].KzgCellProofs[0] = hexutil.Encode(bad)

	w := f.submit(t)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSubmitAotBlobs_UnknownTicket(t *testing.T) {
	f := newAotTestFixture(t)
	f.request.TicketID = "9999"

	w := f.submit(t)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestSubmitAotBlobs_CachesNotWired(t *testing.T) {
	s := &Server{TicketCache: nil, AotDataColumnCache: nil}
	req := httptest.NewRequest(http.MethodPost, aotBlobsPath, bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	s.SubmitAotBlobs(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGetAotDataColumns_CacheNotWired(t *testing.T) {
	s := &Server{AotDataColumnCache: nil}
	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/aot_data_columns", nil)
	w := httptest.NewRecorder()
	s.GetAotDataColumns(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestGetAotDataColumns_Empty(t *testing.T) {
	s := &Server{AotDataColumnCache: das.NewAotDataColumnCache()}
	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/aot_data_columns", nil)
	w := httptest.NewRecorder()
	s.GetAotDataColumns(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp structs.GetAotDataColumnsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, 0, len(resp.Data))
}

// TestGetAotDataColumns_AfterSubmit submits a valid bundle, then dumps the cache and
// checks the bundle is reported with all NUMBER_OF_COLUMNS indices and the right blob count.
func TestGetAotDataColumns_AfterSubmit(t *testing.T) {
	f := newAotTestFixture(t)
	require.Equal(t, http.StatusOK, f.submit(t).Code)

	req := httptest.NewRequest(http.MethodGet, "/prysm/v1/beacon/blob_streaming/aot_data_columns", nil)
	w := httptest.NewRecorder()
	f.server.GetAotDataColumns(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp structs.GetAotDataColumnsResponse
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	require.Equal(t, 1, len(resp.Data))
	require.Equal(t, "2", resp.Data[0].TicketID)
	require.Equal(t, strconv.Itoa(f.numBlob), resp.Data[0].BlobCount)
	require.Equal(t, f.numBlob, len(resp.Data[0].Commitments))
	require.Equal(t, fieldparams.NumberOfColumns, len(resp.Data[0].StoredColumnIndices))
}

func randKzgBlob(seed int64) kzg.Blob {
	r := random.GetRandBlob(seed)
	var b kzg.Blob
	copy(b[:], r[:])
	return b
}

func signAotBlobInfo(t *testing.T, key bls.SecretKey, ticketID uint64, targetSlot primitives.Slot, commitments [][]byte) []byte {
	info := &enginev1.AOTBlobInfo{
		TicketId:           ticketID,
		TargetSlot:         targetSlot,
		BlobKzgCommitments: commitments,
	}
	domain, err := signing.ComputeDomain(params.BeaconConfig().DomainAotBlob, nil, nil)
	require.NoError(t, err)
	signingRoot, err := signing.ComputeSigningRoot(info, domain)
	require.NoError(t, err)
	return key.Sign(signingRoot[:]).Marshal()
}
