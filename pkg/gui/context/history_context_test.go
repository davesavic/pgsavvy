package context

import (
	"strings"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/query"
)

func newTestHistory(rows []query.HistoryRow, drv types.GuiDriver) *HistoryContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.HISTORY,
		ViewName: string(types.HISTORY),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	c := NewHistoryContext(base, deps)
	if rows != nil {
		c.SetRows(rows)
	}
	return c
}

func sampleRows() []query.HistoryRow {
	return []query.HistoryRow{
		{ID: 3, ExecutedAt: 1000, SQL: "SELECT 3", DurationMS: 12, Succeeded: true},
		{ID: 2, ExecutedAt: 900, SQL: "SELECT\n2", DurationMS: 1500, Succeeded: false},
		{ID: 1, ExecutedAt: 800, SQL: "SELECT 1", DurationMS: 5, Succeeded: true},
	}
}

func TestHistoryContext_Identity(t *testing.T) {
	c := newTestHistory(nil, &captureDriver{})
	if got := c.GetKind(); got != types.TEMPORARY_POPUP {
		t.Errorf("kind = %v, want TEMPORARY_POPUP", got)
	}
	if got := c.GetKey(); got != types.HISTORY {
		t.Errorf("key = %v, want HISTORY", got)
	}
	if types.HISTORY.IsEditable() {
		t.Errorf("HISTORY must not be editable")
	}
}

func TestHistoryContext_SetRowsResetsCursor(t *testing.T) {
	c := newTestHistory(sampleRows(), &captureDriver{})
	c.SetCursor(2)
	c.SetRows(sampleRows())
	if c.Cursor() != 0 {
		t.Errorf("cursor = %d after SetRows; want 0", c.Cursor())
	}
	// Newest-first window: first row is the one the caller put first.
	sel, ok := c.Selected()
	if !ok || sel.ID != 3 {
		t.Errorf("Selected after SetRows = (%+v, %v); want ID 3, true", sel, ok)
	}
}

func TestHistoryContext_RenderOneLinePerRow(t *testing.T) {
	drv := &captureDriver{}
	c := newTestHistory(sampleRows(), drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.HISTORY) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.HISTORY))
	}
	lines := strings.Split(drv.lastContent, "\n")
	if len(lines) != 3 {
		t.Fatalf("rendered %d lines, want 3; body=%q", len(lines), drv.lastContent)
	}
	// Multi-line SQL must collapse to a single rendered line.
	if strings.Contains(lines[1], "\n") {
		t.Errorf("row line still multi-line: %q", lines[1])
	}
	if !strings.Contains(lines[1], historyReturnGlyph) {
		t.Errorf("multi-line SQL not collapsed to glyph: %q", lines[1])
	}
	// Cursor row (0) carries the marker; others do not.
	if !strings.HasPrefix(lines[0], "> ") {
		t.Errorf("cursor row lacks marker: %q", lines[0])
	}
	if strings.HasPrefix(lines[1], "> ") {
		t.Errorf("non-cursor row has marker: %q", lines[1])
	}
	// Glyphs reflect per-row success.
	if !strings.Contains(lines[0], historyOKGlyph) {
		t.Errorf("succeeded row missing ok glyph: %q", lines[0])
	}
	if !strings.Contains(lines[1], historyFailGlyph) {
		t.Errorf("failed row missing fail glyph: %q", lines[1])
	}
	// Duration is rendered.
	if !strings.Contains(lines[0], "12ms") {
		t.Errorf("row missing duration: %q", lines[0])
	}
}

func TestHistoryContext_EmptyState(t *testing.T) {
	drv := &captureDriver{}
	c := newTestHistory(nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.lastContent != historyEmptyLine {
		t.Errorf("empty body = %q, want %q", drv.lastContent, historyEmptyLine)
	}
	if _, ok := c.Selected(); ok {
		t.Errorf("Selected on empty list returned ok=true")
	}
}

func TestHistoryContext_SelectedAtCursor(t *testing.T) {
	c := newTestHistory(sampleRows(), &captureDriver{})
	c.SetCursor(1)
	sel, ok := c.Selected()
	if !ok || sel.ID != 2 {
		t.Errorf("Selected = (%+v, %v); want ID 2, true", sel, ok)
	}
}

func TestHistoryContext_CursorClampsNoPanic(t *testing.T) {
	c := newTestHistory(sampleRows(), &captureDriver{})
	c.SetCursor(-5)
	if c.Cursor() != 0 {
		t.Errorf("cursor = %d after under-clamp; want 0", c.Cursor())
	}
	c.SetCursor(99)
	if c.Cursor() != 2 {
		t.Errorf("cursor = %d after over-clamp; want 2", c.Cursor())
	}
	// Empty list: any move snaps to 0 without panic.
	c.SetRows(nil)
	c.SetCursor(10)
	if c.Cursor() != 0 {
		t.Errorf("cursor = %d on empty list; want 0", c.Cursor())
	}
}

func TestHistoryContext_NilDriverNoPanic(t *testing.T) {
	c := newTestHistory(sampleRows(), nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}

func TestHistoryContext_Items(t *testing.T) {
	c := newTestHistory(sampleRows(), &captureDriver{})
	items := c.Items()
	if len(items) != 3 {
		t.Fatalf("Items len = %d, want 3", len(items))
	}
	row, ok := items[0].(query.HistoryRow)
	if !ok || row.ID != 3 {
		t.Errorf("Items[0] = %#v, want HistoryRow ID 3", items[0])
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.UnixMilli(1_000_000_000_000)
	cases := []struct {
		name string
		then time.Time
		want string
	}{
		{"future is just now", now.Add(time.Hour), "just now"},
		{"sub minute", now.Add(-30 * time.Second), "just now"},
		{"minutes", now.Add(-3 * time.Minute), "3m ago"},
		{"hours", now.Add(-2 * time.Hour), "2h ago"},
		{"days", now.Add(-5 * 24 * time.Hour), "5d ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRelativeTime(now, tc.then); got != tc.want {
				t.Errorf("formatRelativeTime = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatHistoryDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0ms"},
		{12, "12ms"},
		{999, "999ms"},
		{1000, "1.0s"},
		{1500, "1.5s"},
		{12345, "12.3s"},
	}
	for _, tc := range cases {
		if got := formatHistoryDuration(tc.ms); got != tc.want {
			t.Errorf("formatHistoryDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}
}

func TestTruncateHistorySQL(t *testing.T) {
	if got := truncateHistorySQL("a\nb", 80); got != "a"+historyReturnGlyph+"b" {
		t.Errorf("newline collapse = %q", got)
	}
	long := strings.Repeat("x", 100)
	got := truncateHistorySQL(long, 10)
	if []rune(got)[len([]rune(got))-1] != '…' {
		t.Errorf("truncated string should end with ellipsis: %q", got)
	}
	if n := len([]rune(got)); n != 10 {
		t.Errorf("truncated rune width = %d, want 10", n)
	}
}
