package builder

import (
	"context"
	"math/big"
	"strings"
	"sync"

	"github.com/OffchainLabs/prysm/v7/consensus-types/interfaces"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	v1 "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// builderClientEntry wraps a BuilderClient with its configuration.
type builderClientEntry struct {
	client BuilderClient
	config BuilderEntry
}

// MultiBuilderClient wraps multiple builder clients and implements the BuilderClient interface.
// It calls all builders in parallel and handles response aggregation.
type MultiBuilderClient struct {
	clients    []builderClientEntry
	sszEnabled bool
}

// NewMultiBuilderClient creates a new MultiBuilderClient from a BuilderWhitelist.
// Each builder in the whitelist will have its own underlying Client.
func NewMultiBuilderClient(whitelist *BuilderWhitelist, opts ...ClientOpt) (*MultiBuilderClient, error) {
	if whitelist == nil || len(whitelist.Builders) == 0 {
		return nil, errors.New("builder whitelist is empty or nil")
	}

	mc := &MultiBuilderClient{
		clients: make([]builderClientEntry, 0, len(whitelist.Builders)),
	}

	// Check if SSZ is enabled from opts
	for _, opt := range opts {
		// Create a temporary client to check the options
		tempClient := &Client{}
		opt(tempClient)
		if tempClient.sszEnabled {
			mc.sszEnabled = true
			break
		}
	}

	for _, entry := range whitelist.Builders {
		client, err := NewClient(entry.URL, opts...)
		if err != nil {
			log.WithError(err).WithField("url", entry.URL).Warn("Failed to create builder client, skipping")
			continue
		}
		mc.clients = append(mc.clients, builderClientEntry{
			client: client,
			config: entry,
		})
	}

	if len(mc.clients) == 0 {
		return nil, errors.New("no valid builder clients could be created")
	}

	log.WithField("count", len(mc.clients)).Info("Created multi-builder client")
	return mc, nil
}

// NodeURL returns a comma-separated list of all builder URLs.
func (mc *MultiBuilderClient) NodeURL() string {
	urls := make([]string, 0, len(mc.clients))
	for _, entry := range mc.clients {
		urls = append(urls, entry.client.NodeURL())
	}
	return strings.Join(urls, ",")
}

// bidResult holds the result of a GetHeader call from a single builder.
type bidResult struct {
	bid    SignedBid
	config BuilderEntry
	err    error
}

// GetHeader calls all builders in parallel and returns the bid with the highest value
// that meets the minimum bid requirement for that builder.
func (mc *MultiBuilderClient) GetHeader(ctx context.Context, slot primitives.Slot, parentHash [32]byte, pubkey [48]byte) (SignedBid, error) {
	ctx, span := trace.StartSpan(ctx, "multiBuilder.GetHeader")
	defer span.End()

	if len(mc.clients) == 0 {
		return nil, errors.New("no builder clients available")
	}

	// Create a channel to collect results
	results := make(chan bidResult, len(mc.clients))
	var wg sync.WaitGroup

	// Call all builders in parallel
	for _, entry := range mc.clients {
		wg.Add(1)
		go func(e builderClientEntry) {
			defer wg.Done()
			bid, err := e.client.GetHeader(ctx, slot, parentHash, pubkey)
			results <- bidResult{
				bid:    bid,
				config: e.config,
				err:    err,
			}
		}(entry)
	}

	// Close the results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and find the best bid
	var bestBid SignedBid
	var bestValue primitives.Wei
	var successCount, errorCount int

	for result := range results {
		if result.err != nil {
			errorCount++
			// Log the error but continue checking other builders
			if !errors.Is(result.err, ErrNoContent) {
				log.WithError(result.err).WithField("url", result.config.URL).Debug("Builder GetHeader failed")
			}
			continue
		}

		if result.bid == nil || result.bid.IsNil() {
			continue
		}

		successCount++
		msg, err := result.bid.Message()
		if err != nil {
			log.WithError(err).WithField("url", result.config.URL).Debug("Failed to get bid message")
			continue
		}

		bidValue := msg.Value()

		// Check if bid meets the minimum bid requirement for this builder
		// Convert min_bid from Gwei to Wei (1 Gwei = 1e9 Wei)
		minBidWei := new(big.Int).Mul(big.NewInt(int64(result.config.MinBid)), big.NewInt(1e9))
		bidValueBigInt := primitives.WeiToBigInt(bidValue)
		if bidValueBigInt.Cmp(minBidWei) < 0 {
			log.WithFields(logrus.Fields{
				"url":     result.config.URL,
				"bid":     bidValueBigInt.String(),
				"min_bid": minBidWei.String(),
			}).Debug("Bid below minimum threshold for builder")
			continue
		}

		// Check if this bid is better than our current best
		bestValueBigInt := primitives.WeiToBigInt(bestValue)
		if bestBid == nil || bidValueBigInt.Cmp(bestValueBigInt) > 0 {
			bestBid = result.bid
			bestValue = bidValue
		}
	}

	span.SetAttributes(
		trace.Int64Attribute("success_count", int64(successCount)),
		trace.Int64Attribute("error_count", int64(errorCount)),
	)

	if bestBid == nil {
		// All builders either failed or returned no content
		if errorCount == len(mc.clients) {
			return nil, ErrNoContent
		}
		return nil, ErrNoContent
	}

	log.WithFields(logrus.Fields{
		"slot":          slot,
		"best_bid_gwei": primitives.WeiToGwei(bestValue),
		"builders_ok":   successCount,
		"builders_err":  errorCount,
	}).Debug("Selected best bid from builders")

	return bestBid, nil
}

// registerResult holds the result of a RegisterValidator call from a single builder.
type registerResult struct {
	url string
	err error
}

// RegisterValidator calls all builders in parallel to register the validators.
// It succeeds if at least one builder successfully registers the validators.
func (mc *MultiBuilderClient) RegisterValidator(ctx context.Context, svr []*ethpb.SignedValidatorRegistrationV1) error {
	ctx, span := trace.StartSpan(ctx, "multiBuilder.RegisterValidator")
	defer span.End()
	span.SetAttributes(trace.Int64Attribute("num_registrations", int64(len(svr))))

	if len(mc.clients) == 0 {
		return errors.New("no builder clients available")
	}

	// Create a channel to collect results
	results := make(chan registerResult, len(mc.clients))
	var wg sync.WaitGroup

	// Call all builders in parallel
	for _, entry := range mc.clients {
		wg.Add(1)
		go func(e builderClientEntry) {
			defer wg.Done()
			err := e.client.RegisterValidator(ctx, svr)
			results <- registerResult{
				url: e.config.URL,
				err: err,
			}
		}(entry)
	}

	// Close the results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results - we succeed if at least one builder succeeds
	var successCount, errorCount int
	var lastError error

	for result := range results {
		if result.err != nil {
			errorCount++
			lastError = result.err
			log.WithError(result.err).WithField("url", result.url).Debug("Builder RegisterValidator failed")
		} else {
			successCount++
			log.WithField("url", result.url).Debug("Builder RegisterValidator succeeded")
		}
	}

	span.SetAttributes(
		trace.Int64Attribute("success_count", int64(successCount)),
		trace.Int64Attribute("error_count", int64(errorCount)),
	)

	if successCount == 0 {
		tracing.AnnotateError(span, lastError)
		return errors.Wrap(lastError, "all builders failed to register validator(s)")
	}

	log.WithFields(logrus.Fields{
		"registrations": len(svr),
		"builders_ok":   successCount,
		"builders_err":  errorCount,
	}).Debug("Registered validator(s) with builders")

	return nil
}

// submitResult holds the result of a SubmitBlindedBlock call from a single builder.
type submitResult struct {
	url        string
	execData   interfaces.ExecutionData
	blobBundle v1.BlobsBundler
	err        error
}

// SubmitBlindedBlock calls all builders in parallel and returns the first successful response.
// Since all builders should return the same payload for the same blinded block,
// we return the first successful response.
func (mc *MultiBuilderClient) SubmitBlindedBlock(ctx context.Context, sb interfaces.ReadOnlySignedBeaconBlock) (interfaces.ExecutionData, v1.BlobsBundler, error) {
	ctx, span := trace.StartSpan(ctx, "multiBuilder.SubmitBlindedBlock")
	defer span.End()

	if len(mc.clients) == 0 {
		return nil, nil, errors.New("no builder clients available")
	}

	// Create a channel to collect results
	results := make(chan submitResult, len(mc.clients))
	var wg sync.WaitGroup

	// Call all builders in parallel
	for _, entry := range mc.clients {
		wg.Add(1)
		go func(e builderClientEntry) {
			defer wg.Done()
			execData, blobBundle, err := e.client.SubmitBlindedBlock(ctx, sb)
			results <- submitResult{
				url:        e.config.URL,
				execData:   execData,
				blobBundle: blobBundle,
				err:        err,
			}
		}(entry)
	}

	// Close the results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results - return the first successful response
	var successCount, errorCount int
	var lastError error
	var execData interfaces.ExecutionData
	var blobBundle v1.BlobsBundler

	for result := range results {
		if result.err != nil {
			errorCount++
			lastError = result.err
			log.WithError(result.err).WithField("url", result.url).Debug("Builder SubmitBlindedBlock failed")
		} else {
			successCount++
			// Use the first successful response
			if execData == nil {
				execData = result.execData
				blobBundle = result.blobBundle
			}
			log.WithField("url", result.url).Debug("Builder SubmitBlindedBlock succeeded")
		}
	}

	span.SetAttributes(
		trace.Int64Attribute("success_count", int64(successCount)),
		trace.Int64Attribute("error_count", int64(errorCount)),
	)

	if successCount == 0 {
		tracing.AnnotateError(span, lastError)
		return nil, nil, errors.Wrap(lastError, "all builders failed to submit blinded block")
	}

	log.WithFields(logrus.Fields{
		"builders_ok":  successCount,
		"builders_err": errorCount,
	}).Debug("Submitted blinded block to builders")

	return execData, blobBundle, nil
}

// SubmitBlindedBlockPostFulu calls all builders in parallel for post-Fulu blocks.
// It succeeds if at least one builder accepts the block.
func (mc *MultiBuilderClient) SubmitBlindedBlockPostFulu(ctx context.Context, sb interfaces.ReadOnlySignedBeaconBlock) error {
	ctx, span := trace.StartSpan(ctx, "multiBuilder.SubmitBlindedBlockPostFulu")
	defer span.End()

	if len(mc.clients) == 0 {
		return errors.New("no builder clients available")
	}

	// Create a channel to collect results
	results := make(chan registerResult, len(mc.clients))
	var wg sync.WaitGroup

	// Call all builders in parallel
	for _, entry := range mc.clients {
		wg.Add(1)
		go func(e builderClientEntry) {
			defer wg.Done()
			err := e.client.SubmitBlindedBlockPostFulu(ctx, sb)
			results <- registerResult{
				url: e.config.URL,
				err: err,
			}
		}(entry)
	}

	// Close the results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results - we succeed if at least one builder succeeds
	var successCount, errorCount int
	var lastError error

	for result := range results {
		if result.err != nil {
			errorCount++
			lastError = result.err
			log.WithError(result.err).WithField("url", result.url).Debug("Builder SubmitBlindedBlockPostFulu failed")
		} else {
			successCount++
			log.WithField("url", result.url).Debug("Builder SubmitBlindedBlockPostFulu succeeded")
		}
	}

	span.SetAttributes(
		trace.Int64Attribute("success_count", int64(successCount)),
		trace.Int64Attribute("error_count", int64(errorCount)),
	)

	if successCount == 0 {
		tracing.AnnotateError(span, lastError)
		return errors.Wrap(lastError, "all builders failed to submit blinded block post-Fulu")
	}

	log.WithFields(logrus.Fields{
		"builders_ok":  successCount,
		"builders_err": errorCount,
	}).Debug("Submitted blinded block post-Fulu to builders")

	return nil
}

// Status checks the status of all builders. Returns nil if at least one builder is healthy.
func (mc *MultiBuilderClient) Status(ctx context.Context) error {
	ctx, span := trace.StartSpan(ctx, "multiBuilder.Status")
	defer span.End()

	if len(mc.clients) == 0 {
		return errors.New("no builder clients available")
	}

	// Create a channel to collect results
	results := make(chan registerResult, len(mc.clients))
	var wg sync.WaitGroup

	// Call all builders in parallel
	for _, entry := range mc.clients {
		wg.Add(1)
		go func(e builderClientEntry) {
			defer wg.Done()
			err := e.client.Status(ctx)
			results <- registerResult{
				url: e.config.URL,
				err: err,
			}
		}(entry)
	}

	// Close the results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results - we succeed if at least one builder is healthy
	var successCount, errorCount int
	var lastError error

	for result := range results {
		if result.err != nil {
			errorCount++
			lastError = result.err
			log.WithError(result.err).WithField("url", result.url).Debug("Builder status check failed")
		} else {
			successCount++
		}
	}

	span.SetAttributes(
		trace.Int64Attribute("healthy_count", int64(successCount)),
		trace.Int64Attribute("unhealthy_count", int64(errorCount)),
	)

	if successCount == 0 {
		tracing.AnnotateError(span, lastError)
		return errors.Wrap(lastError, "all builders are unhealthy")
	}

	return nil
}

// Verify that MultiBuilderClient implements BuilderClient.
var _ BuilderClient = (*MultiBuilderClient)(nil)
