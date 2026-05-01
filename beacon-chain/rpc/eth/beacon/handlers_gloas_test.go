package beacon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	chainMock "github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain/testing"
	dbTest "github.com/OffchainLabs/prysm/v7/beacon-chain/db/testing"
	executiontesting "github.com/OffchainLabs/prysm/v7/beacon-chain/execution/testing"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/rpc/lookup"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/rpc/testutil"
	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	enginev1 "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/runtime/version"
	"github.com/OffchainLabs/prysm/v7/testing/assert"
	mock2 "github.com/OffchainLabs/prysm/v7/testing/mock"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeEnvelopePublisher struct {
	called     bool
	envelope   *ethpb.SignedExecutionPayloadEnvelope
	blobs      [][]byte
	cellProofs [][]byte
	err        error
}

func (f *fakeEnvelopePublisher) PublishExecutionPayloadEnvelopeWithBlobs(
	_ context.Context,
	env *ethpb.SignedExecutionPayloadEnvelope,
	blobs [][]byte,
	cellProofs [][]byte,
) (*emptypb.Empty, error) {
	f.called = true
	f.envelope = env
	f.blobs = blobs
	f.cellProofs = cellProofs
	if f.err != nil {
		return nil, f.err
	}
	return &emptypb.Empty{}, nil
}

func TestGetExecutionPayloadEnvelope_AcceptsSlotID(t *testing.T) {
	ctx := t.Context()
	beaconDB := dbTest.SetupDB(t)

	root := bytesutil.ToBytes32(bytesutil.PadTo([]byte("beacon-root"), 32))
	blockHash := bytesutil.ToBytes32(bytesutil.PadTo([]byte("block-hash"), 32))

	env := &ethpb.SignedExecutionPayloadEnvelope{
		Message: &ethpb.ExecutionPayloadEnvelope{
			Payload: &enginev1.ExecutionPayloadGloas{
				ParentHash:    bytesutil.PadTo([]byte("parent"), 32),
				FeeRecipient:  bytesutil.PadTo([]byte("fee"), 20),
				StateRoot:     bytesutil.PadTo([]byte("state"), 32),
				ReceiptsRoot:  bytesutil.PadTo([]byte("receipts"), 32),
				LogsBloom:     make([]byte, 256),
				PrevRandao:    bytesutil.PadTo([]byte("randao"), 32),
				BaseFeePerGas: bytesutil.PadTo([]byte{1}, 32),
				BlockHash:     blockHash[:],
				Transactions:  [][]byte{},
				Withdrawals:   []*enginev1.Withdrawal{},
				SlotNumber:    primitives.Slot(177),
			},
			ExecutionRequests: &enginev1.ExecutionRequests{},
			BuilderIndex:      primitives.BuilderIndex(42),
			BeaconBlockRoot:   root[:],
		},
		Signature: bytesutil.PadTo([]byte("sig"), 96),
	}
	require.NoError(t, beaconDB.SaveExecutionPayloadEnvelope(ctx, env))

	reconstructor := &executiontesting.EngineClient{
		ExecutionPayloadByBlockHash: map[[32]byte]*enginev1.ExecutionPayload{
			blockHash: &enginev1.ExecutionPayload{
				ParentHash:    bytesutil.PadTo([]byte("parent"), 32),
				FeeRecipient:  bytesutil.PadTo([]byte("fee"), 20),
				StateRoot:     bytesutil.PadTo([]byte("state"), 32),
				ReceiptsRoot:  bytesutil.PadTo([]byte("receipts"), 32),
				LogsBloom:     make([]byte, 256),
				PrevRandao:    bytesutil.PadTo([]byte("randao"), 32),
				BaseFeePerGas: bytesutil.PadTo([]byte{1}, 32),
				BlockHash:     blockHash[:],
				Transactions:  [][]byte{},
			},
		},
	}

	chain := &chainMock.ChainService{
		FinalizedRoots:  map[[32]byte]bool{},
		OptimisticRoots: map[[32]byte]bool{},
	}
	s := &Server{
		BeaconDB:               beaconDB,
		Blocker:                &testutil.MockBlocker{RootToReturn: root},
		ExecutionReconstructor: reconstructor,
		OptimisticModeFetcher:  chain,
		FinalizationFetcher:    chain,
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/eth/v1/beacon/execution_payload_envelope/{block_id}", nil)
	req.SetPathValue("block_id", "177")
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.GetExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, version.String(version.Gloas), w.Header().Get("Eth-Consensus-Version"))
}

func TestGetExecutionPayloadEnvelope_BlockNotFound(t *testing.T) {
	s := &Server{
		Blocker: &testutil.MockBlocker{
			ErrorToReturn: lookup.NewBlockNotFoundError("missing block"),
		},
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/eth/v1/beacon/execution_payload_envelope/{block_id}", nil)
	req.SetPathValue("block_id", "not-a-root")
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.GetExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, true, bytes.Contains(w.Body.Bytes(), []byte("Block not found")))
}

func testSignedEnvelope() *ethpb.SignedExecutionPayloadEnvelope {
	return &ethpb.SignedExecutionPayloadEnvelope{
		Message: &ethpb.ExecutionPayloadEnvelope{
			Payload: &enginev1.ExecutionPayloadGloas{
				ParentHash:    bytesutil.PadTo([]byte("parent"), 32),
				FeeRecipient:  bytesutil.PadTo([]byte("fee"), 20),
				StateRoot:     bytesutil.PadTo([]byte("state"), 32),
				ReceiptsRoot:  bytesutil.PadTo([]byte("receipts"), 32),
				LogsBloom:     make([]byte, 256),
				PrevRandao:    bytesutil.PadTo([]byte("randao"), 32),
				BaseFeePerGas: bytesutil.PadTo([]byte{1}, 32),
				BlockHash:     bytesutil.PadTo([]byte("blockhash"), 32),
				Transactions:  [][]byte{},
				Withdrawals:   []*enginev1.Withdrawal{},
				SlotNumber:    primitives.Slot(100),
			},
			ExecutionRequests: &enginev1.ExecutionRequests{},
			BuilderIndex:      primitives.BuilderIndex(42),
			BeaconBlockRoot:   bytesutil.PadTo([]byte("beacon-root"), 32),
		},
		Signature: bytesutil.PadTo([]byte("sig"), 96),
	}
}

func TestPublishExecutionPayloadEnvelope_OK(t *testing.T) {
	ctrl := gomock.NewController(t)
	signed := testSignedEnvelope()

	v1alpha1Server := mock2.NewMockBeaconNodeValidatorServer(ctrl)
	v1alpha1Server.EXPECT().PublishExecutionPayloadEnvelope(
		gomock.Any(), gomock.Any(),
	).Return(&emptypb.Empty{}, nil)

	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(signed)
	require.NoError(t, err)
	body, err := json.Marshal(jsonEnvelope)
	require.NoError(t, err)

	s := &Server{V1Alpha1ValidatorServer: v1alpha1Server}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader(body))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestPublishExecutionPayloadEnvelope_InvalidBody(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPublishExecutionPayloadEnvelope_ServerError(t *testing.T) {
	ctrl := gomock.NewController(t)

	v1alpha1Server := mock2.NewMockBeaconNodeValidatorServer(ctrl)
	v1alpha1Server.EXPECT().PublishExecutionPayloadEnvelope(
		gomock.Any(), gomock.Any(),
	).Return(nil, status.Error(codes.Internal, "broadcast failed"))

	signed := testSignedEnvelope()
	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(signed)
	require.NoError(t, err)
	body, err := json.Marshal(jsonEnvelope)
	require.NoError(t, err)

	s := &Server{V1Alpha1ValidatorServer: v1alpha1Server}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader(body))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestPublishExecutionPayloadEnvelope_WithBlobs(t *testing.T) {
	signed := testSignedEnvelope()
	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(signed)
	require.NoError(t, err)

	blob := bytesutil.PadTo([]byte("blob-0"), 131072)
	proof := bytesutil.PadTo([]byte("proof"), 48)
	jsonEnvelope.Blobs = []string{hexutil.Encode(blob)}
	jsonEnvelope.CellProofs = make([]string, fieldparams.NumberOfColumns)
	for i := range jsonEnvelope.CellProofs {
		jsonEnvelope.CellProofs[i] = hexutil.Encode(proof)
	}

	body, err := json.Marshal(jsonEnvelope)
	require.NoError(t, err)

	publisher := &fakeEnvelopePublisher{}
	s := &Server{ExecutionPayloadEnvelopePublisher: publisher}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader(body))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, true, publisher.called)
	require.Equal(t, 1, len(publisher.blobs))
	assert.DeepEqual(t, blob, publisher.blobs[0])
	require.Equal(t, fieldparams.NumberOfColumns, len(publisher.cellProofs))
}

func TestPublishExecutionPayloadEnvelope_WithBlobs_MismatchedProofCount(t *testing.T) {
	signed := testSignedEnvelope()
	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(signed)
	require.NoError(t, err)

	blob := bytesutil.PadTo([]byte("blob-0"), 131072)
	jsonEnvelope.Blobs = []string{hexutil.Encode(blob)}
	jsonEnvelope.CellProofs = []string{hexutil.Encode(bytesutil.PadTo([]byte("p"), 48))} // too few

	body, err := json.Marshal(jsonEnvelope)
	require.NoError(t, err)

	publisher := &fakeEnvelopePublisher{}
	s := &Server{ExecutionPayloadEnvelopePublisher: publisher}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader(body))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, false, publisher.called)
	assert.Equal(t, true, bytes.Contains(w.Body.Bytes(), []byte("expected")))
}

func TestPublishExecutionPayloadEnvelope_WithBlobs_PublisherUnset(t *testing.T) {
	signed := testSignedEnvelope()
	jsonEnvelope, err := structs.SignedExecutionPayloadEnvelopeFromConsensus(signed)
	require.NoError(t, err)

	blob := bytesutil.PadTo([]byte("blob-0"), 131072)
	jsonEnvelope.Blobs = []string{hexutil.Encode(blob)}
	jsonEnvelope.CellProofs = make([]string, fieldparams.NumberOfColumns)
	proof := bytesutil.PadTo([]byte("proof"), 48)
	for i := range jsonEnvelope.CellProofs {
		jsonEnvelope.CellProofs[i] = hexutil.Encode(proof)
	}

	body, err := json.Marshal(jsonEnvelope)
	require.NoError(t, err)

	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/beacon/execution_payload_envelope", bytes.NewReader(body))
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}

	s.PublishExecutionPayloadEnvelope(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
