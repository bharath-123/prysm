package builder

import (
	"strings"

	"github.com/OffchainLabs/prysm/v7/api/client/builder"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/blockchain"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/cache"
	"github.com/OffchainLabs/prysm/v7/beacon-chain/db"
	"github.com/OffchainLabs/prysm/v7/cmd/beacon-chain/flags"
	"github.com/urfave/cli/v2"
)

type Option func(s *Service) error

// FlagOptions for builder service flag configurations.
func FlagOptions(c *cli.Context) ([]Option, error) {
	endpoint := c.String(flags.MevRelayEndpoint.Name)
	sszEnabled := c.Bool(flags.EnableBuilderSSZ.Name)
	var client *builder.Client
	if endpoint != "" {
		var opts []builder.ClientOpt
		if sszEnabled {
			log.Info("Using APIs with SSZ enabled")
			opts = append(opts, builder.WithSSZ())
		}
		var err error
		client, err = builder.NewClient(endpoint, opts...)
		if err != nil {
			return nil, err
		}
	}
	opts := []Option{
		WithBuilderClient(client),
	}

	// Gloas (post-ePBS) builders are connected to directly via a multi-builder client.
	if hosts := splitBuilderURLs(c.String(flags.BuilderURLs.Name)); len(hosts) > 0 {
		var mopts []builder.MultiClientOpt
		if sszEnabled {
			mopts = append(mopts, builder.WithMultiClientSSZ())
		}
		multiClient, err := builder.NewMultiClient(hosts, mopts...)
		if err != nil {
			return nil, err
		}
		opts = append(opts, WithMultiBuilderClient(multiClient))
	}
	return opts, nil
}

// splitBuilderURLs parses a comma-separated list of builder URLs, trimming
// whitespace and dropping empty entries.
func splitBuilderURLs(raw string) []string {
	parts := strings.Split(raw, ",")
	hosts := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			hosts = append(hosts, s)
		}
	}
	return hosts
}

// WithBuilderClient sets the builder client for the beacon chain builder service.
func WithBuilderClient(client builder.BuilderClient) Option {
	return func(s *Service) error {
		s.cfg.builderClient = client
		return nil
	}
}

// WithMultiBuilderClient sets the multi-builder (Gloas) client used to query
// builders directly via the post-ePBS Builder API.
func WithMultiBuilderClient(client builder.MultiBuilderClient) Option {
	return func(s *Service) error {
		s.cfg.multiBuilderClient = client
		return nil
	}
}

// WithHeadFetcher gets the head info from chain service.
func WithHeadFetcher(svc blockchain.HeadFetcher) Option {
	return func(s *Service) error {
		s.cfg.headFetcher = svc
		return nil
	}
}

// WithDatabase for head access.
func WithDatabase(beaconDB db.HeadAccessDatabase) Option {
	return func(s *Service) error {
		s.cfg.beaconDB = beaconDB
		return nil
	}
}

// WithRegistrationCache uses a cache for the validator registrations instead of a persistent db.
func WithRegistrationCache() Option {
	return func(s *Service) error {
		s.registrationCache = cache.NewRegistrationCache()
		return nil
	}
}
