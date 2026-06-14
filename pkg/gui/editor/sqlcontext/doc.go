// Package sqlcontext is the detection brain for schema-aware SQL
// completion. Given a SQL buffer and a cursor rune offset it reports
// which clause the cursor sits in (SELECT / FROM / JOIN / WHERE / ON)
// and what kind of identifier is expected there (Tables / Columns /
// Both), so a completion source can decide what to offer.
//
// Detection is built entirely on top of highlight.Tokenize: the engine
// never re-lexes SQL itself and never imports Chroma. It is
// error-tolerant by construction — partial, unterminated, or otherwise
// malformed SQL yields a zero ContextResult (Clause=None, Expect=None)
// rather than a panic.
//
// Analysis is statement-scoped. The cursor's statement is isolated via
// the editor's StatementRangeAt/StatementAt before any clause walking,
// so a clause keyword in an earlier statement never leaks context into
// a cursor sitting in a later one.
//
// Scope: clause + expect detection, plus
// in-scope table/alias resolution, schema-qualified and quoted-identifier
// handling, and trailing dot-qualifier resolution (ContextResult.
// InScopeTables and ContextResult.Qualifier). FK-aware JOIN ranking,
// wiring into the completion SchemaSource,
// and CTE/subquery scopes (deferred) are out of
// scope and live in sibling tasks. This package exposes ContextResult,
// TableRef, Qualifier, and Analyze.
package sqlcontext
