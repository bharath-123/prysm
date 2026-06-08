package das

import (
	"github.com/OffchainLabs/prysm/v7/beacon-chain/core/peerdas"
	enginev1 "github.com/OffchainLabs/prysm/v7/proto/engine/v1"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/pkg/errors"
)

// AvailableAotBlobCommitments returns the AOT blob versioned hashes for the bundles
// this node fully custodies, shaped as engine PayloadAttributes VersionedHashLists ready
// to set on PayloadAttributes.AvailableAotBlobCommitments. The node's required custody
// columns are derived from (nodeID, cgc) via peerdas.Info; a bundle contributes only when
// all of those columns have been stashed for it (see AvailableAotBlobVersionedHashes).
//
// This tells the EL, at FCU/build time, which AOT blobs are available for inclusion. An
// empty result is valid and means no AOT bundle is yet fully custodied.
func (c *AotDataColumnCache) AvailableAotBlobCommitments(nodeID enode.ID, cgc uint64) ([]*enginev1.VersionedHashList, error) {
	info, _, err := peerdas.Info(nodeID, cgc)
	if err != nil {
		return nil, errors.Wrap(err, "peerdas info")
	}

	hashLists := c.AvailableAotBlobVersionedHashes(info.CustodyColumns)
	out := make([]*enginev1.VersionedHashList, len(hashLists))
	for i, hashes := range hashLists {
		out[i] = &enginev1.VersionedHashList{VersionedHashes: hashes}
	}
	return out, nil
}
