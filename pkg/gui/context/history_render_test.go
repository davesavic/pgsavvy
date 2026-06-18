package context

import (
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// TestFormatHistoryRow_SQLHighlighted confirms the SQL preview is run through
// the syntax highlighter in colour mode, so the query stands out from the
// trailing metadata (status glyph, relative time, duration).
func TestFormatHistoryRow_SQLHighlighted(t *testing.T) {
	defer theme.SetMonochromeForTest(false)()

	now := time.Unix(1_700_000_000, 0)
	row := query.HistoryRow{
		SQL:        "select 1",
		Succeeded:  true,
		ExecutedAt: now.Add(-time.Minute).UnixMilli(),
		DurationMS: 12,
	}
	got := formatHistoryRow(row, now)
	wantSQL := highlight.Highlight(truncateHistorySQL("select 1", historyDisplayWidth))
	if !strings.Contains(got, wantSQL) {
		t.Fatalf("row missing highlighted SQL\n got: %q\nwant substr: %q", got, wantSQL)
	}
}

// TestFormatHistoryRow_MonochromeLayoutStable pins the row layout under
// NO_COLOR: highlight.Highlight passes the SQL through verbatim, so the
// metadata columns stay byte-stable and the highlight change is invisible.
func TestFormatHistoryRow_MonochromeLayoutStable(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()

	now := time.Unix(1_700_000_000, 0)
	row := query.HistoryRow{
		SQL:        "select 1",
		Succeeded:  true,
		ExecutedAt: now.Add(-time.Minute).UnixMilli(),
		DurationMS: 12,
	}
	got := formatHistoryRow(row, now)
	want := "select 1 ✓ 1m ago  12ms"
	if got != want {
		t.Fatalf("formatHistoryRow\n got: %q\nwant: %q", got, want)
	}
}
