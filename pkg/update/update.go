// Package update fetches the latest GitHub Release, selects the host's binary
// asset, downloads and verifies it against checksums.txt, and compares versions.
// It does NOT replace the running binary (that is the caller's job in T3) and
// imports nothing from pkg/app to avoid an import cycle — it defines its own
// Options, mirroring pkg/logs.BuildInfo's decoupling precedent.
package update

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"golang.org/x/mod/semver"
)

// httpPhaseTimeout bounds the connect, TLS-handshake, and response-header phases
// of every request so a stalled server can never hang us forever. It is
// deliberately NOT a total request timeout: the binary asset is tens of MiB, so
// capping the whole exchange (http.Client.Timeout) would abort a slow but
// healthy body download mid-stream. A var (not const) so tests can shrink it.
var httpPhaseTimeout = 30 * time.Second

// newHTTPClient builds the client used for all update requests. It guards the
// pre-body phases via the transport but leaves body-read time unbounded, so a
// large, slow-but-progressing download completes instead of failing with
// "Client.Timeout exceeded while reading body".
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: httpPhaseTimeout}).DialContext,
			TLSHandshakeTimeout:   httpPhaseTimeout,
			ResponseHeaderTimeout: httpPhaseTimeout,
			IdleConnTimeout:       httpPhaseTimeout,
		},
	}
}

// Sentinel errors let the caller (and tests) distinguish each actionable
// condition via errors.Is.
var (
	// ErrRateLimited is returned on HTTP 403/429 from the GitHub API.
	ErrRateLimited = errors.New("github API rate limit reached; try again later or set up authentication")
	// ErrNoReleases is returned on HTTP 404 for /releases/latest (no release published yet).
	ErrNoReleases = errors.New("no releases published yet")
	// ErrDownloadInterrupted indicates a short read / transport failure mid-download (retry).
	ErrDownloadInterrupted = errors.New("download interrupted, retry")
	// ErrChecksumMismatch indicates the downloaded bytes did not match checksums.txt.
	ErrChecksumMismatch = errors.New("checksum mismatch")
	// ErrNonReleaseBuild indicates this build is not a release build and must not self-update.
	ErrNonReleaseBuild = errors.New("not a release build")
)

// Options configures an update check. It is the public input handed in by the
// entry point in T3 (CurrentVersion/BuildSource from BuildInfo, GOOS/GOARCH from
// runtime). It imports nothing from pkg/app.
type Options struct {
	CurrentVersion string
	BuildSource    string
	RepoOwner      string
	RepoName       string
	GOOS           string
	GOARCH         string
	Stdout         io.Writer

	// apiBase overrides the GitHub API base URL in tests; empty → defaultAPIBase.
	apiBase string
}

// Result is the handoff to T3. When UpToDate is true, no binary was downloaded
// and Verified/Checksum are nil. Otherwise Verified holds the full re-readable
// binary bytes (NOT a consumed reader) and Checksum is the RAW 32-byte sha256
// digest (len == 32) so T3 can assign selfupdate.Options.Checksum directly.
type Result struct {
	UpToDate       bool
	CurrentVersion string
	LatestTag      string
	AssetName      string
	AssetURL       string
	AssetSize      int64
	Verified       []byte
	Checksum       []byte // raw 32-byte sha256 digest
}

// Updater holds the resolved client and base URL for a single run.
type Updater struct {
	opts    Options
	client  *http.Client
	apiBase string
}

// Run performs the full check-fetch-select-verify-compare flow described in the
// epic and returns the handoff Result. It does not replace the binary.
func Run(opts Options) (*Result, error) {
	if err := checkProvenance(opts); err != nil {
		return nil, err
	}
	if err := checkPlatformSupported(opts.GOOS, opts.GOARCH); err != nil {
		return nil, err
	}

	apiBase := opts.apiBase
	if apiBase == "" {
		apiBase = defaultAPIBase
	}
	u := &Updater{
		opts:    opts,
		client:  newHTTPClient(),
		apiBase: apiBase,
	}
	return u.run()
}

// checkProvenance refuses non-release builds. The gate is BuildSource (build
// provenance), not just the version string, so a goreleaser --snapshot
// pseudo-version (non-dev, non-empty) cannot self-update.
func checkProvenance(opts Options) error {
	if opts.CurrentVersion == "" || opts.CurrentVersion == "dev" {
		return fmt.Errorf("%w (version %q): reinstall with `go install github.com/davesavic/pgsavvy@latest` or download a release binary to update", ErrNonReleaseBuild, opts.CurrentVersion)
	}
	if opts.BuildSource != "release" {
		return fmt.Errorf("%w (build source %q): use a release binary to self-update", ErrNonReleaseBuild, opts.BuildSource)
	}
	return nil
}

func (u *Updater) run() (*Result, error) {
	rel, err := u.fetchLatest()
	if err != nil {
		return nil, err
	}

	if !updateAvailable(u.opts.CurrentVersion, rel.TagName) {
		return &Result{
			UpToDate:       true,
			CurrentVersion: u.opts.CurrentVersion,
			LatestTag:      rel.TagName,
		}, nil
	}

	wantAsset := assetName(rel.TagName, u.opts.GOOS, u.opts.GOARCH)
	binAsset, ok := findAsset(rel, wantAsset)
	if !ok {
		return nil, fmt.Errorf("release %s does not include a binary for this host (%s); download manually", rel.TagName, wantAsset)
	}
	sumsAsset, ok := findAsset(rel, checksumsAssetName)
	if !ok {
		return nil, fmt.Errorf("release %s is missing %s; the release may be corrupted, download manually", rel.TagName, checksumsAssetName)
	}

	verified, checksum, err := u.downloadAndVerify(binAsset, sumsAsset)
	if err != nil {
		return nil, err
	}

	return &Result{
		UpToDate:       false,
		CurrentVersion: u.opts.CurrentVersion,
		LatestTag:      rel.TagName,
		AssetName:      binAsset.Name,
		AssetURL:       binAsset.URL,
		AssetSize:      int64(len(verified)),
		Verified:       verified,
		Checksum:       checksum,
	}, nil
}

// downloadAndVerify streams the binary through sha256 (no double ReadAll),
// downloads checksums.txt, and verifies. It returns the verified bytes and the
// raw 32-byte digest.
func (u *Updater) downloadAndVerify(binAsset, sumsAsset asset) ([]byte, []byte, error) {
	var sumsBuf bytes.Buffer
	if err := u.download(sumsAsset.URL, checksumsMaxBytes, &sumsBuf); err != nil {
		return nil, nil, err
	}
	checksums, err := parseChecksums(sumsBuf.Bytes())
	if err != nil {
		return nil, nil, err
	}

	var binBuf bytes.Buffer
	hasher := sha256.New()
	// Stream once into both the buffer (handoff bytes) and the hasher (digest).
	if err := u.download(binAsset.URL, assetMaxBytes, io.MultiWriter(&binBuf, hasher)); err != nil {
		return nil, nil, err
	}

	computed := hasher.Sum(nil)
	raw, err := verifyChecksum(checksums, binAsset.Name, computed)
	if err != nil {
		return nil, nil, err
	}
	return binBuf.Bytes(), raw, nil
}

// updateAvailable reports whether latestTag is strictly newer than current after
// v-prefix normalization. current >= latest → false (no-op).
func updateAvailable(current, latestTag string) bool {
	return semver.Compare(normalize(current), normalize(latestTag)) < 0
}

// normalize ensures a leading v (semver package requires it).
func normalize(v string) string {
	if v == "" {
		return v
	}
	if v[0] == 'v' {
		return v
	}
	return "v" + v
}
