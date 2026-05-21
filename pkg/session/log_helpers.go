package session

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

// Environment opt-ins for log verbosity (AD-14). Default behaviour is
// minimum-data: SQL is truncated + connection-password scrubbed, bound
// params are hashed.
const (
	envIncludeSQL    = "DBSAVVY_LOG_INCLUDE_SQL"
	envIncludeParams = "DBSAVVY_LOG_INCLUDE_PARAMS"

	// sqlPreviewMax is the truncation budget for SQL emitted to logs by
	// default. AD-14 mandates 200; the cut is at a UTF-8 rune boundary so
	// emitted previews are never invalid UTF-8.
	sqlPreviewMax = 200

	// paramsHashCount is the number of leading params emitted as hashes by
	// default. Matches AD-14.
	paramsHashCount = 5

	// paramHashLen is the truncated hex length of each sha256 emit. 12 hex
	// chars (48 bits) is enough to disambiguate within a single session.
	paramHashLen = 12
)

// sqlPreview returns the first sqlPreviewMax bytes of sql, cut at a UTF-8
// rune boundary so the result is always valid UTF-8. When the env var
// DBSAVVY_LOG_INCLUDE_SQL=full is set, the SQL is returned verbatim with
// no truncation and no password scrub (forensic opt-in). Otherwise, the
// supplied connection-profile password (if non-empty) is scrubbed via
// literal substring replacement with "***" BEFORE truncation.
func sqlPreview(sql, connectionPassword string) string {
	if os.Getenv(envIncludeSQL) == "full" {
		return sql
	}
	if connectionPassword != "" {
		sql = strings.ReplaceAll(sql, connectionPassword, "***")
	}
	return truncateUTF8(sql, sqlPreviewMax)
}

// truncateUTF8 returns s when len(s) <= max, otherwise the longest prefix
// of s with byte-length <= max that ends on a UTF-8 rune boundary. The
// terminating rune is dropped (not split) if it would overflow.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	// Walk back to the start of the rune at or before max.
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// paramsHashes returns ([]string) for the first paramsHashCount values in
// args. Each entry is the first paramHashLen hex chars of sha256(
// fmt.Sprint(v)). When DBSAVVY_LOG_INCLUDE_PARAMS=1 is set, the raw
// stringified values are returned instead (forensic opt-in, AD-14). The
// returned slice is empty when args is empty.
//
// paramsCount returns len(args) so callers can emit
// `params_count` alongside `params_hashes` (or raw values).
func paramsHashes(args []any) []string {
	if len(args) == 0 {
		return nil
	}
	verbose := os.Getenv(envIncludeParams) == "1"
	n := len(args)
	if !verbose && n > paramsHashCount {
		n = paramsHashCount
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		s := fmt.Sprint(args[i])
		if verbose {
			out[i] = s
			continue
		}
		sum := sha256.Sum256([]byte(s))
		out[i] = hex.EncodeToString(sum[:])[:paramHashLen]
	}
	return out
}

// noticePreview applies session.RedactConnectionString + optional literal
// connection-password substring scrub to a pgconn notice/error string.
// Mirrors sqlPreview's "scrub before emit" contract.
func noticePreview(s, connectionPassword string) string {
	s = RedactConnectionString(s)
	if connectionPassword != "" {
		s = strings.ReplaceAll(s, connectionPassword, "***")
	}
	return s
}
