package validator

import (
	"context"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/cache"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/state"
	"github.com/OffchainLabs/prysm/v7/config/params"
	consensusblocks "github.com/OffchainLabs/prysm/v7/consensus-types/blocks"
	"github.com/OffchainLabs/prysm/v7/consensus-types/interfaces"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	"github.com/OffchainLabs/prysm/v7/crypto/bls/common"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// setExecutionPayloadBid selects the best execution payload bid for the block,
// comparing the highest Builder-API bid, the highest P2P bid, and the local
// (self-build) block value, and sets the winner on the block. When
// selfBuildOnly is true the external sources are ignored and the proposer
// self-builds. The returned BidType records which bid was selected.
func (vs *Server) setExecutionPayloadBid(
	ctx context.Context,
	sBlk interfaces.SignedBeaconBlock,
	local *consensusblocks.GetPayloadResponse,
	selfBuildOnly bool,
	builderBids map[string]*ethpb.SignedExecutionPayloadBid,
) (*cache.BidType, error) {
	_, span := trace.StartSpan(ctx, "ProposerServer.setExecutionPayloadBid")
	defer span.End()

	if local == nil || local.ExecutionData == nil {
		return nil, errors.New("local execution payload is nil")
	}

	localValue := primitives.WeiToGwei(local.Bid)

	// Find the highest-value external bid across the Builder-API bids and the
	// P2P bid cache.
	var bestExternal *cache.BidType
	var bestExternalValue primitives.Gwei
	if !selfBuildOnly {
		if url, bid, value, ok := highestBuilderApiBid(builderBids); ok {
			bestExternal = &cache.BidType{ExecutionPayloadBid: bid, IsBuilderApiBid: true, BuilderUrl: url}
			bestExternalValue = value
			log.WithFields(logrus.Fields{
				"url": url,
				"bid": bid,
				"value": value,
			}).Info("BHARATH: Highest builder API bid")
		}
		if cached := vs.cachedP2PBid(sBlk, local); cached != nil {
			log.WithFields(logrus.Fields{
				"cached": cached,
			}).Info("BHARATH: Highest cached P2P bid")
			if bestExternal == nil || cached.Message.Value > bestExternalValue {
				bestExternal = &cache.BidType{ExecutionPayloadBid: cached}
				bestExternalValue = cached.Message.Value
			}
		}
	}

	// Use the external bid only if it strictly exceeds the local block value;
	// ties favour self-building.
	if bestExternal != nil && bestExternalValue > localValue {
		if err := sBlk.SetSignedExecutionPayloadBid(bestExternal.ExecutionPayloadBid); err != nil {
			return nil, errors.Wrap(err, "could not set selected execution payload bid")
		}
		log.WithFields(logrus.Fields{
			"slot":            sBlk.Block().Slot(),
			"isBuilderApiBid": bestExternal.IsBuilderApiBid,
			"builderUrl":      bestExternal.BuilderUrl,
			"externalValue":   bestExternalValue,
			"localValue":      localValue,
		}).Info("BHARATH: Using external execution payload bid over self-build")
		return bestExternal, nil
	}

	// Fall back to self-build bid.
	bid, err := vs.createSelfBuildExecutionPayloadBid(local, sBlk.Block())
	if err != nil {
		return nil, errors.Wrap(err, "could not create execution payload bid")
	}

	// Per spec, self-build bids must use G2 point-at-infinity as the signature.
	signedBid := &ethpb.SignedExecutionPayloadBid{
		Message:   bid,
		Signature: common.InfiniteSignature[:],
	}
	if err := sBlk.SetSignedExecutionPayloadBid(signedBid); err != nil {
		return nil, errors.Wrap(err, "could not set signed execution payload bid")
	}

	return &cache.BidType{ExecutionPayloadBid: signedBid, SelfBuild: true}, nil
}

// highestBuilderApiBid returns the Builder-API bid with the greatest
// execution_payment + value across all bids, along with the builder URL that
// served it and the summed value. ok is false when there are no usable bids.
// Ties are broken by map iteration order (non-deterministic; revisit later).
func highestBuilderApiBid(bids map[string]*ethpb.SignedExecutionPayloadBid) (string, *ethpb.SignedExecutionPayloadBid, primitives.Gwei, bool) {
	var bestURL string
	var bestBid *ethpb.SignedExecutionPayloadBid
	var bestValue primitives.Gwei
	found := false
	for url, bid := range bids {
		if bid == nil || bid.Message == nil {
			continue
		}
		value := bid.Message.ExecutionPayment + bid.Message.Value
		if !found || value > bestValue {
			bestURL, bestBid, bestValue, found = url, bid, value, true
		}
	}
	return bestURL, bestBid, bestValue, found
}

// getBuilderExecutionPayloadBids queries the configured external builders for
// execution payload bids for this block, keyed by the builder URL that served
// each bid. Returns nil if no builders are configured or the request fails.
func (vs *Server) getBuilderExecutionPayloadBids(ctx context.Context, sBlk interfaces.SignedBeaconBlock, head state.BeaconState, local *consensusblocks.GetPayloadResponse) map[string]*ethpb.SignedExecutionPayloadBid {
	if vs.BlockBuilder == nil || local == nil || local.ExecutionData == nil {
		return nil
	}
	var parentHash [32]byte
	copy(parentHash[:], local.ExecutionData.ParentHash())
	parentRoot := sBlk.Block().ParentRoot()
	pubkey := head.PubkeyAtIndex(sBlk.Block().ProposerIndex())

	bids, err := vs.BlockBuilder.GetExecutionPayloadBid(ctx, sBlk.Block().Slot(), parentHash, parentRoot, pubkey)
	if err != nil {
		log.WithError(err).Debug("Could not get execution payload bids from builders")
		return nil
	}
	log.WithField("count", len(bids)).Info("Fetched execution payload bids from builders")
	return bids
}

// cachedP2PBid returns the highest cached P2P bid for this block, or nil if none
// exists or the bid cache is not configured.
func (vs *Server) cachedP2PBid(
	sBlk interfaces.SignedBeaconBlock,
	local *consensusblocks.GetPayloadResponse,
) *ethpb.SignedExecutionPayloadBid {
	if vs.HighestBidCache == nil {
		return nil
	}
	ed := local.ExecutionData
	var parentHash [32]byte
	copy(parentHash[:], ed.ParentHash())
	cached, ok := vs.HighestBidCache.Get(sBlk.Block().Slot(), parentHash, sBlk.Block().ParentRoot())
	if !ok {
		return nil
	}
	return cached
}

// createSelfBuildExecutionPayloadBid creates an ExecutionPayloadBid for self-building,
// where the proposer acts as its own builder. Per spec, the bid value must be zero
// and the builder index must be BUILDER_INDEX_SELF_BUILD.
func (vs *Server) createSelfBuildExecutionPayloadBid(
	local *consensusblocks.GetPayloadResponse,
	block interfaces.ReadOnlyBeaconBlock,
) (*ethpb.ExecutionPayloadBid, error) {
	ed := local.ExecutionData
	if ed == nil || ed.IsNil() {
		return nil, errors.New("execution data is nil")
	}

	parentBlockRoot := block.ParentRoot()
	executionRequestsRoot, err := local.ExecutionRequests.HashTreeRoot()
	if err != nil {
		return nil, errors.Wrap(err, "could not compute execution requests root")
	}
	return &ethpb.ExecutionPayloadBid{
		ParentBlockHash:       ed.ParentHash(),
		ParentBlockRoot:       bytesutil.SafeCopyBytes(parentBlockRoot[:]),
		BlockHash:             ed.BlockHash(),
		PrevRandao:            ed.PrevRandao(),
		FeeRecipient:          ed.FeeRecipient(),
		GasLimit:              ed.GasLimit(),
		BuilderIndex:          params.BeaconConfig().BuilderIndexSelfBuild,
		Slot:                  block.Slot(),
		Value:                 0,
		ExecutionPayment:      0,
		BlobKzgCommitments:    local.BlobsBundler.GetKzgCommitments(),
		ExecutionRequestsRoot: executionRequestsRoot[:],
	}, nil
}
