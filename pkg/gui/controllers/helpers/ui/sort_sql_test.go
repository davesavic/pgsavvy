package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestWrapSorted(t *testing.T) {
	tests := []struct {
		name    string
		orig    string
		ordinal int
		dir     sortDir
		want    string
	}{
		{
			name:    "plain asc strips trailing semicolon",
			orig:    "SELECT * FROM t;",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 1 ASC",
		},
		{
			name:    "join duplicate column uses ordinal not name",
			orig:    "SELECT u.id, p.id FROM users u JOIN posts p ON u.id=p.user_id",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT u.id, p.id FROM users u JOIN posts p ON u.id=p.user_id\n) _pgsavvy_sort\nORDER BY 1 ASC",
		},
		{
			name:    "semicolon inside string literal is not truncated",
			orig:    "SELECT * FROM t WHERE x='a;b'",
			ordinal: 2,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t WHERE x='a;b'\n) _pgsavvy_sort\nORDER BY 2 ASC",
		},
		{
			name:    "desc keyword",
			orig:    "SELECT * FROM t",
			ordinal: 3,
			dir:     sortDesc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 3 DESC",
		},
		{
			name:    "clear returns orig verbatim including trailing semicolon",
			orig:    "SELECT * FROM t;",
			ordinal: 1,
			dir:     sortClear,
			want:    "SELECT * FROM t;",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapSorted(tt.orig, tt.ordinal, tt.dir)
			if got != tt.want {
				t.Fatalf("wrapSorted() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestWrapSorted_TrailingCommentSurvives asserts the unconditional newline rule:
// a trailing line comment in orig must not comment out the generated ORDER BY.
func TestWrapSorted_TrailingCommentSurvives(t *testing.T) {
	orig := "SELECT * FROM t -- note"
	got := wrapSorted(orig, 1, sortAsc)

	if !strings.Contains(got, "\n)") {
		t.Fatalf("expected %q to contain a newline before the closing paren, got %q", orig, got)
	}

	// ORDER BY must live on its own line, not appended to the comment line.
	lines := strings.SplitSeq(got, "\n")
	for line := range lines {
		if strings.Contains(line, "-- note") && strings.Contains(line, "ORDER BY") {
			t.Fatalf("ORDER BY is on the same line as the trailing comment: %q", line)
		}
	}
	if !strings.Contains(got, "\nORDER BY 1 ASC") {
		t.Fatalf("expected ORDER BY on its own line, got %q", got)
	}
}

// TestWrapSorted_ClearByteForByte asserts an exact-equality contract for clear.
func TestWrapSorted_ClearByteForByte(t *testing.T) {
	orig := "SELECT * FROM t;  \n"
	if got := wrapSorted(orig, 1, sortClear); got != orig {
		t.Fatalf("clear must return orig verbatim: got %q, want %q", got, orig)
	}
}

func TestWrapSortedLimitOffsetLexicalBoundaries(t *testing.T) {
	continuedEscape := "SELECT n, E'a'\n'it\\'s' AS label FROM generate_series(1,10) AS n"
	for _, tc := range []struct {
		name  string
		orig  string
		inner string
		tail  string
	}{
		{name: "line comment limit", orig: "SELECT generate_series(1,10) AS n -- LIMIT 1"},
		{name: "line comment offset", orig: "SELECT generate_series(1,10) AS n -- OFFSET 9"},
		{name: "line comment both", orig: "SELECT 1 -- LIMIT 2 OFFSET 3"},
		{name: "block comment", orig: "SELECT 1 /* LIMIT 2 OFFSET 3 */"},
		{name: "nested block comment", orig: "SELECT 1 /* outer /* LIMIT 2 */ OFFSET 3 */"},
		{name: "quoted string", orig: "SELECT 'it''s LIMIT 1 OFFSET 2' AS n"},
		{name: "escape string", orig: `SELECT E'it\'s -- LIMIT 1' AS n`},
		{name: "dollar string", orig: "SELECT $$one\n-- LIMIT 1 OFFSET 2\n$$ AS n"},
		{name: "tagged dollar string", orig: "SELECT $tag$) /* LIMIT 1 */ OFFSET 2$tag$ AS n"},
		{name: "quoted identifier", orig: `SELECT 1 AS "LIMIT 1 OFFSET 2"`},
		{name: "nested query", orig: "SELECT * FROM (SELECT generate_series(1,10) LIMIT 4 OFFSET 1) AS n"},
		{name: "nested with commented tail", orig: "SELECT * FROM (SELECT 1 LIMIT 4) AS n -- OFFSET 2"},
		{name: "CTE limit", orig: "WITH n AS (SELECT 1 LIMIT 2) SELECT * FROM n"},
		{name: "real after block comment", orig: "SELECT 1 /* LIMIT 9 */ LIMIT 3", inner: "SELECT 1 /* LIMIT 9 */", tail: "LIMIT 3"},
		{name: "real with comment separators", orig: "SELECT 1 LIMIT /* count */ 3 OFFSET /* start */ 1", inner: "SELECT 1", tail: "LIMIT /* count */ 3 OFFSET /* start */ 1"},
		{name: "real before line comment", orig: "SELECT 1 LIMIT 3 -- LIMIT 1", inner: "SELECT 1", tail: "LIMIT 3 -- LIMIT 1"},
		{name: "real before block comment", orig: "SELECT 1 OFFSET 2 /* OFFSET 8 */", inner: "SELECT 1", tail: "OFFSET 2 /* OFFSET 8 */"},
		{name: "real after line comment", orig: "SELECT 1 -- LIMIT 9\nLIMIT 3 OFFSET 1", inner: "SELECT 1 -- LIMIT 9", tail: "LIMIT 3 OFFSET 1"},
		{name: "real offset then limit", orig: "SELECT 1 OFFSET /* skip */ 2 LIMIT 3 -- OFFSET 8", inner: "SELECT 1", tail: "OFFSET /* skip */ 2 LIMIT 3 -- OFFSET 8"},
		{name: "nested and real", orig: "SELECT * FROM (SELECT 1 LIMIT 4) AS n LIMIT 2", inner: "SELECT * FROM (SELECT 1 LIMIT 4) AS n", tail: "LIMIT 2"},
		{name: "escape string and real", orig: `SELECT E'it\'s -- LIMIT 9' AS n LIMIT 2`, inner: `SELECT E'it\'s -- LIMIT 9' AS n`, tail: "LIMIT 2"},
		{name: "continued escape string", orig: continuedEscape + " LIMIT 2;", inner: continuedEscape, tail: "LIMIT 2"},
		{name: "continued escape through line comment", orig: "SELECT E'a' -- separator\n'it\\'s -- LIMIT 9' AS n LIMIT 2", inner: "SELECT E'a' -- separator\n'it\\'s -- LIMIT 9' AS n", tail: "LIMIT 2"},
		{name: "continued escape CRLF", orig: "SELECT e'界'\r\n'it\\'s' AS n OFFSET 1 LIMIT 2", inner: "SELECT e'界'\r\n'it\\'s' AS n", tail: "OFFSET 1 LIMIT 2"},
		{name: "repeated escape continuation", orig: "SELECT E'a'\n'b'\n'it\\'s' AS n LIMIT 2", inner: "SELECT E'a'\n'b'\n'it\\'s' AS n", tail: "LIMIT 2"},
		{name: "continued escape with control whitespace", orig: "SELECT E'a'\t\f\v\n'it\\'s' AS n LIMIT 2", inner: "SELECT E'a'\t\f\v\n'it\\'s' AS n", tail: "LIMIT 2"},
		{name: "continued escape with commented tail", orig: "SELECT E'a'\n'it\\'s' AS n -- LIMIT 9"},
		{name: "continued escape terminator and comment", orig: continuedEscape + " LIMIT 2; -- OFFSET 9", inner: continuedEscape, tail: "LIMIT 2  -- OFFSET 9"},
		{name: "newline in literal does not qualify", orig: "SELECT E'a\nb' 'it\\'s' AS n LIMIT 2"},
		{name: "previous gap newline does not qualify", orig: "SELECT E'a'\n'b' 'it\\'s' AS n LIMIT 2"},
		{name: "comma resets escape mode", orig: "SELECT E'a',\n'it\\'s' AS n LIMIT 2"},
		{name: "operator resets escape mode", orig: "SELECT E'a' ||\n'it\\'s' AS n LIMIT 2"},
		{name: "same line does not continue escape", orig: "SELECT E'a' 'it\\'s' AS n LIMIT 2"},
		{name: "block comment does not continue escape", orig: "SELECT E'a'\n/* separator */'it\\'s' AS n LIMIT 2"},
		{name: "CRLF and Unicode", orig: "SELECT '界' AS n -- OFFSET 9\r\nLIMIT 2", inner: "SELECT '界' AS n -- OFFSET 9", tail: "LIMIT 2"},
		{name: "CRLF within string", orig: "SELECT '界\r\nLIMIT 9' AS n LIMIT 2", inner: "SELECT '界\r\nLIMIT 9' AS n", tail: "LIMIT 2"},
		{name: "dollar string and real", orig: "SELECT $界_1$LIMIT 9$界_1$ AS n LIMIT 2", inner: "SELECT $界_1$LIMIT 9$界_1$ AS n", tail: "LIMIT 2"},
		{name: "comment semicolon preserved", orig: "SELECT 1 -- LIMIT 2;"},
		{name: "terminator before comment", orig: "SELECT 1 LIMIT 2; -- LIMIT 9", inner: "SELECT 1", tail: "LIMIT 2  -- LIMIT 9"},
		{name: "ordinary string backslash conservative", orig: `SELECT 'it\'s -- LIMIT 9' AS n LIMIT 2`},
		{name: "ordinary backslash terminator", orig: `SELECT 'C:\path' AS n;`, inner: `SELECT 'C:\path' AS n`},
		{name: "ordinary backslash and real", orig: `SELECT 'C:\path' AS n LIMIT 2`, inner: `SELECT 'C:\path' AS n`, tail: "LIMIT 2"},
		{name: "ordinary even backslashes", orig: `SELECT '\\' AS n LIMIT 2`, inner: `SELECT '\\' AS n`, tail: "LIMIT 2"},
		{name: "ambiguous backslash terminator", orig: `SELECT 'it\'s' AS n;`, inner: `SELECT 'it\'s' AS n`},
		{name: "parameter tail unchanged", orig: "SELECT 1 LIMIT $1"},
		{name: "unclosed block comment", orig: "SELECT 1 /* LIMIT 2"},
		{name: "unclosed dollar string", orig: "SELECT $tag$ LIMIT 2"},
		{name: "unclosed single string", orig: "SELECT ' LIMIT 2"},
		{name: "unclosed nesting", orig: "SELECT (SELECT 1 LIMIT 2"},
	} {
		for _, dir := range []sortDir{sortAsc, sortDesc} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, dir), func(t *testing.T) {
				inner := tc.inner
				if inner == "" {
					inner = tc.orig
				}
				keyword := "ASC"
				if dir == sortDesc {
					keyword = "DESC"
				}
				want := fmt.Sprintf("SELECT * FROM (\n%s\n) _pgsavvy_sort\nORDER BY 1 %s", inner, keyword)
				if tc.tail != "" {
					want += "\n" + tc.tail
				}
				if got := wrapSorted(tc.orig, 1, dir); got != want {
					t.Errorf("wrapSorted() = %q, want %q", got, want)
				}
				if got := wrapSorted(tc.orig, 1, sortClear); got != tc.orig {
					t.Errorf("clear changed original SQL: %q", got)
				}
			})
		}
	}
}

// TestWrapSorted_HoistsTrailingLimitOffset asserts that a trailing LIMIT/OFFSET
// is moved OUT of the inner derived table and re-applied AFTER the ORDER BY.
// Otherwise Postgres applies the inner LIMIT to an unordered scan first, sorting
// only an arbitrary subset (id=1 missing when browsing >LIMIT rows).
func TestWrapSorted_HoistsTrailingLimitOffset(t *testing.T) {
	tests := []struct {
		name    string
		orig    string
		ordinal int
		dir     sortDir
		want    string
	}{
		{
			name:    "table browse LIMIT hoisted after order by",
			orig:    `SELECT * FROM "app"."posts" LIMIT 100`,
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM \"app\".\"posts\"\n) _pgsavvy_sort\nORDER BY 1 ASC\nLIMIT 100",
		},
		{
			name:    "limit with offset hoisted together",
			orig:    "SELECT * FROM t LIMIT 50 OFFSET 10",
			ordinal: 2,
			dir:     sortDesc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 2 DESC\nLIMIT 50 OFFSET 10",
		},
		{
			name:    "offset before limit hoisted together",
			orig:    "SELECT * FROM t OFFSET 5 LIMIT 10",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 1 ASC\nOFFSET 5 LIMIT 10",
		},
		{
			name:    "offset only hoisted",
			orig:    "SELECT * FROM t OFFSET 5",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 1 ASC\nOFFSET 5",
		},
		{
			name:    "trailing semicolon stripped then limit hoisted",
			orig:    "SELECT * FROM t LIMIT 100;",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t\n) _pgsavvy_sort\nORDER BY 1 ASC\nLIMIT 100",
		},
		{
			name:    "lowercase limit hoisted preserving case",
			orig:    "select * from t limit 100",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nselect * from t\n) _pgsavvy_sort\nORDER BY 1 ASC\nlimit 100",
		},
		{
			name:    "LIMIT inside string literal is not hoisted",
			orig:    "SELECT * FROM t WHERE x='a LIMIT 1'",
			ordinal: 1,
			dir:     sortAsc,
			want:    "SELECT * FROM (\nSELECT * FROM t WHERE x='a LIMIT 1'\n) _pgsavvy_sort\nORDER BY 1 ASC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapSorted(tt.orig, tt.ordinal, tt.dir)
			if got != tt.want {
				t.Fatalf("wrapSorted() = %q, want %q", got, tt.want)
			}
		})
	}
}
