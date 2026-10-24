package context

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/stretchr/testify/require"
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
	want := "[tui succeeded] - | select 1 ✓ 1m ago  12ms"
	if got != want {
		t.Fatalf("formatHistoryRow\n got: %q\nwant: %q", got, want)
	}
}

func TestHistoryDisplay_ControlCharacters(t *testing.T) {
	for _, mono := range []bool{false, true} {
		t.Run(fmt.Sprintf("monochrome_%t", mono), func(t *testing.T) {
			defer theme.SetMonochromeForTest(mono)()
			for _, malicious := range []string{
				"SELECT\t'日本語'\r\n-- comment\x00\x7f\u0085\x1b[2J",
				"SELECT '日本語'\x1b]52;c;clipboard-payload\a",
				"SELECT '日本語'\x1b]8;;link-payload\x1b\\",
				"SELECT '日本語'\u009dosc-payload\u009c",
				"SELECT '日本語'\x1b]unterminated-payload",
				"SELECT '日本語'\x1bPdevice-payload\x1b\\",
			} {
				row := query.HistoryRow{SQL: malicious, Source: "cli\x1b[2J", Outcome: "blocked\x1b]bad-metadata\a", ConnectionID: "prod\t日本語\r\n\x00\u0085\x1b]52;c;secret-profile\a"}
				c := newTestHistory([]query.HistoryRow{row}, &captureDriver{})
				got := formatHistoryBody(c.rows, 0, time.Now())
				plain := regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(got, "")
				for _, r := range plain {
					require.False(t, r < 0x20 || (r >= 0x7f && r <= 0x9f), "untrusted control %U in %q", r, got)
				}
				require.Contains(t, plain, "[cli blocked]")
				require.Contains(t, plain, "日本語")
				for _, forbidden := range []string{"payload", "secret-profile", "bad-metadata"} {
					require.NotContains(t, plain, forbidden)
				}
				selected, ok := c.Selected()
				require.True(t, ok)
				require.Equal(t, row, selected, "display sanitization modified stored/recall data")
				if mono {
					require.NotContains(t, got, "\x1b")
				}
			}
		})
	}
}

func TestHistoryDisplay_MetadataAndNarrow(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()
	for _, outcome := range []string{"succeeded", "failed", "blocked"} {
		got := formatHistoryRow(query.HistoryRow{Source: "cli", Outcome: outcome, ConnectionID: "profile", SQL: "SELECT 1"}, time.Now())
		require.True(t, strings.HasPrefix(got, "[cli "+outcome+"] effects unverified | profile |"))
		require.Contains(t, truncateToWidth(got, 32), "[cli "+outcome+"]")
	}
	for _, succeeded := range []bool{false, true} {
		got := formatHistoryRow(query.HistoryRow{Succeeded: succeeded, SQL: "SELECT legacy"}, time.Now())
		want := "failed"
		if succeeded {
			want = "succeeded"
		}
		require.Contains(t, got, "[tui "+want+"] - |")
	}
	require.Equal(t, "a b⏎c", sanitizeHistoryText("a\tb\r\nc"))
	require.Equal(t, "…", truncateHistorySQL("long", 1))
}

func TestHistoryDisplay_DuplicateSQL(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()
	const sql = "SELECT 'identical'\n-- original"
	rows := []query.HistoryRow{
		{ID: 2, SQL: sql, Source: "cli", ConnectionID: "reporting", Outcome: "blocked"},
		{ID: 1, SQL: sql, Source: "tui", ConnectionID: "local", Outcome: "succeeded", Succeeded: true},
	}
	c := newTestHistory(rows, &captureDriver{})
	body := formatHistoryBody(c.rows, 0, time.Now())
	require.Len(t, strings.Split(body, "\n"), 2)
	require.Contains(t, body, "[cli blocked] effects unverified | reporting")
	require.Contains(t, body, "[tui succeeded] local")
	for i, expected := range rows {
		c.SetCursor(i)
		selected, ok := c.Selected()
		require.True(t, ok)
		require.Equal(t, expected, selected)
		require.Equal(t, sql, selected.SQL)
	}
}

func TestHistoryDisplay_CLIOutcomeBeforeEffectsWarning(t *testing.T) {
	defer theme.SetMonochromeForTest(true)()
	for _, tc := range []struct {
		name    string
		sql     string
		outcome string
	}{
		{"select_batch", "BEGIN READ ONLY; SELECT 1; ROLLBACK;", "succeeded"},
		{"committed_update", "BEGIN; UPDATE user_profiles SET first_name = 'Sarah' WHERE id = 1; COMMIT;", "succeeded"},
		{"rolled_back_update", "BEGIN; UPDATE user_profiles SET first_name = 'Sarah' WHERE id = 1; ROLLBACK;", "succeeded"},
		{"failed", "SELECT missing_column;", "failed"},
		{"blocked", "DELETE FROM user_profiles;", "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := query.HistoryRow{Source: "cli", Outcome: tc.outcome, ConnectionID: strings.Repeat("日", 24), SQL: tc.sql}
			c := newTestHistory([]query.HistoryRow{row}, &captureDriver{})
			got := formatHistoryBody(c.rows, c.Cursor(), time.Now())
			require.Contains(t, truncateToWidth(got, 32), "[cli "+tc.outcome+"]", "recorded outcome must remain visible in narrow history views")
			require.Contains(t, truncateToWidth(got, 64), "effects unverified", "execution success must not imply verified effects")
			selected, ok := c.Selected()
			require.True(t, ok)
			require.Equal(t, row, selected, "display ordering must not change SQL recall")
		})
	}
}

func TestHistoryDisplay_CLIEffectsUnverified(t *testing.T) {
	for _, mono := range []bool{false, true} {
		t.Run(fmt.Sprintf("monochrome_%t", mono), func(t *testing.T) {
			defer theme.SetMonochromeForTest(mono)()
			for _, outcome := range []string{"succeeded", "failed", "blocked", "unknown", "", strings.Repeat("失", 24)} {
				for _, succeeded := range []bool{false, true} {
					row := query.HistoryRow{Source: "cli", Outcome: outcome, Succeeded: succeeded, ConnectionID: strings.Repeat("日", 24), SQL: "SELECT '日本語'\n-- original"}
					c := newTestHistory([]query.HistoryRow{row}, &captureDriver{})
					got := formatHistoryBody(c.rows, c.Cursor(), time.Now())
					require.Contains(t, got, "effects unverified")
					require.Contains(t, truncateToWidth(sanitizeHistoryText(got), 64), "effects unverified", "warning must precede wide profile metadata")
					require.Contains(t, got, "日本語")
					selected, ok := c.Selected()
					require.True(t, ok)
					require.Equal(t, row, selected, "warning must not change original SQL or recall data")
					row.Source = "tui"
					require.NotContains(t, formatHistoryRow(row, time.Now()), "effects unverified")
				}
			}
			require.Contains(t, formatHistoryRow(query.HistoryRow{Source: "cli"}, time.Now()), "effects unverified")
			require.Equal(t, historyEmptyLine, formatHistoryBody(nil, 0, time.Now()))
		})
	}
}
