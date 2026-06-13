package sqlcontext

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// runeLen is a tiny helper so tests can place a cursor "at end" without
// counting characters by hand.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

func TestSQLContextScenarios(t *testing.T) {
	cases := []struct {
		name   string
		sql    string
		offset int
		want   ContextResult
	}{
		{
			name:   "column context mid-list (AC scenario 1)",
			sql:    "SELECT id, na",
			offset: runeLen("SELECT id, na"), // cursor after "na"
			want:   ContextResult{Clause: ClauseSELECT, Expect: ExpectColumns},
		},
		{
			name:   "table context after FROM (AC scenario 2)",
			sql:    "SELECT * FROM ",
			offset: runeLen("SELECT * FROM "), // cursor at end
			want:   ContextResult{Clause: ClauseFROM, Expect: ExpectTables},
		},
		{
			name:   "join-condition after ON (AC scenario 3)",
			sql:    "SELECT * FROM users u JOIN orders o ON ",
			offset: runeLen("SELECT * FROM users u JOIN orders o ON "),
			want:   ContextResult{Clause: ClauseON, Expect: ExpectBoth},
		},
		{
			name:   "SELECT immediately after keyword",
			sql:    "SELECT ",
			offset: runeLen("SELECT "),
			want:   ContextResult{Clause: ClauseSELECT, Expect: ExpectColumns},
		},
		{
			name:   "WHERE column context",
			sql:    "SELECT id FROM t WHERE ",
			offset: runeLen("SELECT id FROM t WHERE "),
			want:   ContextResult{Clause: ClauseWHERE, Expect: ExpectColumns},
		},
		{
			name:   "WHERE continued by AND stays column context",
			sql:    "SELECT * FROM t WHERE a = 1 AND ",
			offset: runeLen("SELECT * FROM t WHERE a = 1 AND "),
			want:   ContextResult{Clause: ClauseWHERE, Expect: ExpectColumns},
		},
		{
			name:   "JOIN expects tables",
			sql:    "SELECT * FROM users u JOIN ",
			offset: runeLen("SELECT * FROM users u JOIN "),
			want:   ContextResult{Clause: ClauseJOIN, Expect: ExpectTables},
		},
		{
			name:   "LEFT JOIN expects tables (JOIN is the governing keyword)",
			sql:    "SELECT * FROM users u LEFT JOIN ",
			offset: runeLen("SELECT * FROM users u LEFT JOIN "),
			want:   ContextResult{Clause: ClauseJOIN, Expect: ExpectTables},
		},
		{
			name:   "USING is a join-condition keyword (Both)",
			sql:    "SELECT * FROM a JOIN b USING ",
			offset: runeLen("SELECT * FROM a JOIN b USING "),
			want:   ContextResult{Clause: ClauseON, Expect: ExpectBoth},
		},
		{
			name:   "UPDATE expects tables",
			sql:    "UPDATE ",
			offset: runeLen("UPDATE "),
			want:   ContextResult{Clause: ClauseFROM, Expect: ExpectTables},
		},
		{
			name:   "INSERT INTO expects tables",
			sql:    "INSERT INTO ",
			offset: runeLen("INSERT INTO "),
			want:   ContextResult{Clause: ClauseFROM, Expect: ExpectTables},
		},
		{
			name:   "cursor before any clause keyword",
			sql:    "   SELECT *",
			offset: 1, // inside leading whitespace, before SELECT
			want:   ContextResult{Clause: ClauseNone, Expect: ExpectNone},
		},
		{
			name:   "FROM still governs after a complete table name",
			sql:    "SELECT * FROM users ",
			offset: runeLen("SELECT * FROM users "),
			want:   ContextResult{Clause: ClauseFROM, Expect: ExpectTables},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.sql, tc.offset)
			// 1.1 scope: assert only clause + expect; InScopeTables /
			// Qualifier are covered by the 1.2 ident tests.
			if got.Clause != tc.want.Clause || got.Expect != tc.want.Expect {
				t.Fatalf("Analyze(%q, %d) = %+v, want clause/expect %+v", tc.sql, tc.offset, got, tc.want)
			}
		})
	}
}

func TestSQLContextStatementScoping(t *testing.T) {
	// Cursor in the second statement: the FROM there governs, and the
	// first statement (which has no FROM) does not interfere.
	sql := "SELECT 1; SELECT * FROM u"
	off := runeLen(sql) // inside second statement, after "u"
	got := Analyze(sql, off)
	want := ContextResult{Clause: ClauseFROM, Expect: ExpectTables}
	if got.Clause != want.Clause || got.Expect != want.Expect {
		t.Fatalf("Analyze(%q, %d) = %+v, want %+v", sql, off, got, want)
	}

	// A FROM in an earlier statement must NOT set Expect for a cursor in
	// a later, FROM-less statement.
	sql2 := "SELECT * FROM users; SELECT "
	off2 := runeLen(sql2) // after the trailing "SELECT "
	got2 := Analyze(sql2, off2)
	want2 := ContextResult{Clause: ClauseSELECT, Expect: ExpectColumns}
	if got2.Clause != want2.Clause || got2.Expect != want2.Expect {
		t.Fatalf("Analyze(%q, %d) = %+v, want %+v", sql2, off2, got2, want2)
	}
	// The earlier statement's users table must NOT leak into the
	// FROM-less second statement's scope.
	if len(got2.InScopeTables) != 0 {
		t.Fatalf("Analyze(%q) leaked InScopeTables across statements: %+v", sql2, got2.InScopeTables)
	}

	// Cursor in the FIRST statement sees its FROM, not the second's.
	sql3 := "SELECT * FROM ; SELECT col"
	off3 := strings.Index(sql3, ";") // just before the semicolon, in stmt 1
	got3 := Analyze(sql3, off3)
	want3 := ContextResult{Clause: ClauseFROM, Expect: ExpectTables}
	if got3.Clause != want3.Clause || got3.Expect != want3.Expect {
		t.Fatalf("Analyze(%q, %d) = %+v, want %+v", sql3, off3, got3, want3)
	}
}

func TestSQLContextNoiseAndMalformed(t *testing.T) {
	end := func(s string) int { return runeLen(s) }

	cases := []struct {
		name   string
		sql    string
		offset int
		want   ContextResult
	}{
		{
			name:   "empty input",
			sql:    "",
			offset: 0,
			want:   ContextResult{},
		},
		{
			name:   "lone keyword, cursor inside it",
			sql:    "SELECT",
			offset: 3, // inside the partial trailing keyword "SEL|ECT"
			want:   ContextResult{},
		},
		{
			name:   "partial trailing token FRO",
			sql:    "FRO",
			offset: 3,
			want:   ContextResult{},
		},
		{
			name:   "cursor inside unterminated string yields None",
			sql:    "SELECT * FROM users WHERE name = 'oops",
			offset: end("SELECT * FROM users WHERE name = 'oops"),
			want:   ContextResult{},
		},
		{
			name:   "cursor inside unterminated block comment yields None",
			sql:    "SELECT /* still open",
			offset: end("SELECT /* still open"),
			want:   ContextResult{},
		},
		{
			name:   "whitespace only",
			sql:    "   ",
			offset: 2,
			want:   ContextResult{},
		},
		{
			name:   "semicolons only",
			sql:    ";;;",
			offset: 1,
			want:   ContextResult{},
		},
		{
			name:   "offset past end is clamped, no panic",
			sql:    "SELECT * FROM ",
			offset: 9999,
			want:   ContextResult{Clause: ClauseFROM, Expect: ExpectTables},
		},
		{
			name:   "negative offset is clamped, no panic",
			sql:    "SELECT id",
			offset: -5,
			want:   ContextResult{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.sql, tc.offset) // must not panic
			if got.Clause != tc.want.Clause || got.Expect != tc.want.Expect {
				t.Fatalf("Analyze(%q, %d) = %+v, want %+v", tc.sql, tc.offset, got, tc.want)
			}
		})
	}
}

// TestSQLContextMultilineNoiseOpenedEarlier covers the specific gap the
// old single-line stripNoiseEx had: a string or comment that OPENS on a
// previous line and is still open at the cursor must yield Expect=None.
func TestSQLContextMultilineNoiseOpenedEarlier(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{
			name: "string opened on previous line still open at cursor",
			sql:  "SELECT * FROM users WHERE note = 'opened\nstill inside ",
		},
		{
			name: "block comment opened on previous line still open at cursor",
			sql:  "SELECT col /* opened earlier\nstill inside ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Analyze(tc.sql, runeLen(tc.sql))
			if got.Clause != ClauseNone || got.Expect != ExpectNone ||
				len(got.InScopeTables) != 0 || got.Qualifier.Present {
				t.Fatalf("Analyze(%q, end) = %+v, want zero (in noise)", tc.sql, got)
			}
		})
	}
}

// TestSQLContextNeverPanicsFuzzish throws a handful of adversarial
// strings and offsets at Analyze to assert it returns (rather than
// panics) on garbage.
func TestSQLContextNeverPanicsFuzzish(t *testing.T) {
	inputs := []string{
		"", " ", ";", "()", "'", "/*", "--", "$$", "$tag$",
		"SELECT FROM WHERE ON JOIN",
		"\x00\x01 SELECT",
		"日本語 FROM テーブル",
		"SELECT * FROM 日本; SELECT ",
	}
	offsets := []int{-100, -1, 0, 1, 3, 100, 1 << 20}
	for _, in := range inputs {
		for _, off := range offsets {
			_ = Analyze(in, off) // success criterion: no panic
		}
	}
}
