package sqlcontext

import "testing"

// TestInNoise covers the exported noise predicate that replaces the old
// single-line stripNoiseEx `open` flag. It must report true for a cursor
// inside an open OR closed string/comment, false in clear SQL, and — its
// key improvement — must handle noise that spans multiple lines.
func TestInNoise(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		cursor int
		want   bool
	}{
		{"clear sql", "SELECT * FROM users", runeLen("SELECT * FROM users"), false},
		{"empty", "", 0, false},
		{"inside open string", "SELECT 'abc", runeLen("SELECT 'abc"), true},
		{"inside closed string", "SELECT 'abc'", runeLen("SELECT 'ab"), true},
		{"after closed string", "SELECT 'abc' FROM ", runeLen("SELECT 'abc' FROM "), false},
		{"at string opening quote (not yet inside)", "SELECT 'abc", runeLen("SELECT "), false},
		{"inside line comment", "SELECT 1 -- note", runeLen("SELECT 1 -- no"), true},
		{"inside open block comment", "SELECT 1 /* note", runeLen("SELECT 1 /* no"), true},
		{"after closed block comment", "SELECT /* x */ FROM ", runeLen("SELECT /* x */ FROM "), false},
		// Closed dollar-quote is a String token; the cursor inside it is noise.
		// (An UNTERMINATED dollar-quote is NOT detectable — Chroma lexes `$$`
		// as a Number, not a String — a known limitation.)
		{"inside closed dollar quote", "SELECT $$body$$", runeLen("SELECT $$bo"), true},
		// Multi-line: the string opens on line 1 and the cursor sits on line
		// 2 still inside it — the prior single-line stripper missed this.
		{"multi-line open string", "SELECT 'abc\nFROM ", runeLen("SELECT 'abc\nFROM "), true},
		{"multi-line block comment", "SELECT 1 /* abc\nFROM ", runeLen("SELECT 1 /* abc\nFROM "), true},
		// Statement-scoped: a string opened+closed in an earlier statement
		// does not make a later statement's cursor "in noise".
		{"earlier statement noise does not leak", "SELECT 'x'; SELECT ", runeLen("SELECT 'x'; SELECT "), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InNoise(tc.sql, tc.cursor); got != tc.want {
				t.Errorf("InNoise(%q, %d) = %v; want %v", tc.sql, tc.cursor, got, tc.want)
			}
		})
	}
}

// TestInNoiseNeverPanics feeds malformed/partial fragments to confirm the
// error-tolerance contract holds.
func TestInNoiseNeverPanics(t *testing.T) {
	for _, in := range []string{"", "'", "$$", "/*", "--", "((((", ";;;", "'\n--\n$$"} {
		for off := -2; off <= runeLen(in)+2; off++ {
			_ = InNoise(in, off)
		}
	}
}
