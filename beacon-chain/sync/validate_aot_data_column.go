package sync

import (
	"context"

	"github.com/OffchainLabs/prysm/v7/beacon-chain/p2p"
	eth "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
)

// validateAotDataColumn is the gossip validator for the aot_data_column_sidecar_{subnet_id} topic
// (blob streaming, Heze). It currently performs NO validation beyond decoding and type-checking the
// message: the structure is wired first, and the AotDataColumnVerifier (ticket validity, target-slot
// match, blob_info signature, structural/KZG checks, subnet, dedup) is added in a follow-up.
func (s *Service) validateAotDataColumn(_ context.Context, pid peer.ID, msg *pubsub.Message) (pubsub.ValidationResult, error) {
	// Always accept our own messages.
	if pid == s.cfg.p2p.PeerID() {
		return pubsub.ValidationAccept, nil
	}

	// Ignore messages during initial sync.
	if s.cfg.initialSync.Syncing() {
		return pubsub.ValidationIgnore, nil
	}

	// Reject messages with a nil topic.
	if msg.Topic == nil {
		return pubsub.ValidationReject, p2p.ErrInvalidTopic
	}

	// Decode the message, reject if it fails.
	m, err := s.decodePubsubMessage(msg)
	if err != nil {
		return pubsub.ValidationReject, err
	}

	// Reject messages that are not of the expected type.
	sidecar, ok := m.(*eth.AOTDataColumnSidecar)
	if !ok {
		return pubsub.ValidationReject, errWrongMessage
	}

	// TODO(blob-streaming): run the AotDataColumnVerifier here. No validation is performed yet.

	msg.ValidatorData = sidecar
	return pubsub.ValidationAccept, nil
}
