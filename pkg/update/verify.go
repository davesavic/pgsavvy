package update

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

// parseChecksums parses a checksums.txt body into a basename -> raw-32-byte
// digest map. Each line is `<sha256hex>  <filename>`; a leading `*` binary-mode
// marker on the filename is stripped. Duplicate filenames are an error.
func parseChecksums(body []byte) (map[string][]byte, error) {
	out := make(map[string][]byte)
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed checksums line: %q", line)
		}

		raw, err := hex.DecodeString(fields[0])
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("malformed checksums line (bad sha256): %q", line)
		}

		name := path.Base(strings.TrimPrefix(fields[1], "*"))
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("duplicate checksum entry for %q", name)
		}
		out[name] = raw
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read checksums: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("checksums.txt contained no usable entries")
	}
	return out, nil
}

// verifyChecksum compares the digest computed over the downloaded asset against
// the checksums entry for assetName (matched on basename). A mismatch returns
// ErrChecksumMismatch — a distinct condition from a short download.
func verifyChecksum(checksums map[string][]byte, assetName string, computed []byte) ([]byte, error) {
	want, ok := checksums[path.Base(assetName)]
	if !ok {
		return nil, fmt.Errorf("checksums.txt has no entry for %q; release may be corrupted", assetName)
	}
	if !bytes.Equal(want, computed) {
		return nil, fmt.Errorf("%w: do not trust the downloaded binary", ErrChecksumMismatch)
	}
	return want, nil
}
