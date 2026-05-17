package config

import "strings"

// SafeText strips control bytes from s that could corrupt the terminal
// when emitted to a TUI cell. Specifically: bytes < 0x20 EXCEPT tab
// (0x09), and DEL (0x7f). Multi-byte UTF-8 sequences are preserved
// because the continuation bytes are all >= 0x80.
//
// Policy: minimal-loss. SafeText removes ONLY the unsafe bytes; it does
// NOT attempt to recognise terminal escape sequences and strip their
// printable tail. For example:
//
//	SafeText("evil\x1b[2J")            = "evil[2J"
//	SafeText("hi\x1b]0;pwned\x07world") = "hi]0;pwnedworld"  // \x1b and \x07 stripped; ]0; and word survive
//	SafeText("a\tb")                   = "a\tb"               // tab preserved
//	SafeText("héllo")                  = "héllo"              // multi-byte UTF-8 preserved
func SafeText(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\t' {
			continue
		}
		if c == 0x7f {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
