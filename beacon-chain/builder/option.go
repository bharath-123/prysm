package builder

import (
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
	whitelistFile := c.String(flags.BuilderWhitelistFile.Name)
	endpoint := c.String(flags.MevRelayEndpoint.Name)
	sszEnabled := c.Bool(flags.EnableBuilderSSZ.Name)

	var clientOpts []builder.ClientOpt
	if sszEnabled {
		log.Info("Using Builder APIs with SSZ enabled")
		clientOpts = append(clientOpts, builder.WithSSZ())
	}

	var builderClient builder.BuilderClient

	// BuilderWhitelistFile takes precedence over MevRelayEndpoint
	if whitelistFile != "" {
		whitelist, err := builder.LoadBuilderWhitelist(whitelistFile)
		if err != nil {
			return nil, err
		}
		multiClient, err := builder.NewMultiBuilderClient(whitelist, clientOpts...)
		if err != nil {
			return nil, err
		}
		builderClient = multiClient
		log.WithField("file", whitelistFile).Info("Loaded builder whitelist configuration")
	} else if endpoint != "" {
		// Fall back to legacy single endpoint mode (MEV-Boost)
		log.Warn("Using deprecated --http-mev-relay flag. Consider migrating to --builder-whitelist-file for direct builder connections.")
		client, err := builder.NewClient(endpoint, clientOpts...)
		if err != nil {
			return nil, err
		}
		builderClient = client
	}

	opts := []Option{
		WithBuilderClient(builderClient),
	}
	return opts, nil
}

// WithBuilderClient sets the builder client for the beacon chain builder service.
func WithBuilderClient(client builder.BuilderClient) Option {
	return func(s *Service) error {
		s.cfg.builderClient = client
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
