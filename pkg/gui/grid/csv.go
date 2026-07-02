package grid

import "strings"

// Rfc4180Quote wraps a field in double quotes when it contains the delimiter,
// a double quote, CR or LF. Embedded double quotes are doubled.
func Rfc4180Quote(s string, delim byte) string {
	if !strings.ContainsAny(s, "\r\n\"") && !strings.ContainsRune(s, rune(delim)) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
