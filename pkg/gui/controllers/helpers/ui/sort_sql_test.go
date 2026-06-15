package ui

import (
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
