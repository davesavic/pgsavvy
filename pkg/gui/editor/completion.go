package editor

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/gui/editor/sqlcontext"
)

// maxTriggerResults bounds the deduped suggestion set that Engine.Trigger
// returns. Without it, an empty matcher pattern (editor.Match("", x) is a
// match-all on manual <c-x><c-o>) lets every schema/keyword/function
// candidate flood the merge, scoring sort and downstream render. The cap
// is applied AFTER dedupe so the comparator is unchanged; it simply
// truncates the already-ranked tail. 200 is chosen generously: far above
// any popup window or realistic useful set, so it never changes existing
// test outcomes (their candidate sets are tiny) yet still bounds the
// flood on large schemas (Finding C).
const maxTriggerResults = 200

// Engine merges Suggestions from a list of Sources, sorts by score +
// source priority, and de-duplicates by Text. The dedupe keeps the
// first occurrence in the sorted order, so the highest-scoring (and
// on ties, highest-priority-source) entry wins.
//
// Engine is goroutine-safe only in the sense that Trigger does not
// mutate its sources slice — concurrent Trigger calls are safe, but
// AddSource must not race with Trigger.
type Engine struct {
	sources []Source
}

// NewEngine constructs an Engine wrapping the supplied sources. A nil
// or empty slice is permitted; Trigger then returns an empty slice.
func NewEngine(sources []Source) *Engine {
	if len(sources) == 0 {
		return &Engine{}
	}
	cp := make([]Source, len(sources))
	copy(cp, sources)
	return &Engine{sources: cp}
}

// AddSource appends s to the source list. Nil is silently dropped so
// callers do not need to guard.
func (e *Engine) AddSource(s Source) {
	if s == nil {
		return
	}
	e.sources = append(e.sources, s)
}

// Trigger collects Suggestions from every wired source, sorts them by
// Score desc (tiebreak: producing-source Priority desc), then dedupes
// by Text keeping the first occurrence per sort order. Returns a
// non-nil (possibly empty) slice — callers can range over the result
// without a nil guard.
//
// ctx is passed verbatim to each source. A canceled ctx is the
// source's responsibility to honour; Engine does not short-circuit on
// it because the per-source Suggest call is expected to be cheap and
// synchronous in MVP.
func (e *Engine) Trigger(ctx context.Context, buf *Buffer, pos Position) []Suggestion {
	if len(e.sources) == 0 {
		return []Suggestion{}
	}
	// Build a Name -> Priority lookup for tiebreak. Source ordering
	// in e.sources is the authoritative tiebreaker when two sources
	// declare the same priority — earlier source wins.
	prio := make(map[string]int, len(e.sources))
	order := make(map[string]int, len(e.sources))
	for i, s := range e.sources {
		name := s.Name()
		// First-write-wins so two sources with the same name don't
		// stomp on each other (the second is treated as a duplicate
		// registration; its suggestions still flow in).
		if _, ok := prio[name]; !ok {
			prio[name] = s.Priority()
			order[name] = i
		}
	}
	var merged []Suggestion
	for _, s := range e.sources {
		got := s.Suggest(ctx, buf, pos)
		if len(got) == 0 {
			continue
		}
		merged = append(merged, got...)
	}
	if len(merged) == 0 {
		return []Suggestion{}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		pi, pj := prio[merged[i].Source], prio[merged[j].Source]
		if pi != pj {
			return pi > pj
		}
		// Stable tiebreak on registration order so dedupe is
		// deterministic when two sources share priority and score.
		return order[merged[i].Source] < order[merged[j].Source]
	})
	seen := make(map[string]struct{}, len(merged))
	out := make([]Suggestion, 0, len(merged))
	for _, s := range merged {
		if _, dup := seen[s.Text]; dup {
			continue
		}
		seen[s.Text] = struct{}{}
		out = append(out, s)
	}
	// Top-N cap (Finding C): truncate the already-ranked, deduped tail.
	// This is not a comparator change — the sort order above is
	// untouched; we only bound how much of it we return.
	if len(out) > maxTriggerResults {
		out = out[:maxTriggerResults]
	}
	return out
}

// Sources returns the engine's source list. Returned slice is a copy;
// mutating it does not affect the Engine.
func (e *Engine) Sources() []Source {
	if len(e.sources) == 0 {
		return nil
	}
	cp := make([]Source, len(e.sources))
	copy(cp, e.sources)
	return cp
}

// minPrefixTriggerRunes is the identifier-prefix width (in runes) at or
// above which AutoTriggerFromContext opens the popup outside the
// keyword/dot/column gates. dbsavvy-ko4m.6.1 broadens the auto-trigger
// from "only at FROM/JOIN/UPDATE/INTO or `<ident>.`" to "anywhere the
// cursor sits at the end of a >=2-rune identifier prefix". The fuzzy
// quality floor (dbsavvy-ko4m.3) trims the broadened firing so it does
// not flood the popup. 2 is the threshold: a single typed letter is too
// noisy (matches almost everything), two is the smallest prefix that
// usefully narrows.
const minPrefixTriggerRunes = 2

// AutoTriggerFromContext reports whether the cursor sits at a position
// that should auto-trigger the completion popup. Returns true when either:
//
//   - the cursor sits in a structured schema-completable position
//     (FROM/JOIN/UPDATE/INTO, `<ident>.`, ON/USING, or a SELECT/WHERE
//     column context) — see IsSchemaCompletableContext, OR
//   - the cursor sits at the end of a >=2-rune identifier prefix
//     (dbsavvy-ko4m.6.1) anywhere outside the structured contexts above,
//     provided the cursor is not inside a string/comment. The fuzzy
//     quality floor (dbsavvy-ko4m.3) keeps the broadened firing from
//     flooding.
//
// The structured detection routes through the sqlcontext engine
// (no regexes). The broadened prefix gate measures the prefix in runes
// via identifierPrefixAt (the same scan acceptSuggestion mirrors), so a
// quoted-identifier prefix like `"Co` yields `Co` (the scan stops at the
// quote) and still qualifies. Noise is suppressed via sqlcontext.InNoise,
// which — unlike the single-line stripper it replaces — honours multi-line
// strings/comments.
//
// Nil buf is treated as "no context"; returns false.
//
// dbsavvy-bwq.22 (C5); broadened in dbsavvy-ko4m.6.1; engine-backed in
// dbsavvy-ko4m.1.3.
func AutoTriggerFromContext(buf *Buffer, pos Position) bool {
	if IsSchemaCompletableContext(buf, pos) {
		return true
	}
	if buf == nil {
		return false
	}
	// Broadened gate (dbsavvy-ko4m.6.1): a >=2-rune identifier prefix at the
	// cursor opens the popup outside the structured contexts above, but must
	// NOT fire inside a string/comment. The narrow gate above already returns
	// false in noise; re-check here so the prefix gate honours it too.
	sql, off := bufferTextAndOffset(buf, pos)
	if sqlcontext.InNoise(sql, off) {
		return false
	}
	return utf8.RuneCountInString(identifierPrefixAt(buf, pos)) >= minPrefixTriggerRunes
}

// IsSchemaCompletableContext reports whether the cursor sits in a
// "structured" position where schema completion (tables/columns) is the
// relevant suggestion set: a trailing table-context keyword (FROM/JOIN/
// UPDATE/INTO), a `<ident>.`, a JOIN ON/USING condition, or an unqualified
// column context (SELECT/WHERE/AND/comparison). This is the NARROW gate —
// it does NOT include the broadened >=2-rune identifier-prefix trigger that
// AutoTriggerFromContext adds on top.
//
// It exists so HistorySource can keep deferring to schema completion in
// exactly these positions (whole-statement history is noise there) without
// being suppressed everywhere by the broadened auto-trigger gate. Detection
// routes through sqlcontext.Analyze: the position is schema-completable when
// the engine reports a non-None Expect (FROM/JOIN→Tables, SELECT/WHERE→
// Columns, ON/USING→Both) or a trailing dot-qualifier. A cursor in noise
// yields the zero ContextResult, so this returns false there. Nil buf
// returns false.
//
// dbsavvy-ko4m.6.1 (extracted from AutoTriggerFromContext); engine-backed
// in dbsavvy-ko4m.1.3.
func IsSchemaCompletableContext(buf *Buffer, pos Position) bool {
	if buf == nil {
		return false
	}
	res, ok := schemaContextAt(buf, pos)
	if !ok {
		return false
	}
	if res.Qualifier.Present {
		return true
	}
	return res.Expect != sqlcontext.ExpectNone
}

// IsIdentDotContext reports whether the cursor sits immediately after an
// `<ident>.` (optionally followed by a partial column name) — the column
// completion trigger. Unlike AutoTriggerFromContext it matches ONLY the
// dot form, not the FROM/JOIN/SELECT keyword gates. It lets an explicit
// `.` keystroke re-open the popup even when the post-accept suppression is
// armed: typing `.` right after accepting a table must still show columns.
//
// Detection routes through sqlcontext via schemaContextAt and reports the
// engine's trailing dot-qualifier (Qualifier.Present). schemaContextAt backs
// the cursor off any partial column the user has begun typing, so both
// `users.` and `users.cr` are recognised as dot contexts. Nil buf is
// treated as "no context"; returns false.
func IsIdentDotContext(buf *Buffer, pos Position) bool {
	if buf == nil {
		return false
	}
	res, ok := schemaContextAt(buf, pos)
	if !ok {
		return false
	}
	return res.Qualifier.Present
}

// schemaContextAt analyzes the cursor's SQL context, transparently handling
// the dot-qualifier-with-partial case. The engine sets Qualifier only when
// the cursor sits immediately after a `<ident>.`; a partial column typed
// after the dot (`users.cr`) would otherwise hide the qualifier. So when the
// direct analysis finds no qualifier but an identifier prefix abuts the
// cursor, schemaContextAt re-analyzes at the dot (cursor minus the prefix)
// and, if THAT carries a qualifier, returns it. The returned ContextResult
// is the one whose Qualifier/Expect callers should act on; ok is false only
// for an empty buffer.
func schemaContextAt(buf *Buffer, pos Position) (sqlcontext.ContextResult, bool) {
	sql, off := bufferTextAndOffset(buf, pos)
	if sql == "" {
		return sqlcontext.ContextResult{}, false
	}
	res := sqlcontext.Analyze(sql, off)
	if res.Qualifier.Present {
		return res, true
	}
	// Back the cursor off a partial identifier to surface a dot-qualifier
	// the partial is masking ("users.cr" -> analyze at the dot).
	prefixLen := utf8.RuneCountInString(identifierPrefixAt(buf, pos))
	if prefixLen == 0 || prefixLen >= off {
		return res, true
	}
	if atDot := sqlcontext.Analyze(sql, off-prefixLen); atDot.Qualifier.Present {
		return atDot, true
	}
	return res, true
}

// bufferTextAndOffset returns the buffer's full newline-joined text and the
// cursor's flat rune offset within it, computed under a single LinesCopy
// snapshot so the pair is consistent and lock-safe. An out-of-range pos
// clamps to the nearest valid offset.
func bufferTextAndOffset(buf *Buffer, pos Position) (string, int) {
	lines := buf.LinesCopy()
	if len(lines) == 0 {
		return "", 0
	}
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(string(l.Runes))
	}

	line := pos.Line
	if line < 0 {
		return sb.String(), 0
	}
	if line >= len(lines) {
		line = len(lines) - 1
		pos.Col = len(lines[line].Runes)
	}
	off := 0
	for i := 0; i < line; i++ {
		off += len(lines[i].Runes) + 1 // +1 for the joining '\n'
	}
	col := min(max(pos.Col, 0), len(lines[line].Runes))
	off += col
	return sb.String(), off
}

// AnalyzeContextAt runs the SQL completion-context engine
// (sqlcontext.Analyze) at the cursor pos within buf and returns the
// resulting ContextResult. It is the exported seam acceptSuggestion uses
// to decide, at accept time, whether the cursor sits in a table context
// (ContextResult.Expect == ExpectTables) and which aliases are already in
// scope. A nil/empty buffer yields the zero ContextResult. dbsavvy-ko4m.6.2.
func AnalyzeContextAt(buf *Buffer, pos Position) sqlcontext.ContextResult {
	if buf == nil {
		return sqlcontext.ContextResult{}
	}
	sql, off := bufferTextAndOffset(buf, pos)
	if sql == "" {
		return sqlcontext.ContextResult{}
	}
	return sqlcontext.Analyze(sql, off)
}
