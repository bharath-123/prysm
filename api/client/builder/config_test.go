package builder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OffchainLabs/prysm/v7/testing/require"
)

func TestLoadBuilderWhitelist(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantErr     bool
		errContains string
		wantLen     int
	}{
		{
			name: "valid config with multiple builders",
			content: `builders:
  - url: "https://builder1.example.com"
    min_bid: 100000000
  - url: "https://builder2.example.com"
    min_bid: 50000000
  - url: "https://builder3.example.com"
    min_bid: 0`,
			wantErr: false,
			wantLen: 3,
		},
		{
			name: "valid config with single builder",
			content: `builders:
  - url: "https://builder1.example.com"
    min_bid: 0`,
			wantErr: false,
			wantLen: 1,
		},
		{
			name: "valid config without min_bid (defaults to 0)",
			content: `builders:
  - url: "https://builder1.example.com"`,
			wantErr: false,
			wantLen: 1,
		},
		{
			name:        "empty builders list",
			content:     `builders: []`,
			wantErr:     true,
			errContains: "builder whitelist is empty",
		},
		{
			name: "empty url",
			content: `builders:
  - url: ""
    min_bid: 0`,
			wantErr:     true,
			errContains: "has empty URL",
		},
		{
			name:        "invalid yaml",
			content:     `invalid: yaml: content`,
			wantErr:     true,
			errContains: "failed to parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary file
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "builder-whitelist.yaml")
			err := os.WriteFile(tmpFile, []byte(tt.content), 0600)
			require.NoError(t, err)

			// Load the whitelist
			whitelist, err := LoadBuilderWhitelist(tmpFile)

			if tt.wantErr {
				require.ErrorContains(t, tt.errContains, err)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, whitelist)
			require.Equal(t, tt.wantLen, len(whitelist.Builders))
		})
	}
}

func TestLoadBuilderWhitelist_FileNotFound(t *testing.T) {
	_, err := LoadBuilderWhitelist("/nonexistent/path/to/file.yaml")
	require.ErrorContains(t, "failed to read builder whitelist file", err)
}

func TestBuilderWhitelist_Validate(t *testing.T) {
	tests := []struct {
		name        string
		whitelist   *BuilderWhitelist
		wantErr     bool
		errContains string
	}{
		{
			name:        "nil whitelist",
			whitelist:   nil,
			wantErr:     true,
			errContains: "builder whitelist is nil",
		},
		{
			name:        "empty whitelist",
			whitelist:   &BuilderWhitelist{Builders: []BuilderEntry{}},
			wantErr:     true,
			errContains: "builder whitelist is empty",
		},
		{
			name: "valid whitelist",
			whitelist: &BuilderWhitelist{
				Builders: []BuilderEntry{
					{URL: "https://builder1.example.com", MinBid: 100},
				},
			},
			wantErr: false,
		},
		{
			name: "whitelist with empty URL",
			whitelist: &BuilderWhitelist{
				Builders: []BuilderEntry{
					{URL: "", MinBid: 100},
				},
			},
			wantErr:     true,
			errContains: "has empty URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.whitelist.Validate()
			if tt.wantErr {
				require.ErrorContains(t, tt.errContains, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
