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

// TestAroundStatementStringLiteralMisplit pins the known limitation
// from the naive splitter: a ';' inside a string literal splits into
// two segments. The SQL-aware splitter (highlighter epic) fixes this.
func TestAroundStatementStringLiteralMisplit(t *testing.T) {
	b := bufFromLines(`SELECT ';';`)
	// Cursor at the start; the naive splitter sees ';' at col 8 as a
	// statement boundary, so the FIRST around-statement is "SELECT '"
	// plus the ';' at col 8 — range [0, 9).
	got, ok := AroundStatement(b, Position{Line: 0, Col: 3})
	if !ok {
		t.Fatalf("AroundStatement string-lit ok=false")
	}
	want := Range{Start: Position{Line: 0, Col: 0}, End: Position{Line: 0, Col: 9}}
	if got != want {
		t.Fatalf("AroundStatement string-lit = %+v, want %+v (known limitation)", got, want)
	}
}

// TestSplitStatementsDebugLogStringLiteralMisplit asserts that the
// package-level debug hook fires when SplitStatements observes a ';'
// inside an unterminated single-quote pair on a line.
func TestSplitStatementsDebugLogStringLiteralMisplit(t *testing.T) {
	var captured []string
	prev := DebugLog
	DebugLog = func(format string, args ...any) {
		captured = append(captured, format)
	}
	t.Cleanup(func() { DebugLog = prev; captured = nil })

	_ = SplitStatements("SELECT ';';")
	if len(captured) == 0 {
		t.Fatalf("DebugLog not invoked for string-literal mis-split")
	}
}

func TestSplitStatementsDebugLogQuietWhenBalanced(t *testing.T) {
	var captured []string
	prev := DebugLog
	DebugLog = func(format string, args ...any) {
		captured = append(captured, format)
	}
	t.Cleanup(func() { DebugLog = prev; captured = nil })

	_ = SplitStatements("SELECT 1; SELECT 2;")
	if len(captured) != 0 {
		t.Fatalf("DebugLog fired on clean input: %v", captured)
	}
}

// Sanity: the helper types build correctly (compile-time check).
func TestRangeShapes(t *testing.T) {
	var r Range
	if !reflect.DeepEqual(r, Range{}) {
		t.Fatal("zero Range != Range{}")
	}
}
