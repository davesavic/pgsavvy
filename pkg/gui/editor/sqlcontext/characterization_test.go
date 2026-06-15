package sqlcontext

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
)

// TestSQLContextCharacterization pins the highlight.Tokenize behaviour
// the engine relies on. It runs the lexer over the exact inputs the
// five regexes in completion_schema_source.go handle (FROM/JOIN tables,
// SELECT/WHERE/AND/OR columns, comparison-operator columns, ON/USING
// join conditions, and the open string/comment noise cases) and asserts
// the emitted token-Type sequence is rich enough to reconstruct the
// clause + expect context WITHOUT re-lexing.
//
// This test is intentionally written and committed BEFORE the Analyze
// engine logic: if Tokenize ever stops distinguishing Keyword from
// Identifier, or stops collapsing an unterminated string/comment into a
// single trailing String/Comment token, the engine's assumptions break
// and this characterization fails first.
func TestSQLContextCharacterization(t *testing.T) {
	// kinds extracts the ordered TokenKind sequence, dropping the
	// whitespace tokens highlight emits as Other so the assertions read
	// against the meaningful lexical shape.
	kinds := func(text string) []highlight.TokenKind {
		var out []highlight.TokenKind
		for _, tok := range highlight.Tokenize(text) {
			if tok.Type == highlight.Other { // whitespace
				continue
			}
			out = append(out, tok.Type)
		}
		return out
	}

	K := highlight.Keyword
	I := highlight.Identifier
	P := highlight.Punctuation
	O := highlight.Operator
	S := highlight.String
	C := highlight.Comment
	N := highlight.Number

	cases := []struct {
		name string
		sql  string
		want []highlight.TokenKind
		note string
	}{
		{
			name: "reKeywordTable: FROM then partial table",
			sql:  "SELECT * FROM us",
			want: []highlight.TokenKind{K, O, K, I},
			note: "FROM is a Keyword, the partial table is an Identifier — sufficient to know Expect=Tables.",
		},
		{
			name: "reKeywordTable: trailing FROM, no partial",
			sql:  "SELECT * FROM ",
			want: []highlight.TokenKind{K, O, K},
			note: "FROM is the last Keyword before the cursor — Expect=Tables with no partial.",
		},
		{
			name: "reColumnContext: SELECT then partial column",
			sql:  "SELECT na",
			want: []highlight.TokenKind{K, I},
			note: "SELECT Keyword governs; partial column is an Identifier — Expect=Columns.",
		},
		{
			name: "reColumnContext: mid column list",
			sql:  "SELECT id, na",
			want: []highlight.TokenKind{K, I, P, I},
			note: "The comma Punctuation lets the engine see we are still in the SELECT list — Expect=Columns mid-list.",
		},
		{
			name: "reColumnContext: WHERE then comparison operator",
			sql:  "WHERE col1 = ",
			want: []highlight.TokenKind{K, I, O},
			note: "WHERE Keyword governs; the trailing = is an Operator — Expect=Columns.",
		},
		{
			name: "reJoinCondition: ON trailing",
			sql:  "SELECT * FROM users u JOIN orders o ON ",
			want: []highlight.TokenKind{K, O, K, I, I, K, I, I, K},
			note: "ON is the last Keyword before the cursor — Expect=Both.",
		},
		{
			name: "reFromJoinTables: FROM/JOIN keywords are distinguishable",
			sql:  "SELECT * FROM users JOIN orders",
			want: []highlight.TokenKind{K, O, K, I, K, I},
			note: "Both FROM and JOIN surface as Keyword tokens; in-scope tables are the following Identifiers.",
		},
		{
			name: "noise: unterminated string collapses to one trailing String",
			sql:  "SELECT * FROM users WHERE col1 = 'oops",
			want: []highlight.TokenKind{K, O, K, I, K, I, O, S},
			note: "The open quote becomes a single trailing String token; a cursor inside it must yield Expect=None.",
		},
		{
			name: "noise: open block comment collapses to one trailing Comment",
			sql:  "SELECT /* still open",
			want: []highlight.TokenKind{K, C},
			note: "The open /* becomes a single trailing Comment token spanning to EOF; a cursor inside it yields Expect=None.",
		},
		{
			name: "number literal stays distinct from identifiers",
			sql:  "SELECT 1",
			want: []highlight.TokenKind{K, N},
			note: "A numeric literal is a Number, not an Identifier — it is not a column candidate.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(tc.sql)
			if len(got) != len(tc.want) {
				t.Fatalf("%s\n  sql:  %q\n  want kinds %v\n  got  kinds %v\n  note: %s",
					tc.name, tc.sql, tc.want, got, tc.note)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%s: token %d kind mismatch\n  sql:  %q\n  want %v\n  got  %v\n  note: %s",
						tc.name, i, tc.sql, tc.want, got, tc.note)
				}
			}
		})
	}
}

// TestSQLContextCharacterizationOpenNoiseSpansToEOF verifies the precise
// property the engine uses to suppress false triggers: an unterminated
// string or block comment is emitted as ONE token whose span reaches the
// end of input, so "cursor offset falls inside a String/Comment token"
// is a reliable in-noise test (token types, not line-based stripping).
func TestSQLContextCharacterizationOpenNoiseSpansToEOF(t *testing.T) {
	cases := []struct {
		sql  string
		kind highlight.TokenKind
	}{
		{"SELECT * FROM users WHERE x = 'open string\nspanning lines", highlight.String},
		{"SELECT col /* open comment\nspanning lines", highlight.Comment},
	}

	for _, tc := range cases {
		toks := highlight.Tokenize(tc.sql)
		if len(toks) == 0 {
			t.Fatalf("%q: expected tokens, got none", tc.sql)
		}
		last := toks[len(toks)-1]
		if last.Type != tc.kind {
			t.Fatalf("%q: last token kind = %d, want %d", tc.sql, last.Type, tc.kind)
		}
		end := last.RuneOffset + last.RuneLen
		total := len([]rune(tc.sql))
		if end != total {
			t.Fatalf("%q: trailing %d token spans to %d, want EOF %d", tc.sql, tc.kind, end, total)
		}
	}
}
