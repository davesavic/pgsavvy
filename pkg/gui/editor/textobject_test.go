package editor

import (
	"reflect"
	"testing"
)

func TestInnerQuoteDouble(t *testing.T) {
	b := bufFromLines(`SELECT "hello" FROM x`)
	got, ok := InnerQuote(b, Position{Line: 0, Col: 10}, '"')
	if !ok {
		t.Fatalf("InnerQuote ok=false; want true")
	}
	want := Range{Start: Position{Line: 0, Col: 8}, End: Position{Line: 0, Col: 13}}
	if got != want {
		t.Fatalf("InnerQuote = %+v, want %+v", got, want)
	}
}

func TestAroundQuoteDouble(t *testing.T) {
	b := bufFromLines(`SELECT "hello" FROM x`)
	got, ok := AroundQuote(b, Position{Line: 0, Col: 10}, '"')
	if !ok {
		t.Fatalf("AroundQuote ok=false; want true")
	}
	want := Range{Start: Position{Line: 0, Col: 7}, End: Position{Line: 0, Col: 14}}
	if got != want {
		t.Fatalf("AroundQuote = %+v, want %+v", got, want)
	}
}

func TestInnerQuoteNoSurroundReturnsFalse(t *testing.T) {
	b := bufFromLines(`SELECT 1`)
	if _, ok := InnerQuote(b, Position{Line: 0, Col: 3}, '"'); ok {
		t.Fatalf("InnerQuote ok=true; want false (no quotes)")
	}
}

func TestInnerQuoteSingle(t *testing.T) {
	b := bufFromLines(`SELECT 'abc'`)
	got, ok := InnerQuote(b, Position{Line: 0, Col: 8}, '\'')
	if !ok {
		t.Fatalf("InnerQuote single ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 8}, End: Position{Line: 0, Col: 11}}
	if got != want {
		t.Fatalf("InnerQuote single = %+v, want %+v", got, want)
	}
}

func TestInnerParen(t *testing.T) {
	b := bufFromLines(`f(a, b, c)`)
	got, ok := InnerParen(b, Position{Line: 0, Col: 4})
	if !ok {
		t.Fatalf("InnerParen ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 2}, End: Position{Line: 0, Col: 9}}
	if got != want {
		t.Fatalf("InnerParen = %+v, want %+v", got, want)
	}
}

func TestAroundParen(t *testing.T) {
	b := bufFromLines(`f(a, b, c)`)
	got, ok := AroundParen(b, Position{Line: 0, Col: 4})
	if !ok {
		t.Fatalf("AroundParen ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 1}, End: Position{Line: 0, Col: 10}}
	if got != want {
		t.Fatalf("AroundParen = %+v, want %+v", got, want)
	}
}

func TestInnerParenNested(t *testing.T) {
	b := bufFromLines(`f(a, g(b, c), d)`)
	// Cursor inside the inner g(b, c).
	got, ok := InnerParen(b, Position{Line: 0, Col: 8})
	if !ok {
		t.Fatalf("InnerParen nested ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 7}, End: Position{Line: 0, Col: 11}}
	if got != want {
		t.Fatalf("InnerParen nested = %+v, want %+v", got, want)
	}
}

func TestInnerParenUnmatchedReturnsFalse(t *testing.T) {
	b := bufFromLines(`f(a, b`)
	if _, ok := InnerParen(b, Position{Line: 0, Col: 3}); ok {
		t.Fatalf("InnerParen unmatched ok=true; want false")
	}
}

func TestInnerBracket(t *testing.T) {
	b := bufFromLines(`arr[1, 2]`)
	got, ok := InnerBracket(b, Position{Line: 0, Col: 5})
	if !ok {
		t.Fatalf("InnerBracket ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 4}, End: Position{Line: 0, Col: 8}}
	if got != want {
		t.Fatalf("InnerBracket = %+v, want %+v", got, want)
	}
}

func TestInnerBraces(t *testing.T) {
	b := bufFromLines(`{a, b}`)
	got, ok := InnerBraces(b, Position{Line: 0, Col: 2})
	if !ok {
		t.Fatalf("InnerBraces ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 1}, End: Position{Line: 0, Col: 5}}
	if got != want {
		t.Fatalf("InnerBraces = %+v, want %+v", got, want)
	}
}

func TestInnerParenMultiLine(t *testing.T) {
	b := bufFromLines(`SELECT (`, `  a,`, `  b`, `)`)
	got, ok := InnerParen(b, Position{Line: 1, Col: 2})
	if !ok {
		t.Fatalf("InnerParen multiline ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 8}, End: Position{Line: 3, Col: 0}}
	if got != want {
		t.Fatalf("InnerParen multiline = %+v, want %+v", got, want)
	}
}

func TestInnerParagraph(t *testing.T) {
	b := bufFromLines(`line a`, `line b`, ``, `line c`, `line d`)
	got, ok := InnerParagraph(b, Position{Line: 0, Col: 0})
	if !ok {
		t.Fatalf("InnerParagraph ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 1, Col: 6}}
	if got != want {
		t.Fatalf("InnerParagraph = %+v, want %+v", got, want)
	}
}

func TestInnerParagraphNoBlankLinesReturnsWhole(t *testing.T) {
	b := bufFromLines(`a`, `b`, `c`)
	got, ok := InnerParagraph(b, Position{Line: 1, Col: 0})
	if !ok {
		t.Fatalf("InnerParagraph whole ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 2, Col: 1}}
	if got != want {
		t.Fatalf("InnerParagraph whole = %+v, want %+v", got, want)
	}
}

func TestAroundParagraphIncludesTrailingBlanks(t *testing.T) {
	b := bufFromLines(`line a`, `line b`, ``, ``, `line c`)
	got, ok := AroundParagraph(b, Position{Line: 0, Col: 0})
	if !ok {
		t.Fatalf("AroundParagraph ok=false")
	}
	if got.Start.Line != 0 || got.End.Line != 3 {
		t.Fatalf("AroundParagraph end-line = %d, want 3", got.End.Line)
	}
}

func TestInnerStatementSingleSegment(t *testing.T) {
	b := bufFromLines(`SELECT 1; SELECT 2;`)
	got, ok := InnerStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("InnerStatement ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 8}}
	if got != want {
		t.Fatalf("InnerStatement = %+v, want %+v", got, want)
	}
}

func TestInnerStatementSecondSegment(t *testing.T) {
	b := bufFromLines(`SELECT 1; SELECT 2;`)
	got, ok := InnerStatement(b, Position{Line: 0, Col: 12})
	if !ok {
		t.Fatalf("InnerStatement 2nd ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 9}, End: Position{Line: 0, Col: 18}}
	if got != want {
		t.Fatalf("InnerStatement 2nd = %+v, want %+v", got, want)
	}
}

func TestInnerStatementOnSemicolonResolvesPreceding(t *testing.T) {
	b := bufFromLines(`SELECT 1; SELECT 2;`)
	got, ok := InnerStatement(b, Position{Line: 0, Col: 8})
	if !ok {
		t.Fatalf("InnerStatement on-; ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 8}}
	if got != want {
		t.Fatalf("InnerStatement on-; = %+v, want %+v", got, want)
	}
}

func TestAroundStatementIncludesTrailingSemicolon(t *testing.T) {
	b := bufFromLines(`SELECT 1; SELECT 2;`)
	got, ok := AroundStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("AroundStatement ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 9}}
	if got != want {
		t.Fatalf("AroundStatement = %+v, want %+v", got, want)
	}
}

func TestAroundStatementIncludesLeadingWhitespace(t *testing.T) {
	b := bufFromLines(`SELECT 1; SELECT 2;`)
	got, ok := AroundStatement(b, Position{Line: 0, Col: 12})
	if !ok {
		t.Fatalf("AroundStatement leading-ws ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 9}, End: Position{Line: 0, Col: 19}}
	if got != want {
		t.Fatalf("AroundStatement leading-ws = %+v, want %+v", got, want)
	}
}

func TestAroundStatementNoSemicolonReturnsWhole(t *testing.T) {
	b := bufFromLines(`SELECT 1`)
	got, ok := AroundStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("AroundStatement no-; ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 8}}
	if got != want {
		t.Fatalf("AroundStatement no-; = %+v, want %+v", got, want)
	}
}

// TestAroundStatementStringLiteral verifies that a ';' inside a
// string literal is NOT treated as a statement boundary. The
// Chroma-token-aware splitter correctly returns the whole statement.
func TestAroundStatementStringLiteral(t *testing.T) {
	b := bufFromLines(`SELECT ';';`)
	got, ok := AroundStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("AroundStatement string-lit ok=false")
	}
	// The whole "SELECT ';'" plus trailing ';' — range [0, 11).
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 11}}
	if got != want {
		t.Fatalf("AroundStatement string-lit = %+v, want %+v", got, want)
	}
}

// TestInnerStatementStringLiteral verifies inner-statement with a
// semicolon inside a string literal.
func TestInnerStatementStringLiteral(t *testing.T) {
	b := bufFromLines(`SELECT ';';`)
	got, ok := InnerStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("InnerStatement string-lit ok=false")
	}
	// The inner statement is "SELECT ';'" — range [0, 10).
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 10}}
	if got != want {
		t.Fatalf("InnerStatement string-lit = %+v, want %+v", got, want)
	}
}

// Sanity: the helper types build correctly (compile-time check).
func TestRangeShapes(t *testing.T) {
	var r Range
	if !reflect.DeepEqual(r, Range{}) {
		t.Fatal("zero Range != Range{}")
	}
}

func TestInnerAroundWord(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		col   int
		fn    func(*Buffer, Position) (Range, bool)
		want  Range
		notOk bool
	}{
		{"iw mid-word", "foo bar baz", 5, InnerWord, rng(0, 4, 0, 7), false},
		{"aw word trailing space", "foo bar baz", 5, AroundWord, rng(0, 4, 0, 8), false},
		{"aw word at line end uses leading space", "foo bar", 5, AroundWord, rng(0, 3, 0, 7), false},
		{"iw on whitespace", "foo bar", 3, InnerWord, rng(0, 3, 0, 4), false},
		{"aw on whitespace grows to next word", "foo bar", 3, AroundWord, rng(0, 3, 0, 7), false},
		{"iw on punctuation run", "a+b", 1, InnerWord, rng(0, 1, 0, 2), false},
		{"iw stops at punctuation", "foo.bar", 1, InnerWord, rng(0, 0, 0, 3), false},
		{"iW spans punctuation", "foo.bar baz", 5, InnerWORD, rng(0, 0, 0, 7), false},
		{"aW word trailing space", "foo.bar baz", 1, AroundWORD, rng(0, 0, 0, 8), false},
		{"append slot clamps to last rune", "foo", 3, InnerWord, rng(0, 0, 0, 3), false},
		{"single-char word at start", "a bc", 0, InnerWord, rng(0, 0, 0, 1), false},
		{"empty line returns false", "", 0, InnerWord, Range{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := bufFromLines(tt.line)
			got, ok := tt.fn(b, Position{Line: 0, Col: tt.col})
			if tt.notOk {
				if ok {
					t.Fatalf("ok=true; want false")
				}
				return
			}
			if !ok {
				t.Fatalf("ok=false; want true")
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("= %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInnerWordEmptyBuffer(t *testing.T) {
	if _, ok := InnerWord(&Buffer{}, Position{Line: 0, Col: 0}); ok {
		t.Fatalf("InnerWord on empty buffer ok=true; want false")
	}
}

func rng(sl, sc, el, ec int) Range {
	return Range{Start: Position{Line: sl, Col: sc}, End: Position{Line: el, Col: ec}}
}
