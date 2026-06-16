package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGolden_AssetNameMatchesGoreleaser asserts that the asset name this package
// computes byte-equals a name goreleaser actually produced, catching contract
// drift the hand-authored httptest fixtures cannot. goreleaser --snapshot forces
// tag v0.0.0, so the expected name is built from that same tag.
func TestGolden_AssetNameMatchesGoreleaser(t *testing.T) {
	// dist/ lives at the repo root; this file is pkg/update.
	path := filepath.Join("..", "..", "dist", "artifacts.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("dist/artifacts.json absent (run goreleaser --snapshot): %v", err)
	}

	var artifacts []struct {
		Name  string `json:"name"`
		Extra struct {
			Format string `json:"Format"`
		} `json:"extra"`
	}
	require.NoError(t, json.Unmarshal(raw, &artifacts))

	const snapshotTag = "v0.0.0"
	names := map[string]bool{}
	for _, a := range artifacts {
		if a.Extra.Format == "binary" {
			names[a.Name] = true
		}
	}
	require.NotEmpty(t, names, "no binary-format artifacts found in dist/artifacts.json")

	// Every supported platform's computed name must appear in goreleaser output.
	for goos, arches := range supportedPlatforms {
		for goarch := range arches {
			want := assetName(snapshotTag, goos, goarch)
			require.Truef(t, names[want], "computed asset %q not found in goreleaser artifacts; contract drift", want)
		}
	}

	// And the host's own computed name must match (uses real runtime values).
	if supportedPlatforms[runtime.GOOS][runtime.GOARCH] {
		host := assetName(snapshotTag, runtime.GOOS, runtime.GOARCH)
		require.Truef(t, names[host], "host asset %q missing from goreleaser artifacts", host)
	}
}
