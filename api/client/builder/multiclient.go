package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/OffchainLabs/prysm/v7/api"
	"github.com/OffchainLabs/prysm/v7/api/client"
	"github.com/OffchainLabs/prysm/v7/api/server/structs"
	"github.com/OffchainLabs/prysm/v7/consensus-types/interfaces"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/runtime/version"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// getExecutionPayloadBidPath is the Gloas getExecutionPayloadBid endpoint. The
// format verbs are slot (decimal), parent_hash, parent_root and proposer_pubkey
// (all 0x-prefixed hex).
const getExecutionPayloadBidPath = "/eth/v1/builder/execution_payload_bid/%d/%#x/%#x/%#x"

// postBeaconBlockPath is the Gloas submitSignedBeaconBlock endpoint.
const postBeaconBlockPath = "/eth/v1/builder/beacon_block"

// postBuilderPreferencesPath is the Gloas submitBuilderPreferences endpoint. The
// format verb is the validator pubkey (0x-prefixed hex).
const postBuilderPreferencesPath = "/eth/v1/builder/builder_preferences/%#x"

// MultiBuilderClient calls the post-ePBS (Gloas) Builder API endpoints. Unlike
// BuilderClient, which fronts a single MEV-Boost/relay endpoint, a
// MultiBuilderClient connects directly to one or more builders and fans requests
// out across all of them.
type MultiBuilderClient interface {
	// GetExecutionPayloadBid requests an execution payload bid from each builder
	// URL passed in and returns the bids that were served successfully, keyed by
	// the builder URL that served each bid (used for provenance so the signed
	// beacon block can later be submitted back to the selected builder). For each
	// builder the matching SignedRequestAuthV1 (by builder_url) is attached as the
	// optional request body. The builder URLs and auths originate from the
	// validator client (forwarded in the BlockRequest), not from beacon-node
	// configuration.
	GetExecutionPayloadBid(ctx context.Context, urls []string, auths []*ethpb.SignedRequestAuthV1, slot primitives.Slot, parentHash [32]byte, parentRoot [32]byte, pubkey [48]byte) (map[string]*ethpb.SignedExecutionPayloadBid, error)
	// SubmitBeaconBlock submits the signed beacon block back to the single
	// builder (identified by builderURL) whose bid was selected, so that builder
	// can reveal the corresponding execution payload envelope.
	SubmitBeaconBlock(ctx context.Context, builderURL string, sb interfaces.ReadOnlySignedBeaconBlock) error
	// SubmitBuilderPreferences submits the proposer's per-builder
	// BuilderPreferencesRequestV1 to each builder named in prefsByURL.
	// validatorPubkey is the proposer whose preferences these are (the path
	// parameter); each request body carries its own SignedRequestAuthV1 whose
	// message builder_url must match the target builder.
	SubmitBuilderPreferences(ctx context.Context, validatorPubkey [48]byte, prefsByURL map[string]*ethpb.BuilderPreferencesRequestV1) error
}

// executionPayloadBidResponse is the JSON envelope returned by
// getExecutionPayloadBid: {"version": "gloas", "data": SignedExecutionPayloadBid}.
type executionPayloadBidResponse struct {
	Version string                             `json:"version"`
	Data    *structs.SignedExecutionPayloadBid `json:"data"`
}

// MultiClientOpt is a functional option for the MultiClient type.
type MultiClientOpt func(*MultiClient)

// WithMultiClientSSZ enables SSZ encoding for Builder API requests/responses.
func WithMultiClientSSZ() MultiClientOpt {
	return func(c *MultiClient) {
		c.sszEnabled = true
	}
}

// WithMultiClientObserver registers a request observer (e.g. for logging).
func WithMultiClientObserver(m observer) MultiClientOpt {
	return func(c *MultiClient) {
		c.obvs = append(c.obvs, m)
	}
}

// MultiClient is a Builder API client that targets multiple builder endpoints.
// Post-ePBS the consensus client connects directly to builders (no MEV-Boost
// multiplexer), so requests such as getExecutionPayloadBid fan out across all of
// the configured builder URLs.
type MultiClient struct {
	hc         *http.Client
	obvs       []observer
	sszEnabled bool
}

var _ MultiBuilderClient = &MultiClient{}

// NewMultiClient constructs a MultiClient. It holds no builder URLs of its own:
// post-ePBS the set of builders to query is supplied per request by the
// validator client (forwarded in the BlockRequest), so each call to
// GetExecutionPayloadBid passes in the URLs to fan out to.
func NewMultiClient(opts ...MultiClientOpt) (*MultiClient, error) {
	c := &MultiClient{
		hc: &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// do issues a single request against the given builder base URL and validates
// that the response status matches expectedStatus. It mirrors Client.do but is
// kept separate so the multi-builder flow never shares mutable state with the
// single-endpoint client.
func (c *MultiClient) do(ctx context.Context, base *url.URL, method string, path string, body io.Reader, expectedStatus int, opts ...reqOption) (res []byte, header http.Header, err error) {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.do")
	defer func() {
		tracing.AnnotateError(span, err)
		span.End()
	}()

	u := base.ResolveReference(&url.URL{Path: path})

	span.SetAttributes(trace.StringAttribute("url", u.String()),
		trace.StringAttribute("method", method))

	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return
	}
	req.Header.Add("User-Agent", version.BuildData())
	for _, o := range opts {
		o(req)
	}
	for _, o := range c.obvs {
		if err = o.observe(req); err != nil {
			return
		}
	}
	r, err := c.hc.Do(req)
	if err != nil {
		return
	}
	defer func() {
		closeErr := r.Body.Close()
		if closeErr != nil {
			log.WithError(closeErr).Error("Failed to close response body")
		}
	}()
	if r.StatusCode != expectedStatus {
		err = unexpectedStatusErr(r, expectedStatus)
		return
	}
	res, err = io.ReadAll(io.LimitReader(r.Body, client.MaxBodySize))
	if err != nil {
		err = errors.Wrap(err, "error reading http response body from builder server")
		return
	}
	header = r.Header
	return
}

// GetExecutionPayloadBid requests an execution payload bid from each builder URL
// passed in for the given (slot, parentHash, parentRoot, proposer pubkey) tuple
// and returns the bids that were served successfully. For each builder, the
// SignedRequestAuthV1 whose message builder_url matches that URL is attached as
// the optional request body. Builders that return no bid (204) or error are
// skipped; bid selection is performed by the caller.
func (c *MultiClient) GetExecutionPayloadBid(ctx context.Context, urls []string, auths []*ethpb.SignedRequestAuthV1, slot primitives.Slot, parentHash [32]byte, parentRoot [32]byte, pubkey [48]byte) (map[string]*ethpb.SignedExecutionPayloadBid, error) {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.GetExecutionPayloadBid")
	defer span.End()

	path := fmt.Sprintf(getExecutionPayloadBidPath, slot, parentHash, parentRoot, pubkey)

	// Index the request auths by the builder URL they were signed for, so each
	// builder receives only its own auth as the optional request body.
	authByURL := make(map[string]*ethpb.SignedRequestAuthV1, len(auths))
	for _, a := range auths {
		if a != nil && a.Message != nil {
			authByURL[string(a.Message.BuilderUrl)] = a
		}
	}

	bids := make(map[string]*ethpb.SignedExecutionPayloadBid, len(urls))
	for _, host := range urls {
		base, err := urlForHost(host)
		if err != nil {
			log.WithError(err).WithField("builder", host).Warn("Invalid builder URL, skipping")
			continue
		}

		var body io.Reader
		opts := []reqOption{func(r *http.Request) { r.Header.Set("Accept", api.JsonMediaType) }}
		if auth, ok := authByURL[host]; ok {
			encoded, err := marshalRequestAuth(auth)
			if err != nil {
				log.WithError(err).WithField("builder", host).Warn("Failed to encode request auth, skipping")
				continue
			}
			body = bytes.NewReader(encoded)
			opts = append(opts, func(r *http.Request) { r.Header.Set("Content-Type", api.JsonMediaType) })
			log.WithField("builder", host).WithField("authBody", string(encoded)).Info("BHARATH: Sending getExecutionPayloadBid with request auth body")
		} else {
			log.WithField("builder", host).Info("BHARATH: Sending getExecutionPayloadBid with no request auth (no matching auth for builder)")
		}

		data, _, err := c.do(ctx, base, http.MethodPost, path, body, http.StatusOK, opts...)
		if err != nil {
			log.WithError(err).WithField("builder", base.String()).Debug("No execution payload bid from builder")
			continue
		}
		bid, err := parseExecutionPayloadBidResponse(data)
		if err != nil {
			log.WithError(err).WithField("builder", base.String()).Warn("Failed to parse execution payload bid from builder")
			continue
		}
		bids[base.String()] = bid
	}
	return bids, nil
}

// SubmitBeaconBlock submits the signed beacon block back to the builder
// identified by builderURL. We submit only to the builder whose bid was selected
// because only that builder holds the execution payload it needs to reveal.
func (c *MultiClient) SubmitBeaconBlock(ctx context.Context, builderURL string, sb interfaces.ReadOnlySignedBeaconBlock) error {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.SubmitBeaconBlock")
	defer span.End()

	base, err := urlForHost(builderURL)
	if err != nil {
		return errors.Wrapf(err, "invalid builder url %q", builderURL)
	}
	
	body, opt, err := c.buildBeaconBlockRequest(sb)
	if err != nil {
		return err
	}
	if _, _, err := c.do(ctx, base, http.MethodPost, postBeaconBlockPath, bytes.NewBuffer(body), http.StatusAccepted, opt); err != nil {
		return errors.Wrapf(err, "error submitting beacon block to builder %q", builderURL)
	}
	return nil
}

// SubmitBuilderPreferences submits the proposer's BuilderPreferencesRequestV1 to
// each builder named in prefsByURL. Returns nil only if every builder accepted
// (202); otherwise an aggregated error naming the builders that failed.
func (c *MultiClient) SubmitBuilderPreferences(ctx context.Context, validatorPubkey [48]byte, prefsByURL map[string]*ethpb.BuilderPreferencesRequestV1) error {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.SubmitBuilderPreferences")
	defer span.End()

	path := fmt.Sprintf(postBuilderPreferencesPath, validatorPubkey)
	jsonContentType := func(r *http.Request) { r.Header.Set("Content-Type", api.JsonMediaType) }

	var failures []string
	for host, req := range prefsByURL {
		base, err := urlForHost(host)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: invalid url: %v", host, err))
			continue
		}
		body, err := marshalBuilderPreferencesRequest(req)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", host, err))
			continue
		}
		if _, _, err := c.do(ctx, base, http.MethodPost, path, bytes.NewReader(body), http.StatusAccepted, jsonContentType); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", host, err))
			continue
		}
	}
	if len(failures) > 0 {
		return errors.Errorf("submit builder preferences failed for %d/%d builder(s): %s", len(failures), len(prefsByURL), strings.Join(failures, "; "))
	}
	return nil
}

// buildBeaconBlockRequest encodes the signed beacon block as the request body
// for submitSignedBeaconBlock, as SSZ when enabled and JSON otherwise.
func (c *MultiClient) buildBeaconBlockRequest(sb interfaces.ReadOnlySignedBeaconBlock) ([]byte, reqOption, error) {
	if c.sszEnabled {
		body, err := sb.MarshalSSZ()
		if err != nil {
			return nil, nil, errors.Wrap(err, "could not marshal SSZ for beacon block")
		}
		opt := func(r *http.Request) {
			r.Header.Set(api.VersionHeader, version.String(sb.Version()))
			r.Header.Set("Content-Type", api.OctetStreamMediaType)
		}
		return body, opt, nil
	}

	pb, err := sb.Proto()
	if err != nil {
		return nil, nil, errors.Wrap(err, "could not get beacon block proto")
	}
	gloasBlk, ok := pb.(*ethpb.SignedBeaconBlockGloas)
	if !ok {
		return nil, nil, errors.Errorf("expected a Gloas signed beacon block, got %T", pb)
	}
	jsonBlk, err := structs.SignedBeaconBlockGloasFromConsensus(gloasBlk)
	if err != nil {
		return nil, nil, errors.Wrap(err, "could not convert beacon block to json struct")
	}
	body, err := json.Marshal(jsonBlk)
	if err != nil {
		return nil, nil, errors.Wrap(err, "could not marshal beacon block to JSON")
	}
	opt := func(r *http.Request) {
		r.Header.Set(api.VersionHeader, version.String(sb.Version()))
		r.Header.Set("Content-Type", api.JsonMediaType)
	}
	return body, opt, nil
}

func parseExecutionPayloadBidResponse(data []byte) (*ethpb.SignedExecutionPayloadBid, error) {
	resp := &executionPayloadBidResponse{}
	if err := json.Unmarshal(data, resp); err != nil {
		return nil, errors.Wrap(err, "could not unmarshal execution payload bid response")
	}
	if resp.Data == nil {
		return nil, errors.New("execution payload bid response missing data")
	}
	return resp.Data.ToConsensus()
}

// signedRequestAuthJSON is the JSON wire form of a SignedRequestAuthV1 used as
// the optional getExecutionPayloadBid auth body. builder_url is a plain URL
// string (not base64/hex), slot is a decimal string, and signature is 0x-hex —
// matching the Builder API spec (examples/gloas/signed_request_auth.json). The
// generated proto JSON cannot be used directly: builder_url is a `bytes` field,
// so encoding/json would emit it base64-encoded.
type signedRequestAuthJSON struct {
	Message   requestAuthJSON `json:"message"`
	Signature string          `json:"signature"`
}

type requestAuthJSON struct {
	BuilderURL string `json:"builder_url"`
	Slot       string `json:"slot"`
}

// marshalRequestAuth encodes a SignedRequestAuthV1 to its Builder API JSON form.
func marshalRequestAuth(a *ethpb.SignedRequestAuthV1) ([]byte, error) {
	return json.Marshal(signedRequestAuthJSON{
		Message: requestAuthJSON{
			BuilderURL: string(a.Message.BuilderUrl),
			Slot:       fmt.Sprintf("%d", a.Message.Slot),
		},
		Signature: hexutil.Encode(a.Signature),
	})
}

// builderPreferencesRequestJSON is the JSON wire form of a
// BuilderPreferencesRequestV1 (submitBuilderPreferences body).
type builderPreferencesRequestJSON struct {
	Preferences builderPreferencesJSON `json:"preferences"`
	Auth        signedRequestAuthJSON  `json:"auth"`
}

type builderPreferencesJSON struct {
	MaxExecutionPayment string `json:"max_execution_payment"`
}

// marshalBuilderPreferencesRequest encodes a BuilderPreferencesRequestV1 to its
// Builder API JSON form (max_execution_payment as a decimal string; auth as in
// marshalRequestAuth — builder_url plain string, slot decimal, signature 0x-hex).
func marshalBuilderPreferencesRequest(req *ethpb.BuilderPreferencesRequestV1) ([]byte, error) {
	if req == nil || req.Preferences == nil || req.Auth == nil || req.Auth.Message == nil {
		return nil, errors.New("incomplete builder preferences request")
	}
	return json.Marshal(builderPreferencesRequestJSON{
		Preferences: builderPreferencesJSON{
			MaxExecutionPayment: fmt.Sprintf("%d", req.Preferences.MaxExecutionPayment),
		},
		Auth: signedRequestAuthJSON{
			Message: requestAuthJSON{
				BuilderURL: string(req.Auth.Message.BuilderUrl),
				Slot:       fmt.Sprintf("%d", req.Auth.Message.Slot),
			},
			Signature: hexutil.Encode(req.Auth.Signature),
		},
	})
}
