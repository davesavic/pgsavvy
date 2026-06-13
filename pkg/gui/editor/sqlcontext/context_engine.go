package sqlcontext

import (
	"strings"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/gui/editor/highlight"
)

// Clause identifies the SQL clause the cursor sits in. It is the
// governing major-clause keyword most recently seen before the cursor
// within the cursor's own statement.
type Clause int

const (
	// ClauseNone means no governing clause was found (empty input, a
	// cursor before any clause keyword, or a cursor inside noise).
	ClauseNone Clause = iota
	ClauseSELECT
	ClauseFROM
	ClauseJOIN
	ClauseWHERE
	ClauseON
)

// Expect describes what kind of identifier a completion source should
// offer at the cursor.
type Expect int

const (
	// ExpectNone means nothing schema-aware should be offered (e.g. the
	// cursor is inside a string/comment, or no clause governs it).
	ExpectNone Expect = iota
	// ExpectTables means table names are expected (FROM / JOIN).
	ExpectTables
	// ExpectColumns means column names are expected (SELECT / WHERE).
	ExpectColumns
	// ExpectBoth means either a table (to qualify a column) or a column
	// is expected (ON / USING join predicates).
	ExpectBoth
)

// TableRef is one table brought into scope by the cursor's statement
// (via FROM / JOIN). Name is the table's bare (unquoted) name, Schema
// its optional schema qualifier (empty when unqualified), and Alias the
// optional alias bound to it ("FROM users u" / "FROM users AS u"). A
// table with no alias has Alias == "". Quoted identifiers are stored
// unquoted with their case preserved ("Orders" -> Orders).
//
// An alias is never itself emitted as a TableRef.Name: the alias lives
// only in the Alias field of the table it labels.
type TableRef struct {
	Name   string
	Alias  string
	Schema string
}

// Qualifier describes a trailing "<ident>." immediately left of the
// cursor — the dot-qualified prefix a completion source uses to narrow
// columns to one table. It is populated only when the cursor sits
// directly after such a dot.
//
// Ident is the raw identifier text left of the dot (an alias or a table
// name), unquoted with case preserved. Table/Schema are the resolved
// table that Ident refers to, found by:
//  1. matching Ident against an in-scope alias, else
//  2. matching Ident against an in-scope table's bare name.
//
// When Ident matches neither (an undeclared alias such as "z."), Table
// and Schema are empty while Ident still carries the typed text, and
// Present is true. Present distinguishes "no dot qualifier at the
// cursor" (Present == false, the zero value) from "a dot qualifier whose
// alias could not be resolved" (Present == true, Table == "").
type Qualifier struct {
	Present bool
	Ident   string
	Table   string
	Schema  string
}

// ContextResult is the detection brain's output: the clause governing
// the cursor, what identifiers are expected there, the tables the
// cursor's statement brings into scope, and any trailing dot-qualifier
// at the cursor. The zero value (ClauseNone, ExpectNone, nil
// InScopeTables, absent Qualifier) is the safe "offer nothing" result
// returned for empty, malformed, or in-noise input.
type ContextResult struct {
	Clause        Clause
	Expect        Expect
	InScopeTables []TableRef
	Qualifier     Qualifier
}

// Analyze reports the SQL completion context at runeOffset within sql.
//
// It scopes analysis to the cursor's own statement (semicolon-delimited,
// semicolons inside strings/comments/dollar-quotes ignored), then walks
// the token stream up to the cursor and returns the clause governed by
// the most recent clause keyword plus the identifiers expected there.
//
// Analyze never panics on partial, unterminated, or otherwise malformed
// SQL: such input yields the zero ContextResult. A cursor inside an
// (possibly unterminated, possibly multi-line) string or comment also
// yields the zero ContextResult, so noise never false-triggers.
func Analyze(sql string, runeOffset int) ContextResult {
	total := utf8.RuneCountInString(sql)
	runeOffset = clamp(runeOffset, 0, total)

	start, end := statementRangeAt(sql, runeOffset)
	if start >= end {
		return ContextResult{}
	}

	// Translate the whole-buffer cursor offset into one relative to the
	// scoped statement substring, clamped to the statement span.
	cursor := clamp(runeOffset, start, end) - start

	runes := []rune(sql)
	stmt := string(runes[start:end])

	tokens := highlight.Tokenize(stmt)
	if cursorInNoise(tokens, cursor) {
		return ContextResult{}
	}

	result := clauseAt(tokens, cursor)
	result.InScopeTables = scopeTables(tokens)
	result.Qualifier = qualifierAt(tokens, cursor, result.InScopeTables)
	return result
}

// InNoise reports whether the cursor at runeOffset within sql sits inside
// a string literal or comment (an open OR closed one) — the positions
// where completion must stay silent because a trailing keyword/operator is
// quoted content, not a real clause position.
//
// Analysis is statement-scoped, mirroring Analyze: the cursor's own
// statement is isolated first, so a string opened in an earlier statement
// does not bleed into a later one. Unlike the single-line stripNoiseEx it
// replaces, this handles multi-line strings/comments: highlight.Tokenize
// collapses an unterminated (and/or multi-line) construct into one token
// spanning to end of input, so a cursor on a later line still inside the
// construct is correctly detected.
//
// InNoise never panics on partial, unterminated, or malformed SQL.
func InNoise(sql string, runeOffset int) bool {
	total := utf8.RuneCountInString(sql)
	runeOffset = clamp(runeOffset, 0, total)

	start, end := statementRangeAt(sql, runeOffset)
	if start >= end {
		return false
	}
	cursor := clamp(runeOffset, start, end) - start

	runes := []rune(sql)
	stmt := string(runes[start:end])

	return cursorInNoise(highlight.Tokenize(stmt), cursor)
}

// clauseAt walks the keyword tokens that END strictly before the cursor
// and returns the context implied by the most recent clause keyword.
//
// Requiring the keyword to end before the cursor (not merely begin
// before it) means a keyword the cursor is still inside — e.g. the lone
// "SEL|ECT" — does not govern, so a half-typed keyword yields the zero
// ContextResult rather than a premature trigger.
func clauseAt(tokens []highlight.Token, cursor int) ContextResult {
	result := ContextResult{}
	for _, tok := range tokens {
		if tok.RuneOffset >= cursor {
			break
		}
		if tok.Type != highlight.Keyword {
			continue
		}
		// Keyword must be complete (ended) before the cursor.
		if tok.RuneOffset+tok.RuneLen >= cursor {
			continue
		}
		if res, ok := clauseForKeyword(tok.Value); ok {
			result = res
		}
	}
	return result
}

// clauseForKeyword maps a clause keyword to its (Clause, Expect) and
// reports whether the keyword is clause-defining. Keywords that do not
// open or continue a recognised clause return ok=false, leaving the
// previously-seen clause in force.
func clauseForKeyword(kw string) (ContextResult, bool) {
	switch strings.ToUpper(kw) {
	case "SELECT":
		return ContextResult{Clause: ClauseSELECT, Expect: ExpectColumns}, true
	case "FROM", "INTO", "UPDATE":
		return ContextResult{Clause: ClauseFROM, Expect: ExpectTables}, true
	case "JOIN":
		return ContextResult{Clause: ClauseJOIN, Expect: ExpectTables}, true
	case "WHERE", "AND", "OR":
		return ContextResult{Clause: ClauseWHERE, Expect: ExpectColumns}, true
	case "ON", "USING":
		return ContextResult{Clause: ClauseON, Expect: ExpectBoth}, true
	default:
		return ContextResult{}, false
	}
}

// cursorInNoise reports whether cursor sits inside a String or Comment
// token. Because highlight.Tokenize collapses an unterminated (and/or
// multi-line) string or comment into a single token spanning to the end
// of input, this reliably suppresses completion inside noise without any
// line-based stripping. A cursor exactly at a noise token's start is not
// yet inside it; a cursor anywhere within (offset, offset+len] is.
func cursorInNoise(tokens []highlight.Token, cursor int) bool {
	for _, tok := range tokens {
		if tok.Type != highlight.String && tok.Type != highlight.Comment {
			continue
		}
		if cursor > tok.RuneOffset && cursor <= tok.RuneOffset+tok.RuneLen {
			return true
		}
	}
	return false
}

// statementRangeAt returns the [start, end) rune-offset boundaries of
// the statement containing runeOff, mirroring the editor's
// StatementRangeAt but implemented locally over highlight.Tokenize to
// keep sqlcontext free of an editor import (avoids a future cycle when
// the completion engine in package editor depends on sqlcontext).
func statementRangeAt(buf string, runeOff int) (start, end int) {
	runeCount := utf8.RuneCountInString(buf)
	runeOff = clamp(runeOff, 0, runeCount)

	semis := semicolonRuneOffsets(highlight.Tokenize(buf))
	if len(semis) == 0 {
		return 0, runeCount
	}

	start = 0
	end = runeCount
	for _, s := range semis {
		if s < runeOff {
			start = s + 1
		}
		if s >= runeOff {
			end = s
			break
		}
	}
	return start, end
}

// semicolonRuneOffsets returns the rune offsets of semicolons that act
// as statement boundaries — i.e. Punctuation-token semicolons, which
// excludes any ';' lexed inside a string, comment, or dollar-quoted
// block. Chroma may coalesce adjacent punctuation, so each Punctuation
// token is scanned for ';'.
func semicolonRuneOffsets(tokens []highlight.Token) []int {
	var offsets []int
	for _, tok := range tokens {
		if tok.Type != highlight.Punctuation {
			continue
		}
		for i, r := range []rune(tok.Value) {
			if r == ';' {
				offsets = append(offsets, tok.RuneOffset+i)
			}
		}
	}
	return offsets
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
