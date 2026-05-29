package sync

import (
	"context"
	"fmt"

	eth "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"
)

// aotColumnStasher is the subset of the AOT data column cache the subscriber needs: it stages a
// gossip-received AOT data column sidecar ahead of block time. Implemented by
// *das.AotDataColumnCache.
type aotColumnStasher interface {
	Stash(*eth.AOTDataColumnSidecar) error
}

// aotDataColumnSubscriber handles validated AOTDataColumnSidecar gossip messages by stashing them in
// the AOT data column cache, where they wait to be merged into the block-root store at inclusion time.
//
// Reconstruction and re-gossip of AOT columns are not implemented yet.
func (s *Service) aotDataColumnSubscriber(_ context.Context, msg proto.Message) error {
	sidecar, ok := msg.(*eth.AOTDataColumnSidecar)
	if !ok {
		return fmt.Errorf("unexpected AOT data column type: %T", msg)
	}

	if s.cfg.aotDataColumnCache == nil {
		return errors.New("AOT data column cache is not configured")
	}

	if err := s.cfg.aotDataColumnCache.Stash(sidecar); err != nil {
		return errors.Wrap(err, "stash AOT data column sidecar")
	}

	return nil
}
