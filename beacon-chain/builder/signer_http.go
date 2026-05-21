package builder

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const (
	signGetHeaderAuthPath = "/eth/v1/validator/sign_get_header_auth"
	signerHTTPTimeout     = 5 * time.Second
)

// signGetHeaderAuthRequest is the JSON body sent to the validator sign endpoint.
type signGetHeaderAuthRequest struct {
	Slot       uint64 `json:"slot"`
	ParentHash string `json:"parent_hash"`
	Pubkey     string `json:"pubkey"`
}

// signGetHeaderAuthResponse is the JSON response from the validator.
type signGetHeaderAuthResponse struct {
	Data *struct {
		Signature string `json:"signature"`
	} `json:"data"`
}

// NewHTTPGetHeaderAuthSigner returns a GetHeaderAuthSigner that POSTs (slot, parentHash, pubkey)
// to the given base URL (e.g. http://localhost:7500) and returns the signature from the response.
// The endpoint must implement POST /eth/v1/validator/sign_get_header_auth.
func NewHTTPGetHeaderAuthSigner(baseURL string) GetHeaderAuthSigner {
	baseURL = strings.TrimSuffix(baseURL, "/")
	url := baseURL + signGetHeaderAuthPath
	client := &http.Client{Timeout: signerHTTPTimeout}
	return func(ctx context.Context, slot primitives.Slot, parentHash [32]byte, pubkey [48]byte) []byte {
		log.WithFields(map[string]any{
			"slot":   slot,
			"url":    url,
			"pubkey": hexutil.Encode(pubkey[:4]),
		}).Info("Calling validator for X-Request-Auth signature")
		reqBody := signGetHeaderAuthRequest{
			Slot:       uint64(slot),
			ParentHash: hexutil.Encode(parentHash[:]),
			Pubkey:     hexutil.Encode(pubkey[:]),
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			log.WithError(err).Warn("Failed to marshal sign_get_header_auth request")
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.WithError(err).Warn("Failed to create sign_get_header_auth request")
			return nil
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			log.WithError(err).Warn("Failed to call get-header auth signer")
			return nil
		}
		defer func() {
			if closeErr := resp.Body.Close(); closeErr != nil {
				log.WithError(closeErr).Debug("Failed to close signer response body")
			}
		}()
		if resp.StatusCode != http.StatusOK {
			log.WithField("status", resp.StatusCode).Warn("Get-header auth signer returned non-OK status")
			return nil
		}
		var out signGetHeaderAuthResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			log.WithError(err).Warn("Failed to decode sign_get_header_auth response")
			return nil
		}
		if out.Data == nil || out.Data.Signature == "" {
			log.Warn("Get-header auth signer returned empty signature")
			return nil
		}
		sigBytes, err := hex.DecodeString(strings.TrimPrefix(out.Data.Signature, "0x"))
		if err != nil {
			log.WithError(err).Warn("Failed to decode signature hex from get-header auth signer")
			return nil
		}
		if len(sigBytes) != 96 {
			log.WithField("len", len(sigBytes)).Warn("Get-header auth signer returned invalid signature length")
			return nil
		}
		log.WithField("slot", slot).Info("Received X-Request-Auth signature from validator")
		return sigBytes
	}
}
