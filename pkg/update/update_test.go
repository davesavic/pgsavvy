package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeRelease describes the assets a fake GitHub server should expose.
type fakeRelease struct {
	tag        string
	binName    string
	binBody    []byte
	sumsName   string
	sumsBody   []byte
	emitBin    bool // include the binary asset in assets[]
	emitSums   bool // include the checksums asset in assets[]
	binStatus  int  // status for the binary asset download; 0 → 200
	truncateBy int  // bytes to drop from binBody when serving (short read)
}

// newFakeGitHub returns an httptest server that serves /releases/latest and the
// per-asset download URLs, plus the User-Agent header it last observed.
func newFakeGitHub(t *testing.T, owner, name string, rel fakeRelease, releaseStatus int, gotUA *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	relPath := fmt.Sprintf("/repos/%s/%s/releases/latest", owner, name)
	mux.HandleFunc(relPath, func(w http.ResponseWriter, r *http.Request) {
		if gotUA != nil {
			*gotUA = r.Header.Get("User-Agent")
		}
		if releaseStatus != 0 && releaseStatus != http.StatusOK {
			w.WriteHeader(releaseStatus)
			return
		}
		assets := []map[string]string{}
		if rel.emitBin {
			assets = append(assets, map[string]string{
				"name":                 rel.binName,
				"browser_download_url": srv.URL + "/dl/" + rel.binName,
			})
		}
		if rel.emitSums {
			assets = append(assets, map[string]string{
				"name":                 rel.sumsName,
				"browser_download_url": srv.URL + "/dl/" + rel.sumsName,
			})
		}
		body := map[string]any{"tag_name": rel.tag, "assets": assets}
		_ = json.NewEncoder(w).Encode(body)
	})

	mux.HandleFunc("/dl/"+rel.binName, func(w http.ResponseWriter, r *http.Request) {
		if rel.binStatus != 0 && rel.binStatus != http.StatusOK {
			w.WriteHeader(rel.binStatus)
			return
		}
		body := rel.binBody
		if rel.truncateBy > 0 {
			// Advertise the full length but write fewer bytes, then close →
			// the client gets io.ErrUnexpectedEOF mid-copy.
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:len(body)-rel.truncateBy])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		_, _ = w.Write(body)
	})

	if rel.sumsName != "" {
		mux.HandleFunc("/dl/"+rel.sumsName, func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(rel.sumsBody)
		})
	}

	t.Cleanup(srv.Close)
	return srv
}

// checksumsLine builds a `<sha256hex>  <filename>` line for body.
func checksumsLine(body []byte, filename string) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filename)
}

func baseRelease(tag, goos, goarch string) fakeRelease {
	binName := assetName(tag, goos, goarch)
	binBody := []byte("FAKE-BINARY-CONTENT-" + tag)
	return fakeRelease{
		tag:      tag,
		binName:  binName,
		binBody:  binBody,
		sumsName: checksumsAssetName,
		sumsBody: []byte(checksumsLine(binBody, binName)),
		emitBin:  true,
		emitSums: true,
	}
}

func baseOpts(srvURL, current string) Options {
	return Options{
		CurrentVersion: current,
		BuildSource:    "release",
		RepoOwner:      "davesavic",
		RepoName:       "pgsavvy",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Stdout:         io.Discard,
		apiBase:        srvURL,
	}
}

func TestRun_UpdateAvailable_VerifiesAndHandsOff(t *testing.T) {
	var ua string
	rel := baseRelease("v1.2.0", "linux", "amd64")
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, &ua)

	res, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.NoError(t, err)
	require.False(t, res.UpToDate)
	require.Equal(t, "v1.2.0", res.LatestTag)
	require.Equal(t, rel.binName, res.AssetName)

	// AC: streamed sha256 matches checksums.txt.
	want := sha256.Sum256(rel.binBody)
	require.Equal(t, want[:], res.Checksum)

	// AC: User-Agent header sent.
	require.Equal(t, "pgsavvy/v1.0.0", ua)
}

func TestRun_UpToDate_NoBinaryDownload(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	// Trip-wire: if the binary URL is hit, the test fails.
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)

	res, err := Run(baseOpts(srv.URL, "v1.2.0"))
	require.NoError(t, err)
	require.True(t, res.UpToDate)
	require.Nil(t, res.Verified)
	require.Nil(t, res.Checksum)
}

func TestRun_NonReleaseBuild_Refused(t *testing.T) {
	cases := []struct {
		name    string
		version string
		source  string
	}{
		{"dev version", "dev", "release"},
		{"empty version", "", "release"},
		{"snapshot source", "v1.2.0", "task"},
		{"snapshot pseudo", "v0.0.0-next", "snapshot"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := baseOpts("http://127.0.0.1:0/should-not-be-hit", tc.version)
			opts.BuildSource = tc.source
			res, err := Run(opts)
			require.Nil(t, res)
			require.ErrorIs(t, err, ErrNonReleaseBuild)
		})
	}
}

func TestRun_ChecksumMismatch_NoHandoff(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	// Checksums line claims a different hash than the served binary.
	rel.sumsBody = []byte(checksumsLine([]byte("WRONG"), rel.binName))
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)

	res, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.Nil(t, res)
	require.ErrorIs(t, err, ErrChecksumMismatch)
}

func TestRun_Handoff_RereadableBytesAndRawDigest(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)

	res, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.NoError(t, err)

	// Re-readable []byte, not a consumed reader.
	require.Equal(t, rel.binBody, res.Verified)
	require.Equal(t, rel.binBody, res.Verified, "bytes still present on second read")

	// Raw 32-byte digest, NOT 64-char hex.
	require.Len(t, res.Checksum, sha256.Size)
	require.Len(t, res.Checksum, 32)
	require.NotEqual(t, 64, len(res.Checksum))
}

func TestRun_RateLimitedAndNoReleases(t *testing.T) {
	t.Run("403 rate limited", func(t *testing.T) {
		rel := baseRelease("v1.2.0", "linux", "amd64")
		srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusForbidden, nil)
		_, err := Run(baseOpts(srv.URL, "v1.0.0"))
		require.ErrorIs(t, err, ErrRateLimited)
	})
	t.Run("429 rate limited", func(t *testing.T) {
		rel := baseRelease("v1.2.0", "linux", "amd64")
		srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusTooManyRequests, nil)
		_, err := Run(baseOpts(srv.URL, "v1.0.0"))
		require.ErrorIs(t, err, ErrRateLimited)
	})
	t.Run("404 no releases", func(t *testing.T) {
		rel := baseRelease("v1.2.0", "linux", "amd64")
		srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusNotFound, nil)
		_, err := Run(baseOpts(srv.URL, "v1.0.0"))
		require.ErrorIs(t, err, ErrNoReleases)
		require.NotErrorIs(t, err, ErrRateLimited)
	})
}

func TestRun_MissingAssets(t *testing.T) {
	t.Run("host binary absent → distinct error, no checksum step", func(t *testing.T) {
		rel := baseRelease("v1.2.0", "linux", "amd64")
		rel.emitBin = false
		srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)
		_, err := Run(baseOpts(srv.URL, "v1.0.0"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not include a binary for this host")
		require.NotContains(t, err.Error(), "corrupted")
	})
	t.Run("checksums.txt absent → corrupted error, not 404", func(t *testing.T) {
		rel := baseRelease("v1.2.0", "linux", "amd64")
		rel.emitSums = false
		srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)
		_, err := Run(baseOpts(srv.URL, "v1.0.0"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "corrupted")
		require.NotErrorIs(t, err, ErrNoReleases)
	})
}

func TestRun_UnsupportedPlatform_NoAssetLookup(t *testing.T) {
	// Trip-wire server: any request fails the test (apiBase points nowhere real).
	opts := baseOpts("http://127.0.0.1:0/should-not-be-hit", "v1.0.0")
	opts.GOOS = "linux"
	opts.GOARCH = "ppc64le"
	_, err := Run(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "auto-update not available for linux/ppc64le")
	require.Contains(t, err.Error(), "linux/amd64")
}

func TestRun_TagWithoutVPrefix_NormalizesAndCompares(t *testing.T) {
	// Latest tag lacks the leading v; current does too. 1.2.0 > 1.0.0 → update.
	rel := baseRelease("1.2.0", "linux", "amd64") // assetName uses tag verbatim
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)
	res, err := Run(baseOpts(srv.URL, "1.0.0"))
	require.NoError(t, err)
	require.False(t, res.UpToDate)
	require.Equal(t, "1.2.0", res.LatestTag)
}

func TestRun_EmptyTagName_Errors(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	rel.tag = ""
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)
	_, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "tag_name")
}

func TestRun_MalformedChecksums_Errors(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	rel.sumsBody = []byte("this-is-not-a-valid-checksums-line\n")
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)
	_, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrChecksumMismatch)
}

func TestRun_TruncatedDownload_DistinctFromMismatch(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	rel.binBody = []byte(strings.Repeat("X", 4096))
	rel.sumsBody = []byte(checksumsLine(rel.binBody, rel.binName))
	rel.truncateBy = 2048
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusOK, nil)

	_, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrDownloadInterrupted)
	require.NotErrorIs(t, err, ErrChecksumMismatch)
}

func TestRun_ServerError_Surfaced(t *testing.T) {
	rel := baseRelease("v1.2.0", "linux", "amd64")
	srv := newFakeGitHub(t, "davesavic", "pgsavvy", rel, http.StatusInternalServerError, nil)
	res, err := Run(baseOpts(srv.URL, "v1.0.0"))
	require.Nil(t, res)
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
}

// TestDownload_SlowProgressingBody_NotCappedByTotalTimeout reproduces the
// `pgsavvy update` failure: a 51 MiB asset cannot finish inside a 30s total
// http.Client.Timeout, so the body read aborts with "context deadline exceeded
// (Client.Timeout ... while reading body)". The server below delivers headers
// instantly, then trickles the body in chunks whose total elapsed time exceeds
// httpPhaseTimeout while each individual gap stays well under it. With a total
// request timeout this fails; with phase-only timeouts (the fix) it succeeds.
func TestDownload_SlowProgressingBody_NotCappedByTotalTimeout(t *testing.T) {
	prev := httpPhaseTimeout
	httpPhaseTimeout = 150 * time.Millisecond
	t.Cleanup(func() { httpPhaseTimeout = prev })

	want := []byte("SLOW-BUT-HEALTHY-DOWNLOAD-PAYLOAD")
	gap := 40 * time.Millisecond // < phase timeout per chunk; > phase timeout in total

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)
		flusher.Flush() // headers arrive immediately, before the slow body
		for _, b := range want {
			time.Sleep(gap)
			_, _ = w.Write([]byte{b})
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	u := &Updater{client: newHTTPClient()}
	var got bytes.Buffer
	err := u.download(srv.URL, assetMaxBytes, &got)

	require.NoError(t, err)
	require.NotErrorIs(t, err, ErrDownloadInterrupted)
	require.Equal(t, want, got.Bytes())
}

func TestParseChecksums_BinaryModeMarkerAndDuplicate(t *testing.T) {
	body := []byte("HELLO")
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])

	t.Run("strips leading * marker and matches basename", func(t *testing.T) {
		line := fmt.Sprintf("%s  *path/to/pgsavvy_v1.2.0_linux_amd64\n", hexsum)
		got, err := parseChecksums([]byte(line))
		require.NoError(t, err)
		require.Equal(t, sum[:], got["pgsavvy_v1.2.0_linux_amd64"])
	})
	t.Run("duplicate filename errors", func(t *testing.T) {
		line := fmt.Sprintf("%s  f\n%s  f\n", hexsum, hexsum)
		_, err := parseChecksums([]byte(line))
		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate")
	})
}
