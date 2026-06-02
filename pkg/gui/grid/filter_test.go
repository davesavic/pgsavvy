package grid

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// twoColView builds a view with two text columns and the supplied rows
// installed. Cursor starts at (0,0); column meta is text/text.
func twoColView(t *testing.T, rows [][]any) *View {
	t.Helper()
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "name", TypeName: "text"},
		{Name: "city", TypeName: "text"},
	})
	for _, r := range rows {
		v.AppendRows([]models.Row{{Values: r}})
	}
	return v
}

// TestSetFilter_CachesCompiledRegex pins the contract that a successful
// SetFilter call caches the compiled *regexp.Regexp under filterState.re.
// Subsequent SetFilter calls with the SAME src may or may not recompile;
// the AC pin is that the cached regex is non-nil after the call.
func TestSetFilter_CachesCompiledRegex(t *testing.T) {
	v := twoColView(t, nil)
	require.NoError(t, v.SetFilter("alice", false))
	v.mu.RLock()
	re := v.filterState.re
	v.mu.RUnlock()
	require.NotNil(t, re, "SetFilter should cache the compiled regex")
}

func TestSetFilter_InvalidRegex_LeavesPriorUnchanged(t *testing.T) {
	v := twoColView(t, nil)
	require.NoError(t, v.SetFilter("alice", false))
	priorActive := v.FilterActive()
	require.True(t, priorActive)

	err := v.SetFilter("[", false) // unterminated bracket
	require.Error(t, err)

	// Prior filter still installed.
	require.True(t, v.FilterActive(), "invalid regex must leave prior filter intact")
	v.mu.RLock()
	require.Equal(t, "alice", v.filterState.src)
	v.mu.RUnlock()
}

func TestSetFilter_RejectsOversizedPattern(t *testing.T) {
	v := twoColView(t, nil)
	v.SetFilterMaxRegexBytes(8)
	// First install a valid one to verify "prior unchanged".
	require.NoError(t, v.SetFilter("ok", false))

	big := strings.Repeat("a", 9)
	err := v.SetFilter(big, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too long")

	// Prior still installed.
	v.mu.RLock()
	require.Equal(t, "ok", v.filterState.src)
	v.mu.RUnlock()
}

func TestSetFilter_EmptyIsNoOp(t *testing.T) {
	v := twoColView(t, nil)
	require.NoError(t, v.SetFilter("alice", false))
	require.True(t, v.FilterActive())
	require.NoError(t, v.SetFilter("", false))
	// Filter unchanged: empty is treated as cancel.
	require.True(t, v.FilterActive(), "empty src must not alter filter state")
}

// TestProjection_FilterNoLongerExcludes pins dbsavvy-2ttm (T1): applyFilter
// is now identity, so an active filter no longer drops rows from the
// projection. (SetFilter still compiles; its row-hiding effect is gone.)
func TestProjection_FilterNoLongerExcludes(t *testing.T) {
	v := twoColView(t, [][]any{
		{"alice", "ny"},
		{"bob", "sf"},
		{"charlotte", "la"},
	})
	require.NoError(t, v.SetFilter("charlotte", false))
	snap := v.snapshot()
	indices := project(snap)
	require.Equal(t, []int{0, 1, 2}, indices, "applyFilter is identity: every row survives")
}

// TestProjection_AllColsNoLongerAffectsProjection pins that the allCols
// flag no longer changes the projection now that applyFilter is identity.
func TestProjection_AllColsNoLongerAffectsProjection(t *testing.T) {
	v := twoColView(t, [][]any{
		{"alice", "ny"},
		{"bob", "sfsf"},
	})
	require.NoError(t, v.SetFilter("sfsf", false))
	snap := v.snapshot()
	require.Equal(t, []int{0, 1}, project(snap), "identity projection: all rows present")

	v.ToggleFilterAllCols()
	snap = v.snapshot()
	require.Equal(t, []int{0, 1}, project(snap), "allCols no longer affects the projection")
}

func TestSetColumns_ClearsFilter(t *testing.T) {
	v := twoColView(t, nil)
	require.NoError(t, v.SetFilter("alice", false))
	require.True(t, v.FilterActive())

	v.SetColumns([]models.ColumnMeta{{Name: "x", TypeName: "text"}})
	require.False(t, v.FilterActive(), "SetColumns must clear any active filter")
}

func TestJumpNextMatch_WrapsAround(t *testing.T) {
	v := twoColView(t, [][]any{
		{"match", ""},
		{"miss", ""},
		{"match", ""},
	})
	require.NoError(t, v.SetFilter("match", false))
	// Start cursor at row 2 (last match); next must wrap to row 0.
	v.mu.Lock()
	v.cursorRow = 2
	v.mu.Unlock()

	v.JumpNextMatch()
	row, _ := v.CursorPosition()
	require.Equal(t, 0, row, "JumpNextMatch from last match must wrap to first")
}

func TestJumpPrevMatch_WrapsAround(t *testing.T) {
	v := twoColView(t, [][]any{
		{"match", ""},
		{"miss", ""},
		{"match", ""},
	})
	require.NoError(t, v.SetFilter("match", false))
	v.mu.Lock()
	v.cursorRow = 0
	v.mu.Unlock()

	v.JumpPrevMatch()
	row, _ := v.CursorPosition()
	require.Equal(t, 2, row, "JumpPrevMatch from first match must wrap to last")
}

func TestJumpNextMatch_NoOpWhenFilterInactive(t *testing.T) {
	v := twoColView(t, [][]any{
		{"a", ""},
		{"b", ""},
	})
	v.JumpNextMatch()
	row, _ := v.CursorPosition()
	require.Equal(t, 0, row, "no filter active → no cursor movement")
}

func TestJumpNextMatch_EmptyBufferNoOp(t *testing.T) {
	v := twoColView(t, nil)
	require.NoError(t, v.SetFilter("anything", false))
	// Must not panic / loop.
	v.JumpNextMatch()
}

func TestToggleFilterAllCols_NoOpWithoutFilter(t *testing.T) {
	v := twoColView(t, nil)
	v.ToggleFilterAllCols()
	v.mu.RLock()
	require.False(t, v.filterState.allCols, "phantom toggle without active filter")
	v.mu.RUnlock()
}

// TestSetFilter_NoDeadlockWithAppendRows exercises the contract pinned
// in the dbsavvy-uv0 amendments: SetFilter must acquire the view write
// lock briefly so concurrent AppendRows can keep running. The test runs
// both verbs in tight goroutines and asserts the test completes (via the
// implicit -timeout) and that all appended rows are present.
func TestSetFilter_NoDeadlockWithAppendRows(t *testing.T) {
	v := twoColView(t, nil)

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			batch := make([]models.Row, perGoroutine)
			for j := range batch {
				batch[j] = models.Row{Values: []any{"x", "y"}}
			}
			v.AppendRows(batch)
		}()
		go func(i int) {
			defer wg.Done()
			pattern := "x"
			if i%2 == 0 {
				pattern = "y"
			}
			_ = v.SetFilter(pattern, i%2 == 0)
		}(i)
	}
	wg.Wait()

	require.Equal(t, goroutines*perGoroutine, v.RowCount())
}
