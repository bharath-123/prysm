package rpc

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/OffchainLabs/prysm/v7/api/client/builder"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	"github.com/OffchainLabs/prysm/v7/network/httputil"
	validatorpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1/validator-client"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/pkg/errors"
)

// SignGetHeaderAuthRequest is the JSON body for POST /eth/v1/validator/sign_get_header_auth.
// The signature is produced by the private key corresponding to Pubkey; Pubkey must be a
// validator key managed by this validator client.
type SignGetHeaderAuthRequest struct {
	Slot       interface{} `json:"slot"`        // Slot as number or decimal string (e.g. 123 or "123")
	ParentHash string      `json:"parent_hash"` // 32-byte hash as hex with 0x prefix
	Pubkey     string      `json:"pubkey"`      // 48-byte BLS pubkey as hex; must match a local validator key (signature uses its private key)
}

// SignGetHeaderAuthResponse is the JSON response.
type SignGetHeaderAuthResponse struct {
	Data *SignGetHeaderAuthData `json:"data"`
}

// SignGetHeaderAuthData holds the signature.
type SignGetHeaderAuthData struct {
	Signature string `json:"signature"` // 96-byte BLS signature as hex with 0x prefix
}

// SignGetHeaderAuth returns a BLS signature over SSZ(slot, parent_hash, pubkey) for the X-Request-Auth header.
// The signature is produced by the private key corresponding to the pubkey in the request; that pubkey
// must be one of the validator keys managed by this client.
// POST /eth/v1/validator/sign_get_header_auth
// Works with local and derived keymanagers. Web3Signer may require additional support.
func (s *Server) SignGetHeaderAuth(w http.ResponseWriter, r *http.Request) {
	ctx, span := trace.StartSpan(r.Context(), "validator.SignGetHeaderAuth")
	defer span.End()

	if r.Method != http.MethodPost {
		httputil.HandleError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.validatorService == nil {
		httputil.HandleError(w, "Validator service not ready.", http.StatusServiceUnavailable)
		return
	}
	if !s.walletInitialized {
		httputil.HandleError(w, "Prysm Wallet not initialized. Please create a new wallet.", http.StatusServiceUnavailable)
		return
	}
	km, err := s.validatorService.Keymanager()
	if err != nil {
		httputil.HandleError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req SignGetHeaderAuthRequest
	err = json.NewDecoder(r.Body).Decode(&req)
	switch {
	case errors.Is(err, io.EOF):
		httputil.HandleError(w, "No data submitted", http.StatusBadRequest)
		return
	case err != nil:
		httputil.HandleError(w, "Could not decode request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	slot, err := parseSlotFromRequest(req.Slot)
	if err != nil {
		httputil.HandleError(w, "invalid slot: "+err.Error(), http.StatusBadRequest)
		return
	}
	parentHash, err := decodeHex32(req.ParentHash)
	if err != nil {
		httputil.HandleError(w, "invalid parent_hash: "+err.Error(), http.StatusBadRequest)
		return
	}
	pubkey, err := decodeHex48(req.Pubkey)
	if err != nil {
		httputil.HandleError(w, "invalid pubkey: "+err.Error(), http.StatusBadRequest)
		return
	}

	data := &builder.GetHeaderAuthData{
		Slot:       primitives.Slot(slot),
		ParentHash: parentHash,
		Pubkey:     pubkey,
	}
	root, err := data.HashTreeRoot()
	if err != nil {
		httputil.HandleError(w, "failed to compute signing root: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Sign with the private key corresponding to the request's pubkey. Keymanager looks up the secret key
	// for this pubkey and signs the root (no domain). Returns error if this client does not hold that key.
	sig, err := km.Sign(ctx, &validatorpb.SignRequest{
		PublicKey:       pubkey[:],
		SigningRoot:     root[:],
		SignatureDomain: []byte{}, // Not used for this signature type
	})
	if err != nil {
		httputil.HandleError(w, "failed to sign (ensure pubkey is a validator key managed by this client): "+err.Error(), http.StatusInternalServerError)
		return
	}
	if sig == nil {
		httputil.HandleError(w, "signature is nil", http.StatusInternalServerError)
		return
	}

	sigBytes := sig.Marshal()
	if len(sigBytes) != 96 {
		httputil.HandleError(w, "invalid signature length", http.StatusInternalServerError)
		return
	}

	httputil.WriteJson(w, &SignGetHeaderAuthResponse{
		Data: &SignGetHeaderAuthData{
			Signature: hexutil.Encode(sigBytes),
		},
	})
}

func parseSlotFromRequest(v interface{}) (uint64, error) {
	if v == nil {
		return 0, errors.New("slot is required")
	}
	switch t := v.(type) {
	case float64:
		if t < 0 || t != float64(uint64(t)) {
			return 0, errors.New("slot must be a non-negative integer")
		}
		return uint64(t), nil
	case string:
		if t == "" {
			return 0, errors.New("slot is required")
		}
		u, err := strconv.ParseUint(t, 10, 64)
		if err != nil {
			return 0, errors.New("slot must be a number")
		}
		return u, nil
	default:
		return 0, errors.New("slot must be a number or string")
	}
}

func decodeHex32(hexStr string) ([32]byte, error) {
	b, err := hexutil.Decode(hexStr)
	if err != nil {
		return [32]byte{}, err
	}
	return bytesutil.ToBytes32(b), nil
}

func decodeHex48(hexStr string) ([48]byte, error) {
	b, err := hexutil.Decode(hexStr)
	if err != nil {
		return [48]byte{}, err
	}
	return bytesutil.ToBytes48(b), nil
}
