package grid

import (
	"fmt"
	"regexp"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// filterState carries the in-grid /regex filter. The compiled regex is
// cached on SetFilter so subsequent renders reuse it; renders read the
// state via the snapshot (never from v.filterState directly) so a
// concurrent SetFilter cannot tear the draw mid-frame.
//
// The cursor (cursorRow) is a row-index into the raw v.rows buffer, NOT
// into the filtered projection. j/k navigation continues to walk raw
// rows even while a filter is active — only Render skips non-matching
// rows. JumpNextMatch / JumpPrevMatch are the dedicated cursor verbs that
// honor the filter. This is the deliberate simplest-first design for
// dbsavvy-uv0.4 (per CLAUDE.md "start with simplest solution"); T5+T6
// may revisit cursor mechanics once sort/hide compose with filter.
type filterState struct {
	src     string         // raw pattern; "" = inactive
	allCols bool           // when true, regex matches against ANY column's value
	re      *regexp.Regexp // compiled once on SetFilter
}

// defaultFilterMaxRegexBytes is the regex-source byte cap when the View
// is constructed without an explicit override. Mirrors the config
// default ui.filter_max_regex_bytes (4096).
const defaultFilterMaxRegexBytes = 4096

// SetFilterMaxRegexBytes installs the regex-source byte cap used by
// SetFilter. n <= 0 falls back to defaultFilterMaxRegexBytes. Wired from
// config at chord-registration time so a hot-reloaded config value takes
// effect on the next /regex invocation.
func (v *View) SetFilterMaxRegexBytes(n int) {
	if n <= 0 {
		n = defaultFilterMaxRegexBytes
	}
	v.mu.Lock()
	v.filterMaxRegexBytes = n
	v.mu.Unlock()
}

// SetFilter compiles src and installs it as the active filter.
//
// Empty src is treated as cancel: filter state is left unchanged and nil
// is returned. (Vim's "repeat last search" semantics are intentionally
// NOT implemented here — see dbsavvy-uv0 amendments.)
//
// A src longer than the configured cap returns an error containing
// "too long"; the prior filter state is unchanged.
//
// An invalid regex returns the regexp.Compile error; the prior filter
// state is unchanged.
//
// The regex is compiled exactly once per successful SetFilter call;
// subsequent renders reuse the cached *regexp.Regexp.
func (v *View) SetFilter(src string, allCols bool) error {
	if src == "" {
		return nil
	}
	// The unlock-relock around regex.Compile is intentional: compile is the
	// expensive step, and we don't want to hold the View lock while it runs.
	// filterMaxRegexBytes is effectively immutable at call time (wired once
	// at chord-registration), so reading it outside the install lock is safe.
	v.mu.Lock()
	cap := v.filterMaxRegexBytes
	if cap <= 0 {
		cap = defaultFilterMaxRegexBytes
	}
	v.mu.Unlock()
	if len(src) > cap {
		return fmt.Errorf("filter pattern too long (>%d bytes)", cap)
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.filterState = filterState{src: src, allCols: allCols, re: re}
	v.mu.Unlock()
	return nil
}

// ClearFilter drops any active filter so every loaded row renders again.
// No-op when no filter is active.
func (v *View) ClearFilter() {
	v.mu.Lock()
	v.filterState = filterState{}
	v.mu.Unlock()
}

// ToggleFilterAllCols flips the allCols flag of the currently active
// filter without recompiling the cached regex. No-op when no filter is
// active (matches the AC "<C-a> with no active filter is a no-op").
func (v *View) ToggleFilterAllCols() {
	v.mu.Lock()
	if v.filterState.re != nil {
		v.filterState.allCols = !v.filterState.allCols
	}
	v.mu.Unlock()
}

// FilterActive reports whether a filter is currently installed.
func (v *View) FilterActive() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.filterState.re != nil
}

// JumpNextMatch moves the cursor to the next row whose cell content
// matches the active filter, wrapping at the buffer tail. No-op when
// the filter is inactive or the buffer is empty.
//
// The walk and cursor mutation happen under the write lock — RWMutex
// does not support lock-upgrade, so we acquire write up front. The walk
// is bounded by len(rows) iterations to prevent any pathological stall.
func (v *View) JumpNextMatch() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.jumpMatchLocked(+1)
}

// JumpPrevMatch is the symmetric counterpart of JumpNextMatch.
func (v *View) JumpPrevMatch() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.jumpMatchLocked(-1)
}

// jumpMatchLocked walks the row buffer in the supplied direction from
// cursorRow+dir, wrapping at the boundaries, and lands the cursor on
// the first matching row. Caller must hold v.mu (write).
func (v *View) jumpMatchLocked(dir int) {
	if v.filterState.re == nil || len(v.rows) == 0 {
		return
	}
	n := len(v.rows)
	step := 1
	if dir < 0 {
		step = -1
	}
	for i := 1; i <= n; i++ {
		idx := (v.cursorRow + step*i) % n
		if idx < 0 {
			idx += n
		}
		if rowMatchesLocked(v.rows[idx], v.cols, v.filterState.re, v.filterState.allCols, v.cursorCol) {
			v.cursorRow = idx
			return
		}
	}
}

// rowMatchesLocked is the shared row-matching predicate used by the
// projection and the jump methods. When allCols is true a row matches
// if ANY column's stringified value matches the regex; otherwise only
// the column at cursorCol is tested. Out-of-range cursorCol falls back
// to matching against column 0 (defensive — cursor is normally bounded
// by SetColumns / move verbs).
func rowMatchesLocked(row models.Row, cols []models.ColumnMeta, re *regexp.Regexp, allCols bool, cursorCol int) bool {
	if re == nil {
		return true
	}
	if allCols {
		for c, col := range cols {
			var v any
			if c < len(row.Values) {
				v = row.Values[c]
			}
			s := renderCellPlain(v, col)
			if re.MatchString(s) {
				return true
			}
		}
		return false
	}
	target := cursorCol
	if target < 0 || target >= len(cols) {
		target = 0
	}
	if target >= len(cols) {
		return false
	}
	var v any
	if target < len(row.Values) {
		v = row.Values[target]
	}
	return re.MatchString(renderCellPlain(v, cols[target]))
}
