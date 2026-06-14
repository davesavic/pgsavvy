package grid

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// searchState carries the in-grid plain-substring SEARCH.
// Search never hides rows — it produces a
// cell-major match list with UTF-8 byte offsets into each cell's
// renderCellPlain output, and drives n/N cursor navigation across cells
// in reading order. The match list is recomputed under the write lock on
// SetSearch and copied defensively into the snapshot under RLock so a
// held render frame can't tear while a concurrent SetSearch / Next / Prev
// mutates the live slice.
//
// "active" is defined purely as query != "" — a query with zero matches
// is still active (the status bar shows 0/0).
type searchState struct {
	query     string // raw query; "" = inactive
	smartCase bool   // true => case-sensitive (query has an uppercase rune)
	matches   []cellMatch
	current   int // index into matches; 0 when there are no matches
}

// cellMatch locates a single substring hit within one cell. row/col are
// indices into the loaded buffer (raw row index, raw column index).
// byteStart/byteEnd are byte offsets into the EXACT renderCellPlain
// output for that cell (post capCellBytes + post SanitizeCellEscapes) so
// the highlight pass (T2) can slice the rendered string directly.
type cellMatch struct {
	row       int
	col       int
	byteStart int
	byteEnd   int
}

// SetSearch installs query as the active search, recomputing the
// cell-major match list over ALL loaded rows × ALL columns. Smart-case
// is derived from query (case-sensitive iff query contains an uppercase
// rune — vim/ripgrep semantics). The current match is set to the first
// match at/after the current (cursorRow, cursorCol) in reading order, or
// 0 when no match follows. When at least one match exists the cursor is
// moved onto the current match cell.
//
// An empty query clears the search (inactive). The match list is
// recomputed entirely under the write lock; snapshot()/Render are never
// invoked while the lock is held (mirrors SetFilter's discipline) so a
// concurrent AppendRows cannot deadlock.
func (v *View) SetSearch(query string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if query == "" {
		v.searchState = searchState{}
		return
	}
	smartCase := queryIsCaseSensitive(query)
	matches := computeMatchesLocked(v.rows, v.cols, query, smartCase)
	current := firstMatchAtOrAfter(matches, v.cursorRow, v.cursorCol)
	v.searchState = searchState{
		query:     query,
		smartCase: smartCase,
		matches:   matches,
		current:   current,
	}
	v.moveCursorToCurrentMatchLocked()
}

// ClearSearch drops any active search so SearchActive reports false and
// the match list is emptied. No-op when no search is active.
func (v *View) ClearSearch() {
	v.mu.Lock()
	v.searchState = searchState{}
	v.mu.Unlock()
}

// SearchActive reports whether a search query is installed. Active is
// defined as query != "" — a query that matched zero cells is still
// active (so the status bar can render "0/0").
func (v *View) SearchActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.searchState.query != ""
}

// NextMatch advances the current match by one (wrapping at the tail) and
// moves the cursor onto that match's cell. Zero matches is a no-op; a
// single match leaves the cursor where it is. Stale matches (against a
// buffer that has since shrunk) are skipped via the bounds-check in
// moveCursorToCurrentMatchLocked.
func (v *View) NextMatch() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stepMatchLocked(+1)
}

// PrevMatch is the symmetric counterpart of NextMatch.
func (v *View) PrevMatch() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.stepMatchLocked(-1)
}

// stepMatchLocked advances v.searchState.current by dir with wrap and
// moves the cursor onto the new current match. Caller holds v.mu (write).
func (v *View) stepMatchLocked(dir int) {
	n := len(v.searchState.matches)
	if n == 0 {
		return
	}
	v.searchState.current = ((v.searchState.current+dir)%n + n) % n
	v.moveCursorToCurrentMatchLocked()
}

// moveCursorToCurrentMatchLocked positions the cursor on the cell of the
// current match, bounds-checking against the CURRENT buffer dimensions
// (rows may have shrunk via SetColumns/AppendRows since the match list
// was built). When the current match is stale it is left in place and
// the cursor is not moved. Caller holds v.mu (write).
func (v *View) moveCursorToCurrentMatchLocked() {
	if v.searchState.current < 0 || v.searchState.current >= len(v.searchState.matches) {
		return
	}
	m := v.searchState.matches[v.searchState.current]
	if m.row < 0 || m.row >= len(v.rows) {
		return
	}
	if m.col < 0 || m.col >= len(v.cols) {
		return
	}
	v.cursorRow = m.row
	v.cursorCol = m.col
	// Search-jump is a cursor motion like j/k/G — drive the live-follow
	// callback so the relationship panel follows search-jump and n/N too.
	v.fireCursorChangeLocked()
}

// SearchStatus reports the search state for the status bar. cur is the
// 1-based index of the current match (0 when total == 0); total is the
// match count; active is query != "".
func (v *View) SearchStatus() (query string, cur, total int, active bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	total = len(v.searchState.matches)
	cur = 0
	if total > 0 {
		cur = v.searchState.current + 1
	}
	return v.searchState.query, cur, total, v.searchState.query != ""
}

// copyMatches returns a fresh allocation holding the same matches as in,
// so a snapshot held across a frame can't tear when SetSearch / Next /
// Prev replace the live slice under v.mu. Returns nil for an empty input.
func copyMatches(in []cellMatch) []cellMatch {
	if len(in) == 0 {
		return nil
	}
	out := make([]cellMatch, len(in))
	copy(out, in)
	return out
}

// computeMatchesLocked builds the cell-major match list in reading order
// (row ascending, then col ascending) over every loaded cell. Each cell
// is stringified via renderCellPlain so byte offsets align exactly with
// what the highlight pass will render. Caller holds v.mu.
func computeMatchesLocked(rows []models.Row, cols []models.ColumnMeta, query string, caseSensitive bool) []cellMatch {
	out := make([]cellMatch, 0)
	for r := range rows {
		row := rows[r]
		for c := range cols {
			var val any
			if c < len(row.Values) {
				val = row.Values[c]
			}
			cell := renderCellPlain(val, cols[c])
			for _, span := range substringMatches(cell, query, caseSensitive) {
				out = append(out, cellMatch{
					row:       r,
					col:       c,
					byteStart: span[0],
					byteEnd:   span[1],
				})
			}
		}
	}
	return out
}

// firstMatchAtOrAfter returns the index of the first match whose
// (row, col) is at or after (fromRow, fromCol) in reading order, or 0
// when no such match exists (so navigation starts from the top). matches
// is assumed already in reading order.
func firstMatchAtOrAfter(matches []cellMatch, fromRow, fromCol int) int {
	for i, m := range matches {
		if m.row > fromRow || (m.row == fromRow && m.col >= fromCol) {
			return i
		}
	}
	return 0
}

// queryIsCaseSensitive applies smart-case: the search is case-sensitive
// iff the query contains at least one uppercase rune (vim/ripgrep).
func queryIsCaseSensitive(query string) bool {
	for _, r := range query {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// substringMatches returns every [start,end) byte range in orig where
// query matches, under the case mode. The offsets are valid in orig
// (the original renderCellPlain string), NOT in any lowercased copy.
//
// Case-insensitive matching folds both sides with strings.ToLower for
// the comparison only; because ToLower is not guaranteed byte-length
// preserving for all runes, we scan orig rune-by-rune and compare a
// folded window against the folded query so the recorded offsets always
// point at original bytes. Matches do not overlap (the scan advances
// past each hit), which suffices for highlight rendering.
func substringMatches(orig, query string, caseSensitive bool) [][2]int {
	if query == "" {
		return nil
	}
	if caseSensitive {
		return literalMatches(orig, query)
	}
	return foldedMatches(orig, strings.ToLower(query))
}

// literalMatches finds non-overlapping byte ranges of query in orig via
// strings.Index — the fast path when no case folding is needed.
func literalMatches(orig, query string) [][2]int {
	var out [][2]int
	from := 0
	for {
		idx := strings.Index(orig[from:], query)
		if idx < 0 {
			return out
		}
		start := from + idx
		end := start + len(query)
		out = append(out, [2]int{start, end})
		from = end
	}
}

// foldedMatches finds non-overlapping ranges in orig that match
// lowerQuery (already lowercased) under case folding, returning byte
// offsets into orig. It scans candidate start positions on rune
// boundaries and folds the matched window with strings.ToLower for the
// comparison so multibyte runes whose lowercase form differs in byte
// length still yield original-string offsets.
func foldedMatches(orig, lowerQuery string) [][2]int {
	var out [][2]int
	for i := 0; i < len(orig); {
		end, ok := foldedMatchAt(orig, i, lowerQuery)
		if !ok {
			_, size := utf8.DecodeRuneInString(orig[i:])
			i += size
			continue
		}
		out = append(out, [2]int{i, end})
		i = end
	}
	return out
}

// foldedMatchAt reports whether lowerQuery matches orig starting at byte
// offset start (under case folding), returning the exclusive end byte
// offset in orig when it does. It consumes orig runes until the folded
// prefix equals lowerQuery, comparing one folded rune at a time so a
// multibyte rune whose lowercase changes byte length stays offset-safe.
func foldedMatchAt(orig string, start int, lowerQuery string) (int, bool) {
	q := lowerQuery
	pos := start
	for len(q) > 0 {
		if pos >= len(orig) {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(orig[pos:])
		folded := string(unicode.ToLower(r))
		if !strings.HasPrefix(q, folded) {
			return 0, false
		}
		q = q[len(folded):]
		pos += size
	}
	return pos, true
}
