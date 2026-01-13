package builder

import (
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

// BuilderEntry represents a single builder configuration with URL and minimum bid.
// This corresponds to the BuilderConfig in the specification.
type BuilderEntry struct {
	URL    string `yaml:"url"`
	MinBid uint64 `yaml:"min_bid"`
}

// BuilderWhitelist represents a list of whitelisted builders.
// This can be loaded from a YAML file and passed to the multi-builder client.
type BuilderWhitelist struct {
	Builders []BuilderEntry `yaml:"builders"`
}

// LoadBuilderWhitelist loads the builder whitelist configuration from a YAML file.
func LoadBuilderWhitelist(path string) (*BuilderWhitelist, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath) // #nosec G304
	if err != nil {
		return nil, errors.Wrap(err, "failed to read builder whitelist file")
	}

	var whitelist BuilderWhitelist
	if err := yaml.Unmarshal(data, &whitelist); err != nil {
		return nil, errors.Wrap(err, "failed to parse builder whitelist YAML")
	}

	if len(whitelist.Builders) == 0 {
		return nil, errors.New("builder whitelist is empty")
	}

	// Validate each builder entry
	for i, b := range whitelist.Builders {
		if b.URL == "" {
			return nil, errors.Errorf("builder at index %d has empty URL", i)
		}
	}

	return &whitelist, nil
}

// Validate performs validation on the BuilderWhitelist.
func (bw *BuilderWhitelist) Validate() error {
	if bw == nil {
		return errors.New("builder whitelist is nil")
	}
	if len(bw.Builders) == 0 {
		return errors.New("builder whitelist is empty")
	}
	for i, b := range bw.Builders {
		if b.URL == "" {
			return errors.Errorf("builder at index %d has empty URL", i)
		}
	}
	return nil
}
