package sqlcontext

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
)

// scopeTables extracts the tables the statement brings into scope by
// scanning every FROM / JOIN clause for table references of the form
//
//	[schema.]table [[AS] alias]
//
// schema, table, and alias may each be a bare or a double-quoted
// identifier; quoted identifiers are stored unquoted with case
// preserved. An alias is recorded in the owning TableRef.Alias and is
// never emitted as a TableRef.Name — fixing the v1 bug where aliases
// polluted the table-name set.
//
// On a duplicate alias collision ("FROM a x JOIN b x") both TableRefs
// are still emitted (each table keeps its own alias); only the
// alias->table resolution used by qualifierAt is deterministic, and it
// is last-wins (the most recently parsed table bearing the alias). This
// rule is arbitrary but stable, matching the natural left-to-right scan.
func scopeTables(tokens []highlight.Token) []TableRef {
	var refs []TableRef
	for i := 0; i < len(tokens); i++ {
		if !isFromOrJoin(tokens[i]) {
			continue
		}
		ref, next, ok := parseTableRef(tokens, i+1)
		if ok {
			refs = append(refs, ref)
		}
		// Continue scanning from the token after the ref so a JOIN
		// keyword that follows is still picked up; comma-separated
		// FROM lists (rare in completion-time partial SQL) are not
		// chased here — out of scope.
		i = next - 1
	}
	return refs
}

// parseTableRef reads one table reference starting at token index start
// (the token after a FROM/JOIN keyword). It returns the parsed TableRef,
// the index just past the consumed tokens, and ok=false when no table
// name is present (e.g. "FROM " with nothing typed yet).
func parseTableRef(tokens []highlight.Token, start int) (TableRef, int, bool) {
	i := skipSpace(tokens, start)
	if i >= len(tokens) || !isIdentLike(tokens[i]) {
		return TableRef{}, i, false
	}

	var ref TableRef
	ref.Name = identValue(tokens[i])
	i++

	// Optional "schema.table": the first ident was the schema, the part
	// after the dot is the table.
	if j := skipSpace(tokens, i); j < len(tokens) && isDot(tokens[j]) {
		k := skipSpace(tokens, j+1)
		if k < len(tokens) && isIdentLike(tokens[k]) {
			ref.Schema = ref.Name
			ref.Name = identValue(tokens[k])
			i = k + 1
		}
	}

	// Optional alias, with or without an explicit AS.
	j := skipSpace(tokens, i)
	if j < len(tokens) && isAs(tokens[j]) {
		j = skipSpace(tokens, j+1)
	}
	if j < len(tokens) && isIdentLike(tokens[j]) {
		ref.Alias = identValue(tokens[j])
		i = j + 1
	}

	return ref, i, true
}

// qualifierAt resolves a trailing "<ident>." sitting immediately left of
// the cursor. It returns the zero (absent) Qualifier when the cursor is
// not directly after such a dot.
//
// Resolution looks the ident up first against in-scope aliases, then
// against in-scope bare table names. An unresolved ident (undeclared
// alias) yields Present=true with empty Table/Schema — never a panic and
// never a wrong table.
func qualifierAt(tokens []highlight.Token, cursor int, refs []TableRef) Qualifier {
	dot, ok := dotEndingAt(tokens, cursor)
	if !ok {
		return Qualifier{}
	}
	ident, ok := identEndingAt(tokens, dot.RuneOffset)
	if !ok {
		return Qualifier{}
	}

	name := identValue(ident)
	q := Qualifier{Present: true, Ident: name}
	if ref, found := resolve(refs, name); found {
		q.Table = ref.Name
		q.Schema = ref.Schema
	}
	return q
}

// resolve maps a dot-qualifier ident to its table: alias match wins,
// then a bare table-name match. Alias matching is last-wins so a
// duplicate alias resolves deterministically to the most recent table.
func resolve(refs []TableRef, ident string) (TableRef, bool) {
	var (
		hit   TableRef
		found bool
	)
	for _, r := range refs {
		if r.Alias == ident {
			hit, found = r, true // last-wins
		}
	}
	if found {
		return hit, true
	}
	for _, r := range refs {
		if r.Name == ident {
			return r, true
		}
	}
	return TableRef{}, false
}

// dotEndingAt returns the Punctuation "." token whose end is exactly at
// cursor, i.e. the cursor sits immediately after a dot.
func dotEndingAt(tokens []highlight.Token, cursor int) (highlight.Token, bool) {
	for _, tok := range tokens {
		if isDot(tok) && tok.RuneOffset+tok.RuneLen == cursor {
			return tok, true
		}
	}
	return highlight.Token{}, false
}

// identEndingAt returns the ident-like token whose end is exactly at off
// (the ident immediately left of a dot).
func identEndingAt(tokens []highlight.Token, off int) (highlight.Token, bool) {
	for _, tok := range tokens {
		if isIdentLike(tok) && tok.RuneOffset+tok.RuneLen == off {
			return tok, true
		}
	}
	return highlight.Token{}, false
}

// skipSpace advances past whitespace/other-noise tokens (but not idents,
// keywords, or punctuation) and returns the next significant index.
func skipSpace(tokens []highlight.Token, i int) int {
	for i < len(tokens) && isSpace(tokens[i]) {
		i++
	}
	return i
}

// isComma reports whether tok is a lone comma separator. Mirroring isDot, it
// matches by value rather than by kind to stay robust to lexer quirks.
func isComma(tok highlight.Token) bool { return tok.Value == "," }

// isTableSlotKeyword reports whether tok is a clause keyword that opens a
// table slot (FROM / JOIN / UPDATE / INTO) — one that expects a table name
// immediately after it.
func isTableSlotKeyword(tok highlight.Token) bool {
	if tok.Type != highlight.Keyword {
		return false
	}
	res, ok := clauseForKeyword(tok.Value)
	return ok && res.Expect == ExpectTables
}

// lastTableSlotOpener returns the index of the most recent token, ending at
// or before cursor, that opens a table slot: a FROM/JOIN/UPDATE/INTO keyword
// or a comma continuing the table list. Returns -1 when none precedes the
// cursor. A token the cursor sits inside (a half-typed keyword) is skipped so
// it never counts as a completed opener.
func lastTableSlotOpener(tokens []highlight.Token, cursor int) int {
	idx := -1
	for i, tok := range tokens {
		if tok.RuneOffset >= cursor {
			break
		}
		if tok.RuneOffset+tok.RuneLen > cursor {
			continue // cursor sits inside this (partial) token
		}
		if isTableSlotKeyword(tok) || isComma(tok) {
			idx = i
		}
	}
	return idx
}

// aliasSlot reports whether the cursor sits in the alias / trailing position
// of a FROM/JOIN/UPDATE/INTO table reference: a complete [schema.]table name
// has already been consumed since the last table-slot opener (keyword or
// comma) and the cursor has moved past it. There the user is naming an alias,
// so table suggestions are noise. It returns false while the table name is
// still being typed (cursor abutting the qualified-name chain), in a fresh
// slot before any name, or when no opener precedes the cursor.
func aliasSlot(tokens []highlight.Token, cursor int) bool {
	open := lastTableSlotOpener(tokens, cursor)
	if open < 0 {
		return false
	}
	i := skipSpace(tokens, open+1)
	if i >= len(tokens) || tokens[i].RuneOffset >= cursor || !isIdentLike(tokens[i]) {
		return false // nothing (or no identifier) typed yet -> fresh table slot
	}
	// Walk the [schema.]table dotted chain, tracking the end offset of the
	// last token that is part of the qualified name.
	nameEnd := tokens[i].RuneOffset + tokens[i].RuneLen
	i++
	for {
		d := skipSpace(tokens, i)
		if d >= len(tokens) || tokens[d].RuneOffset >= cursor || !isDot(tokens[d]) {
			break
		}
		n := skipSpace(tokens, d+1)
		if n >= len(tokens) || tokens[n].RuneOffset >= cursor || !isIdentLike(tokens[n]) {
			// Trailing dot ("app.") still counts as within the qualified name.
			nameEnd = tokens[d].RuneOffset + tokens[d].RuneLen
			break
		}
		nameEnd = tokens[n].RuneOffset + tokens[n].RuneLen
		i = n + 1
	}
	// Cursor past the table-name chain (whitespace/alias after it): alias slot.
	return cursor > nameEnd
}

func isFromOrJoin(tok highlight.Token) bool {
	if tok.Type != highlight.Keyword {
		return false
	}
	switch strings.ToUpper(tok.Value) {
	case "FROM", "JOIN":
		return true
	default:
		return false
	}
}

func isAs(tok highlight.Token) bool {
	return tok.Type == highlight.Keyword && strings.EqualFold(tok.Value, "AS")
}

// isDot reports whether tok is a lone "." separator. Chroma's PostgreSQL
// lexer does NOT classify a bare dot as Punctuation — it lexes "." as a
// Number (a dot can begin a float like ".5"), so this matches by value
// rather than by kind to stay robust to that quirk.
func isDot(tok highlight.Token) bool {
	return tok.Value == "."
}

// isIdentLike reports whether tok is a usable identifier: a bare
// Identifier token, or a quoted "..." identifier. Chroma classifies a
// double-quoted identifier as Other (not String), so we detect it by the
// leading quote rather than by kind.
func isIdentLike(tok highlight.Token) bool {
	if tok.Type == highlight.Identifier {
		return true
	}
	return isQuotedIdent(tok)
}

func isQuotedIdent(tok highlight.Token) bool {
	return strings.HasPrefix(tok.Value, `"`) && strings.HasSuffix(tok.Value, `"`) && len(tok.Value) >= 2
}

// isSpace reports whether tok is insignificant filler between meaningful
// tokens. Chroma emits whitespace as an Other-kind token; a quoted
// identifier is also Other, so guard against treating it as space.
func isSpace(tok highlight.Token) bool {
	if tok.Type != highlight.Other {
		return false
	}
	return strings.TrimSpace(tok.Value) == ""
}

// identValue returns the identifier's logical name: a quoted identifier
// stripped of its surrounding quotes with case preserved ("Orders" ->
// Orders), a bare identifier unchanged.
func identValue(tok highlight.Token) string {
	if isQuotedIdent(tok) {
		return strings.TrimSuffix(strings.TrimPrefix(tok.Value, `"`), `"`)
	}
	return tok.Value
}
