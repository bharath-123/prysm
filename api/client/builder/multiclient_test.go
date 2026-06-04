package builder

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/blocks"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/testing/assert"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/OffchainLabs/prysm/v7/testing/util"
)

// sampleBidJSON returns a getExecutionPayloadBid JSON response envelope with
// correctly-sized hex fields.
func sampleBidJSON(t *testing.T) []byte {
	hash32 := "0x" + strings.Repeat("ab", 32)
	addr20 := "0x" + strings.Repeat("cd", 20)
	sig96 := "0x" + strings.Repeat("ef", 96)

	bid := &structs.SignedExecutionPayloadBid{
		Message: &structs.ExecutionPayloadBid{
			ParentBlockHash:       hash32,
			ParentBlockRoot:       hash32,
			BlockHash:             hash32,
			PrevRandao:            hash32,
			FeeRecipient:          addr20,
			GasLimit:              "30000000",
			BuilderIndex:          "7",
			Slot:                  "123",
			Value:                 "1000000000",
			ExecutionPayment:      "500",
			BlobKzgCommitments:    []string{},
			ExecutionRequestsRoot: hash32,
		},
		Signature: sig96,
	}
	envelope := &executionPayloadBidResponse{Version: "gloas", Data: bid}
	out, err := json.Marshal(envelope)
	require.NoError(t, err)
	return out
}

func TestMultiClient_GetExecutionPayloadBid_SingleBuilder(t *testing.T) {
	ctx := t.Context()
	var slot uint64 = 123
	var parentHash, parentRoot [32]byte
	var pubkey [48]byte
	for i := range parentHash {
		parentHash[i] = 0x11
		parentRoot[i] = 0x22
	}
	for i := range pubkey {
		pubkey[i] = 0x33
	}

	hc := &http.Client{
		Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, true, strings.HasPrefix(r.URL.Path, "/eth/v1/builder/execution_payload_bid/123/"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBuffer(sampleBidJSON(t))),
				Request:    r.Clone(ctx),
			}, nil
		}),
	}
	c := &MultiClient{hc: hc}

	bids, err := c.GetExecutionPayloadBid(ctx, []string{"http://builder1:3500"}, nil, primitives.Slot(slot), parentHash, parentRoot, pubkey)
	require.NoError(t, err)
	require.Equal(t, 1, len(bids))
	got, ok := bids["http://builder1:3500"]
	require.Equal(t, true, ok)
	assert.Equal(t, primitives.Slot(123), got.Message.Slot)
	assert.Equal(t, uint64(30000000), got.Message.GasLimit)
	assert.Equal(t, primitives.BuilderIndex(7), got.Message.BuilderIndex)
	assert.Equal(t, primitives.Gwei(1000000000), got.Message.Value)
	assert.Equal(t, primitives.Gwei(500), got.Message.ExecutionPayment)
	require.Equal(t, fieldparams.FeeRecipientLength, len(got.Message.FeeRecipient))
	require.Equal(t, fieldparams.BLSSignatureLength, len(got.Signature))
}

func TestMultiClient_GetExecutionPayloadBid_NoBid(t *testing.T) {
	ctx := t.Context()
	hc := &http.Client{
		Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(bytes.NewBuffer(nil)),
				Request:    r.Clone(ctx),
			}, nil
		}),
	}
	c := &MultiClient{hc: hc}

	bids, err := c.GetExecutionPayloadBid(ctx, []string{"http://builder1:3500"}, nil, 1, [32]byte{}, [32]byte{}, [48]byte{})
	require.NoError(t, err)
	require.Equal(t, 0, len(bids))
}

func TestMultiClient_SubmitBeaconBlock(t *testing.T) {
	ctx := t.Context()
	blk, err := blocks.NewSignedBeaconBlock(util.NewBeaconBlockGloas())
	require.NoError(t, err)

	var gotPath, gotHost string
	hc := &http.Client{
		Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.Path
			gotHost = r.URL.Host
			require.Equal(t, http.MethodPost, r.Method)
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       io.NopCloser(bytes.NewBuffer(nil)),
				Request:    r.Clone(ctx),
			}, nil
		}),
	}
	c := &MultiClient{hc: hc}

	// Submits to the passed URL (builder2), not the configured builder1.
	require.NoError(t, c.SubmitBeaconBlock(ctx, "http://builder2:4000", blk))
	require.Equal(t, "/eth/v1/builder/beacon_block", gotPath)
	require.Equal(t, "builder2:4000", gotHost)
}

func TestMultiClient_SubmitBeaconBlock_Error(t *testing.T) {
	ctx := t.Context()
	blk, err := blocks.NewSignedBeaconBlock(util.NewBeaconBlockGloas())
	require.NoError(t, err)

	hc := &http.Client{
		Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewBuffer(nil)),
				Request:    r.Clone(ctx),
			}, nil
		}),
	}
	c := &MultiClient{hc: hc}
	require.ErrorContains(t, "builder", c.SubmitBeaconBlock(ctx, "http://builder1:3500", blk))
}

func TestMultiClient_GetExecutionPayloadBid_FanOut(t *testing.T) {
	ctx := t.Context()
	hc := &http.Client{
		Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
			// builder1 serves a bid, builder2 has none.
			if strings.HasPrefix(r.URL.Host, "builder1") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBuffer(sampleBidJSON(t))),
					Request:    r.Clone(ctx),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNoContent,
				Body:       io.NopCloser(bytes.NewBuffer(nil)),
				Request:    r.Clone(ctx),
			}, nil
		}),
	}
	c := &MultiClient{hc: hc}

	bids, err := c.GetExecutionPayloadBid(ctx, []string{"http://builder1:3500", "http://builder2:3500"}, nil, 1, [32]byte{}, [32]byte{}, [48]byte{})
	require.NoError(t, err)
	require.Equal(t, 1, len(bids))
}
