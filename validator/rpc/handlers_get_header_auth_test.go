package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OffchainLabs/prysm/v7/api/client/builder"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/testing/require"
	"github.com/OffchainLabs/prysm/v7/validator/accounts"
	"github.com/OffchainLabs/prysm/v7/validator/accounts/iface"
	"github.com/OffchainLabs/prysm/v7/validator/client"
	"github.com/OffchainLabs/prysm/v7/validator/client/testutil"
	"github.com/OffchainLabs/prysm/v7/validator/keymanager"
	"github.com/OffchainLabs/prysm/v7/validator/keymanager/derived"
	mocks "github.com/OffchainLabs/prysm/v7/validator/testing"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const getHeaderAuthTestPassword = "29384283xasjasd32%%&*@*#*"

// TestSignGetHeaderAuth_ReturnsValidSignature tests the full signing flow: POST to SignGetHeaderAuth,
// get the signature, recompute the expected signing root, and verify the signature with BLS.
func TestSignGetHeaderAuth_ReturnsValidSignature(t *testing.T) {
	ctx := context.Background()
	localWalletDir := setupWalletDir(t)
	defaultWalletPath = localWalletDir
	opts := []accounts.Option{
		accounts.WithWalletDir(defaultWalletPath),
		accounts.WithKeymanagerType(keymanager.Derived),
		accounts.WithWalletPassword(getHeaderAuthTestPassword),
		accounts.WithSkipMnemonicConfirm(true),
	}
	acc, err := accounts.NewCLIManager(opts...)
	require.NoError(t, err)
	w, err := acc.WalletCreate(ctx)
	require.NoError(t, err)
	km, err := w.InitializeKeymanager(ctx, iface.InitKeymanagerConfig{ListenForChanges: false})
	require.NoError(t, err)
	vs, err := client.NewValidatorService(ctx, &client.Config{
		Conn:      mocks.MockNodeConnection(),
		Wallet:    w,
		Validator: &testutil.FakeValidator{Km: km},
	})
	require.NoError(t, err)
	s := &Server{
		walletInitialized: true,
		wallet:            w,
		validatorService:  vs,
	}

	dr, ok := km.(*derived.Keymanager)
	require.Equal(t, true, ok)
	err = dr.RecoverAccountsFromMnemonic(ctx, mocks.TestMnemonic, derived.DefaultMnemonicLanguage, "", 1)
	require.NoError(t, err)
	pubKeys, err := dr.FetchValidatingPublicKeys(ctx)
	require.NoError(t, err)
	pubkey := pubKeys[0]

	slot := primitives.Slot(100)
	parentHash := [32]byte{}
	parentHash[0] = 0xab
	parentHash[31] = 0xcd

	body := SignGetHeaderAuthRequest{
		Slot:       uint64(slot),
		ParentHash: hexutil.Encode(parentHash[:]),
		Pubkey:     hexutil.Encode(pubkey[:]),
	}
	bodyBytes, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/eth/v1/validator/sign_get_header_auth", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	wRec := httptest.NewRecorder()
	wRec.Body = &bytes.Buffer{}
	s.SignGetHeaderAuth(wRec, req)

	require.Equal(t, http.StatusOK, wRec.Code, "response body: %s", wRec.Body.String())
	var resp SignGetHeaderAuthResponse
	require.NoError(t, json.NewDecoder(wRec.Body).Decode(&resp))
	require.NotNil(t, resp.Data)
	require.NotEmpty(t, resp.Data.Signature)

	sigBytes, err := hexutil.Decode(resp.Data.Signature)
	require.NoError(t, err)
	require.Equal(t, 96, len(sigBytes), "BLS signature must be 96 bytes")

	// Recompute the expected signing root (same as handler)
	data := &builder.GetHeaderAuthData{
		Slot:       slot,
		ParentHash: parentHash,
		Pubkey:     pubkey,
	}
	root, err := data.HashTreeRoot()
	require.NoError(t, err)

	// Verify the signature with the pubkey and root
	pk, err := bls.PublicKeyFromBytes(pubkey[:])
	require.NoError(t, err)
	valid, err := bls.VerifySignature(sigBytes, root, pk)
	require.NoError(t, err)
	require.Equal(t, true, valid, "signature must verify against GetHeaderAuthData root and request pubkey")
}

// TestSignGetHeaderAuth_Integration_HTTPSigner tests the flow used by the beacon node: an HTTP
// signer calls the validator's SignGetHeaderAuth endpoint and receives a signature, then we verify it.
func TestSignGetHeaderAuth_Integration_HTTPSigner(t *testing.T) {
	ctx := context.Background()
	localWalletDir := setupWalletDir(t)
	defaultWalletPath = localWalletDir
	opts := []accounts.Option{
		accounts.WithWalletDir(defaultWalletPath),
		accounts.WithKeymanagerType(keymanager.Derived),
		accounts.WithWalletPassword(getHeaderAuthTestPassword),
		accounts.WithSkipMnemonicConfirm(true),
	}
	acc, err := accounts.NewCLIManager(opts...)
	require.NoError(t, err)
	w, err := acc.WalletCreate(ctx)
	require.NoError(t, err)
	km, err := w.InitializeKeymanager(ctx, iface.InitKeymanagerConfig{ListenForChanges: false})
	require.NoError(t, err)
	vs, err := client.NewValidatorService(ctx, &client.Config{
		Conn:      mocks.MockNodeConnection(),
		Wallet:    w,
		Validator: &testutil.FakeValidator{Km: km},
	})
	require.NoError(t, err)
	srv := &Server{
		walletInitialized: true,
		wallet:            w,
		validatorService:  vs,
	}
	dr, ok := km.(*derived.Keymanager)
	require.Equal(t, true, ok)
	err = dr.RecoverAccountsFromMnemonic(ctx, mocks.TestMnemonic, derived.DefaultMnemonicLanguage, "", 1)
	require.NoError(t, err)
	pubKeys, err := dr.FetchValidatingPublicKeys(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, pubKeys)
	pubkey := pubKeys[0]

	// Simulate beacon node: signer that POSTs to our handler via a transport (no real port needed).
	baseURL := "http://validator.test"
	transport := &handlerTransport{handler: srv.SignGetHeaderAuth, baseURL: baseURL}
	signer := newHTTPGetHeaderAuthSignerWithClient(baseURL, &http.Client{Transport: transport})
	slot := primitives.Slot(42)
	parentHash := [32]byte{}
	for i := range 32 {
		parentHash[i] = byte(i)
	}

	sig := signer(ctx, slot, parentHash, pubkey)
	require.NotNil(t, sig, "signer must return a signature")
	require.Equal(t, 96, len(sig))

	// Verify the signature
	data := &builder.GetHeaderAuthData{Slot: slot, ParentHash: parentHash, Pubkey: pubkey}
	root, err := data.HashTreeRoot()
	require.NoError(t, err)
	pk, err := bls.PublicKeyFromBytes(pubkey[:])
	require.NoError(t, err)
	valid, err := bls.VerifySignature(sig, root, pk)
	require.NoError(t, err)
	require.Equal(t, true, valid, "HTTP signer returned signature must verify")
}

// NewHTTPGetHeaderAuthSignerForTest is the same logic as beacon-chain/builder.NewHTTPGetHeaderAuthSigner
// so we can test the integration without importing the beacon-chain package (which would create a cycle).
// It POSTs (slot, parent_hash, pubkey) to baseURL + /eth/v1/validator/sign_get_header_auth.
func NewHTTPGetHeaderAuthSignerForTest(baseURL string) func(context.Context, primitives.Slot, [32]byte, [48]byte) []byte {
	url := baseURL + "/eth/v1/validator/sign_get_header_auth"
	client := &http.Client{}
	return func(ctx context.Context, slot primitives.Slot, parentHash [32]byte, pubkey [48]byte) []byte {
		reqBody := struct {
			Slot       uint64 `json:"slot"`
			ParentHash string `json:"parent_hash"`
			Pubkey     string `json:"pubkey"`
		}{
			uint64(slot),
			hexutil.Encode(parentHash[:]),
			hexutil.Encode(pubkey[:]),
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var out struct {
			Data *struct {
				Signature string `json:"signature"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil || out.Data.Signature == "" {
			return nil
		}
		sigBytes, err := hexutil.Decode(out.Data.Signature)
		if err != nil || len(sigBytes) != 96 {
			return nil
		}
		return sigBytes
	}
}

// handlerTransport rounds requests to the given handler (for testing without a real server).
type handlerTransport struct {
	handler http.HandlerFunc
	baseURL string
}

func (t *handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	w.Body = &bytes.Buffer{}
	t.handler(w, req)
	return w.Result(), nil
}

func newHTTPGetHeaderAuthSignerWithClient(baseURL string, c *http.Client) func(context.Context, primitives.Slot, [32]byte, [48]byte) []byte {
	url := baseURL + "/eth/v1/validator/sign_get_header_auth"
	return func(ctx context.Context, slot primitives.Slot, parentHash [32]byte, pubkey [48]byte) []byte {
		reqBody := struct {
			Slot       uint64 `json:"slot"`
			ParentHash string `json:"parent_hash"`
			Pubkey     string `json:"pubkey"`
		}{
			uint64(slot),
			hexutil.Encode(parentHash[:]),
			hexutil.Encode(pubkey[:]),
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var out struct {
			Data *struct {
				Signature string `json:"signature"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data == nil || out.Data.Signature == "" {
			return nil
		}
		sigBytes, err := hexutil.Decode(out.Data.Signature)
		if err != nil || len(sigBytes) != 96 {
			return nil
		}
		return sigBytes
	}
}

func TestSignGetHeaderAuth_Errors(t *testing.T) {
	s := &Server{walletInitialized: false}
	body := SignGetHeaderAuthRequest{Slot: 1, ParentHash: "0x", Pubkey: "0x"}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/validator/sign_get_header_auth", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.SignGetHeaderAuth(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)

	// Uninitialized wallet
	s = &Server{walletInitialized: true, validatorService: nil}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/eth/v1/validator/sign_get_header_auth", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	s.SignGetHeaderAuth(w, req)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// TestSignGetHeaderAuth_WrongPubkey returns error when pubkey is not managed by the client.
func TestSignGetHeaderAuth_WrongPubkey(t *testing.T) {
	ctx := context.Background()
	localWalletDir := setupWalletDir(t)
	defaultWalletPath = localWalletDir
	opts := []accounts.Option{
		accounts.WithWalletDir(defaultWalletPath),
		accounts.WithKeymanagerType(keymanager.Derived),
		accounts.WithWalletPassword(getHeaderAuthTestPassword),
		accounts.WithSkipMnemonicConfirm(true),
	}
	acc, err := accounts.NewCLIManager(opts...)
	require.NoError(t, err)
	w, err := acc.WalletCreate(ctx)
	require.NoError(t, err)
	km, err := w.InitializeKeymanager(ctx, iface.InitKeymanagerConfig{ListenForChanges: false})
	require.NoError(t, err)
	vs, err := client.NewValidatorService(ctx, &client.Config{
		Conn:      mocks.MockNodeConnection(),
		Wallet:    w,
		Validator: &testutil.FakeValidator{Km: km},
	})
	require.NoError(t, err)
	s := &Server{walletInitialized: true, wallet: w, validatorService: vs}
	dr, ok := km.(*derived.Keymanager)
	require.Equal(t, true, ok)
	err = dr.RecoverAccountsFromMnemonic(ctx, mocks.TestMnemonic, derived.DefaultMnemonicLanguage, "", 1)
	require.NoError(t, err)

	// Use a pubkey that is not in the keymanager (random)
	otherKey, err := bls.RandKey()
	require.NoError(t, err)
	otherPubkey := bytesutil.ToBytes48(otherKey.PublicKey().Marshal())

	body := SignGetHeaderAuthRequest{
		Slot:       1,
		ParentHash: hexutil.Encode(make([]byte, 32)),
		Pubkey:     hexutil.Encode(otherPubkey[:]),
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/eth/v1/validator/sign_get_header_auth", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	wRec := httptest.NewRecorder()
	s.SignGetHeaderAuth(wRec, req)
	require.Equal(t, http.StatusInternalServerError, wRec.Code)
	require.StringContains(t, "no signing key found", wRec.Body.String(), "expected error when pubkey not managed")
}
