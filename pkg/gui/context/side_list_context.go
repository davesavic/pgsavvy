package context

// SideListContext is the shared base for the five SIDE_CONTEXT panels
// (Connections, Schemas, Tables, Columns, Indexes). It carries the
// cursor index and the row slice; concrete side contexts swap their row
// type via SetItems and read selection via SelectedItem.
//
// SideListContext stores items as []any rather than a generic type
// parameter to keep the IBaseContext interface footprint flat (Go 1.25
// supports generics but the rest of pkg/gui/types and the test fakes
// treat contexts uniformly via IBaseContext, which itself is not
// generic). Concrete contexts type-assert in their HandleRender pass.
type SideListContext struct {
	BaseContext

	deps Deps

	cursor int
	items  []any

	// search carries the rail search engine state (dbsavvy-ioaj). Single
	// threaded on the UI thread (worker SetItems is marshaled onto it per
	// dbsavvy-zt9), so it needs no lock.
	search railSearchState
	// nameOf extracts the searchable name from a raw items[] element. nil
	// until a search-enabled rail injects one via SetRailNameAccessor; the
	// two search-enabled rails (Schemas, Tables) always set it, so a nil
	// nameOf simply yields zero matches (defensive, not a feature).
	nameOf func(any) string
	// visible reports whether the raw row index is currently visible. nil
	// means every row is visible. Hidden rows never match.
	visible func(rowIndex int) bool
}

// RailMatch locates one substring hit on a rail row. RowIndex is the RAW
// items[] index (NOT a visible-compacted index) so T2's renderRows, which
// iterates by raw index, can look matches up directly. ByteStart/ByteEnd
// index the (sanitized) name string exactly.
type RailMatch struct {
	RowIndex  int
	ByteStart int
	ByteEnd   int
}

// railSearchState is the per-rail search engine state. "active" is defined
// purely as query != "" — a query with zero matches is still active (the
// status bar shows 0/0). Mirrors grid/search.go:searchState at 1D.
type railSearchState struct {
	query     string // raw query; "" = inactive
	smartCase bool   // true => case-sensitive (query has an uppercase rune)
	matches   []RailMatch
	current   int // index into matches; 0 when there are no matches
}

// Deps is the per-context view of types.ContextTreeDeps. Aliased locally
// so concrete contexts don't repeat the long type name; it's
// always-by-value because the hook fields inside are themselves pointers
// (func values, interface).
type Deps = depsAlias

// NewSideListContext constructs a SideListContext with a BaseContext
// already wired. Concrete side contexts embed by value and forward to
// this constructor.
func NewSideListContext(base BaseContext, deps Deps) SideListContext {
	return SideListContext{
		BaseContext: base,
		deps:        deps,
	}
}

// SetItems replaces the row slice. Items beyond the new length silently
// clamp the cursor; callers refreshing the data after a delete do not
// need to reset the cursor manually.
func (s *SideListContext) SetItems(items []any) {
	// Repopulate is the single choke point for stale-offset invalidation:
	// match byte offsets index the prior name strings and must not survive.
	s.search = railSearchState{}
	s.items = items
	if s.cursor >= len(items) {
		if len(items) == 0 {
			s.cursor = 0
		} else {
			s.cursor = len(items) - 1
		}
	}
}

// Items returns the current row slice (read-only view; do not mutate).
func (s *SideListContext) Items() []any { return s.items }

// Cursor returns the current cursor index. May be 0 when items is empty.
func (s *SideListContext) Cursor() int { return s.cursor }

// SetCursor moves the cursor, clamping into the valid range. A move
// against an empty list snaps to 0.
func (s *SideListContext) SetCursor(i int) {
	if len(s.items) == 0 {
		s.cursor = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= len(s.items) {
		i = len(s.items) - 1
	}
	s.cursor = i
}

// SelectedItem returns the item under the cursor, or nil when the list
// is empty.
func (s *SideListContext) SelectedItem() any {
	if len(s.items) == 0 || s.cursor < 0 || s.cursor >= len(s.items) {
		return nil
	}
	return s.items[s.cursor]
}

// SetRailNameAccessor injects the name extractor used by the search
// engine. The two search-enabled rails (Schemas, Tables) set this from
// their constructors; rails that never search leave it nil and the engine
// produces zero matches.
func (s *SideListContext) SetRailNameAccessor(fn func(any) string) {
	s.nameOf = fn
}

// SetRailVisible injects a visibility predicate keyed by RAW items[] index.
// Hidden rows never match and are never landed on. nil means all visible.
func (s *SideListContext) SetRailVisible(fn func(rowIndex int) bool) {
	s.visible = fn
}

// SetSearch installs query as the active search, recomputing the match
// list over every visible row by raw index. Smart-case is derived from the
// query (case-sensitive iff it contains an uppercase rune). The current
// match is set to the first match at/after the cursor, or 0 when none
// follows; when at least one match exists the cursor moves onto it. An
// empty query clears the search and leaves the cursor unmoved. A query with
// zero matches stays active with the cursor unmoved. Mirrors
// grid/search.go:SetSearch at 1D.
func (s *SideListContext) SetSearch(query string) {
	if query == "" {
		s.search = railSearchState{}
		return
	}
	smartCase := railQueryIsCaseSensitive(query)
	matches := s.computeMatches(query, smartCase)
	current := railFirstMatchAtOrAfter(matches, s.cursor)
	s.search = railSearchState{
		query:     query,
		smartCase: smartCase,
		matches:   matches,
		current:   current,
	}
	s.moveCursorToCurrentMatch()
}

// computeMatches builds the rail match list in ascending raw-row order,
// skipping hidden rows and guarding a nil name accessor.
func (s *SideListContext) computeMatches(query string, caseSensitive bool) []RailMatch {
	if s.nameOf == nil {
		return nil
	}
	out := make([]RailMatch, 0)
	for i := range s.items {
		if s.visible != nil && !s.visible(i) {
			continue
		}
		for _, span := range railSubstringMatches(s.nameOf(s.items[i]), query, caseSensitive) {
			out = append(out, RailMatch{
				RowIndex:  i,
				ByteStart: span[0],
				ByteEnd:   span[1],
			})
		}
	}
	return out
}

// moveCursorToCurrentMatch positions the cursor on the current match's row.
// No-op when there are zero matches or the index is out of range.
func (s *SideListContext) moveCursorToCurrentMatch() {
	if s.search.current < 0 || s.search.current >= len(s.search.matches) {
		return
	}
	s.SetCursor(s.search.matches[s.search.current].RowIndex)
}

// NextMatch advances the current match by one (wrapping at the tail) and
// moves the cursor onto it. Zero matches is a no-op.
func (s *SideListContext) NextMatch() { s.stepMatch(+1) }

// PrevMatch is the symmetric counterpart of NextMatch.
func (s *SideListContext) PrevMatch() { s.stepMatch(-1) }

// stepMatch advances current by dir with wrap and moves the cursor.
func (s *SideListContext) stepMatch(dir int) {
	n := len(s.search.matches)
	if n == 0 {
		return
	}
	s.search.current = ((s.search.current+dir)%n + n) % n
	s.moveCursorToCurrentMatch()
}

// ClearSearch drops any active search.
func (s *SideListContext) ClearSearch() { s.search = railSearchState{} }

// SearchActive reports whether a search query is installed (query != "").
// A query that matched zero rows is still active.
func (s *SideListContext) SearchActive() bool { return s.search.query != "" }

// SearchStatus reports the search state for the status bar. cur is the
// 1-based index of the current match (0 when total == 0); total is the
// match count; active is query != "".
func (s *SideListContext) SearchStatus() (query string, cur, total int, active bool) {
	total = len(s.search.matches)
	cur = 0
	if total > 0 {
		cur = s.search.current + 1
	}
	return s.search.query, cur, total, s.search.query != ""
}

// Matches returns the current match slice. Single-threaded on the UI
// thread; callers must not mutate it.
func (s *SideListContext) Matches() []RailMatch { return s.search.matches }
