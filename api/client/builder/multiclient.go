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
	"github.com/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// getExecutionPayloadBidPath is the Gloas getExecutionPayloadBid endpoint. The
// format verbs are slot (decimal), parent_hash, parent_root and proposer_pubkey
// (all 0x-prefixed hex).
const getExecutionPayloadBidPath = "/eth/v1/builder/execution_payload_bid/%d/%#x/%#x/%#x"

// postBeaconBlockPath is the Gloas submitSignedBeaconBlock endpoint.
const postBeaconBlockPath = "/eth/v1/builder/beacon_block"

// MultiBuilderClient calls the post-ePBS (Gloas) Builder API endpoints. Unlike
// BuilderClient, which fronts a single MEV-Boost/relay endpoint, a
// MultiBuilderClient connects directly to one or more builders and fans requests
// out across all of them.
type MultiBuilderClient interface {
	// NodeURLs returns the configured builder endpoint URLs.
	NodeURLs() []string
	// GetExecutionPayloadBid requests an execution payload bid from each
	// configured builder and returns the bids that were served successfully,
	// keyed by the builder URL that served each bid (used for provenance so the
	// signed beacon block can later be submitted back to the selected builder).
	GetExecutionPayloadBid(ctx context.Context, slot primitives.Slot, parentHash [32]byte, parentRoot [32]byte, pubkey [48]byte) (map[string]*ethpb.SignedExecutionPayloadBid, error)
	// Status checks the status endpoint of every configured builder.
	Status(ctx context.Context) error
	// SubmitBeaconBlock submits the signed beacon block back to the single
	// builder (identified by builderURL) whose bid was selected, so that builder
	// can reveal the corresponding execution payload envelope.
	SubmitBeaconBlock(ctx context.Context, builderURL string, sb interfaces.ReadOnlySignedBeaconBlock) error
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
	hc          *http.Client
	builderURLs []*url.URL
	obvs        []observer
	sszEnabled  bool
}

var _ MultiBuilderClient = &MultiClient{}

// NewMultiClient constructs a MultiClient targeting the provided builder hosts.
// Each host can be a URL string or a `host:port` pair (assumed http), matching
// the parsing rules used by the single-endpoint Client.
func NewMultiClient(hosts []string, opts ...MultiClientOpt) (*MultiClient, error) {
	urls := make([]*url.URL, 0, len(hosts))
	for _, h := range hosts {
		u, err := urlForHost(h)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}
	c := &MultiClient{
		hc:          &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
		builderURLs: urls,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// NodeURLs returns the configured builder endpoint URLs.
func (c *MultiClient) NodeURLs() []string {
	hosts := make([]string, 0, len(c.builderURLs))
	for _, u := range c.builderURLs {
		hosts = append(hosts, u.String())
	}
	return hosts
}

// NodeURL returns the configured builder endpoint URLs as a comma-separated
// string, for logging.
func (c *MultiClient) NodeURL() string {
	return strings.Join(c.NodeURLs(), ",")
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

// GetExecutionPayloadBid requests an execution payload bid from each configured
// builder for the given (slot, parentHash, parentRoot, proposer pubkey) tuple and
// returns the bids that were served successfully. Builders that return no bid
// (204) or error are skipped; bid selection is performed by the caller.
func (c *MultiClient) GetExecutionPayloadBid(ctx context.Context, slot primitives.Slot, parentHash [32]byte, parentRoot [32]byte, pubkey [48]byte) (map[string]*ethpb.SignedExecutionPayloadBid, error) {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.GetExecutionPayloadBid")
	defer span.End()

	path := fmt.Sprintf(getExecutionPayloadBidPath, slot, parentHash, parentRoot, pubkey)
	acceptJSON := func(r *http.Request) {
		r.Header.Set("Accept", api.JsonMediaType)
	}

	bids := make(map[string]*ethpb.SignedExecutionPayloadBid, len(c.builderURLs))
	for _, base := range c.builderURLs {
		data, _, err := c.do(ctx, base, http.MethodPost, path, nil, http.StatusOK, acceptJSON)
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

// Status queries the status endpoint of every configured builder. It returns
// nil only if all builders respond healthy; otherwise it returns an error
// summarizing which builders failed and why.
func (c *MultiClient) Status(ctx context.Context) error {
	ctx, span := trace.StartSpan(ctx, "builder.multiclient.Status")
	defer span.End()

	acceptJSON := func(r *http.Request) {
		r.Header.Set("Accept", api.JsonMediaType)
	}
	var failures []string
	for _, base := range c.builderURLs {
		if _, _, err := c.do(ctx, base, http.MethodGet, getStatus, nil, http.StatusOK, acceptJSON); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", base.String(), err))
		}
	}
	if len(failures) > 0 {
		return errors.Errorf("status check failed for %d/%d builder(s): %s", len(failures), len(c.builderURLs), strings.Join(failures, "; "))
	}
	return nil
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
