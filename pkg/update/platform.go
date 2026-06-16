package update

import "fmt"

// supportedPlatforms lists the GOOS/GOARCH combinations goreleaser builds assets
// for. A host outside this matrix has no asset to download.
var supportedPlatforms = map[string]map[string]bool{
	"linux":   {"amd64": true, "arm64": true},
	"darwin":  {"amd64": true, "arm64": true},
	"windows": {"amd64": true, "arm64": true},
}

const supportedPlatformsList = "linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64, windows/arm64"

// checkPlatformSupported returns a distinct, actionable error when the host is
// outside the build matrix so callers can short-circuit before any asset lookup.
func checkPlatformSupported(goos, goarch string) error {
	if supportedPlatforms[goos][goarch] {
		return nil
	}
	return fmt.Errorf("auto-update not available for %s/%s; download manually. supported platforms: %s", goos, goarch, supportedPlatformsList)
}

// assetName builds the release asset name for a tag/platform, mirroring the
// goreleaser template `pgsavvy_{{.Tag}}_{{.Os}}_{{.Arch}}` (+.exe on windows).
// The tag is used VERBATIM (it keeps its leading v).
func assetName(tag, goos, goarch string) string {
	name := fmt.Sprintf("pgsavvy_%s_%s_%s", tag, goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}
