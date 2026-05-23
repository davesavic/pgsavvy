package editor

import (
	"context"
	"sort"
)

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

// AutoTriggerFromContext reports whether the cursor sits at a position
// that should auto-trigger the completion popup. Returns true when the
// line-up-to-cursor ends in one of:
//
//   - a trailing whitespace-terminated table-context keyword:
//     `FROM `, `JOIN `, `INNER JOIN `, `LEFT JOIN `, `RIGHT JOIN `,
//     `CROSS JOIN `, `UPDATE `, `INTO ` (case-insensitive)
//   - a trailing `<ident>.` (no whitespace before the dot)
//
// Detection delegates to the same regex matchers SchemaSource uses
// (reKeywordTable, reIdentDot) so behaviour stays consistent. Noise
// (string literals, comments, dollar-quoted strings) is stripped via
// stripNoise — auto-trigger does NOT fire inside a quoted string or
// comment.
//
// Nil buf is treated as "no context"; returns false.
//
// dbsavvy-bwq.22 (C5).
func AutoTriggerFromContext(buf *Buffer, pos Position) bool {
	if buf == nil {
		return false
	}
	line := lineUpToCursor(buf, pos)
	if line == "" {
		return false
	}
	stripped := stripNoise(line)
	if reIdentDot.MatchString(stripped) {
		return true
	}
	return reKeywordTable.MatchString(stripped)
}
