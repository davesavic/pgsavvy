package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	// defaultAPIBase is the GitHub REST API base; overridable in tests.
	defaultAPIBase = "https://api.github.com"

	// checksumsAssetName is the literal name of the checksums asset (set via
	// goreleaser checksum.name_template).
	checksumsAssetName = "checksums.txt"

	checksumsMaxBytes int64 = 1 << 20   // 1 MiB
	assetMaxBytes     int64 = 200 << 20 // 200 MiB
)

// release is the subset of the GitHub /releases/latest payload we consume.
type release struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// asset is a resolved release asset (name + download URL).
type asset struct {
	Name string
	URL  string
}

// fetchLatest GETs /repos/<owner>/<name>/releases/latest and parses the tag and
// assets. Errors are mapped to distinct, actionable conditions.
func (u *Updater) fetchLatest() (*release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", u.apiBase, u.opts.RepoOwner, u.opts.RepoName)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build release request: %w", err)
	}
	u.setHeaders(req)

	resp, err := u.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := mapReleaseStatus(resp.StatusCode); err != nil {
		return nil, err
	}

	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, checksumsMaxBytes)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release response: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("release response has empty tag_name; release may be malformed")
	}
	return &rel, nil
}

// mapReleaseStatus turns a /releases/latest HTTP status into a distinct error.
func mapReleaseStatus(code int) error {
	if code == http.StatusOK {
		return nil
	}
	if code == http.StatusForbidden || code == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	if code == http.StatusNotFound {
		return ErrNoReleases
	}
	return fmt.Errorf("github returned unexpected status %d when fetching the latest release", code)
}

// findAsset returns the asset matching name, or false if absent.
func findAsset(rel *release, name string) (asset, bool) {
	for _, a := range rel.Assets {
		if a.Name == name {
			return asset{Name: a.Name, URL: a.URL}, true
		}
	}
	return asset{}, false
}

// download streams the asset at url through w, capping at maxBytes. It returns a
// short-read error when the server closes before sending a full response that
// the caller can distinguish from a checksum mismatch.
func (u *Updater) download(url string, maxBytes int64, w io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	u.setHeaders(req)

	resp, err := u.client.Do(req)
	if err != nil {
		return downloadInterrupted(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	if _, err := io.Copy(w, io.LimitReader(resp.Body, maxBytes)); err != nil {
		return downloadInterrupted(err)
	}
	return nil
}

func (u *Updater) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "pgsavvy/"+u.opts.CurrentVersion)
}

// downloadInterrupted wraps mid-stream/transport failures (short reads,
// deadline-mid-copy, connection drops) so the user is told to retry rather than
// told the binary is untrustworthy — distinct from a checksum mismatch.
func downloadInterrupted(err error) error {
	return fmt.Errorf("%w: %v", ErrDownloadInterrupted, err)
}
