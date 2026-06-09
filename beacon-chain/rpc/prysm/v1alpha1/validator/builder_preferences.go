package validator

import (
	"context"

	fieldparams "github.com/OffchainLabs/prysm/v7/config/fieldparams"
	"github.com/OffchainLabs/prysm/v7/encoding/bytesutil"
	"github.com/OffchainLabs/prysm/v7/monitoring/tracing/trace"
	ethpb "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// SubmitBuilderPreferences forwards a proposer's per-builder preferences to each
// target builder via the Gloas/ePBS Builder API submitBuilderPreferences
// endpoint. The validator client signs the preferences (one
// BuilderPreferencesRequestV1 per builder, each carrying a SignedRequestAuthV1)
// and the beacon node, which holds the builder client, performs the HTTP POSTs.
//
// The target builder for each entry is taken from its auth message data (the
// builder URL), mirroring how getExecutionPayloadBid auths are routed.
func (vs *Server) SubmitBuilderPreferences(
	ctx context.Context,
	req *ethpb.SubmitBuilderPreferencesRequest,
) (*emptypb.Empty, error) {
	ctx, span := trace.StartSpan(ctx, "ValidatorServer.SubmitBuilderPreferences")
	defer span.End()

	if req == nil || len(req.Preferences) == 0 {
		return nil, status.Errorf(codes.InvalidArgument, "builder preferences request is empty")
	}
	if len(req.ValidatorPubkey) != fieldparams.BLSPubkeyLength {
		return nil, status.Errorf(codes.InvalidArgument,
			"validator_pubkey must be %d bytes (got %d)", fieldparams.BLSPubkeyLength, len(req.ValidatorPubkey))
	}

	// Route each preferences entry to its target builder by the auth message
	// data (the builder URL). Duplicate URLs keep the last entry.
	prefsByURL := make(map[string]*ethpb.BuilderPreferencesRequestV1, len(req.Preferences))
	for _, pref := range req.Preferences {
		if pref == nil || pref.Auth == nil || pref.Auth.Message == nil {
			return nil, status.Errorf(codes.InvalidArgument, "builder preferences entry is missing its auth")
		}
		url := string(pref.Auth.Message.Data)
		if url == "" {
			return nil, status.Errorf(codes.InvalidArgument, "builder preferences entry has an empty builder url (auth data)")
		}
		prefsByURL[url] = pref
	}

	validatorPubkey := bytesutil.ToBytes48(req.ValidatorPubkey)
	if err := vs.BlockBuilder.SubmitBuilderPreferences(ctx, validatorPubkey, prefsByURL); err != nil {
		return nil, status.Errorf(codes.Internal, "could not submit builder preferences: %v", err)
	}

	log.WithFields(logrus.Fields{
		"validatorPubkey": bytesutil.Trunc(req.ValidatorPubkey),
		"builders":        len(prefsByURL),
	}).Debug("Submitted builder preferences")
	return &emptypb.Empty{}, nil
}
