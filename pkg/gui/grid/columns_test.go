package grid

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestAutoSize_LocksAfterSampleCount feeds AutoSizeSampleRowCount rows of
// modest width, then appends one row with a far longer cell. The frozen
// width should reflect the sampled rows — not the late, overlong row —
// so the late-row Yank still reads the full value (TSV yank uses
// renderCellPlain which only enforces MaxCellRenderBytes), but the
// Render output truncates the late row with an ellipsis.
func TestAutoSize_LocksAfterSampleCount(t *testing.T) {
	v := NewView()
	v.SetColumns(makeSingleCol("c1", "text"))

	// Feed exactly AutoSizeSampleRowCount rows, all narrow. Widest cell
	// is 5 runes ("xxxxx"). After this batch, widths are locked at
	// max(MinColumnWidth, 5) == MinColumnWidth (6).
	sample := make([]models.Row, AutoSizeSampleRowCount)
	for i := range sample {
		sample[i] = models.Row{Values: []any{"xxxxx"}}
	}
	v.AppendRows(sample)

	// Now append a row whose value is far wider than the locked column.
	longValue := strings.Repeat("Z", 200)
	v.AppendRows([]models.Row{{Values: []any{longValue}}})

	// Move cursor to the late row, render, and check the body line for
	// truncation. We don't depend on a gocui.View here — invoke the
	// internal renderer directly to assert the padded width.
	v.JumpLast()
	snap := v.snapshot()
	// effectiveWidth should still be MinColumnWidth since 5-rune cells
	// were the widest in the sample.
	effective := effectiveWidth(snap.widths, 0)
	require.LessOrEqual(t, effective, MaxColumnWidth,
		"locked width must respect MaxColumnWidth")
	require.GreaterOrEqual(t, effective, MinColumnWidth,
		"locked width must respect MinColumnWidth")
	// The late row's rendered data-line should show a truncation
	// ellipsis because the actual content is much wider than the width.
	line := renderDataLine(snap, AutoSizeSampleRowCount, effective+10)
	require.Contains(t, line, "…",
		"overlong cell rendered against locked width must include ellipsis")

	// And the line's visible width (after stripping ANSI) should be
	// bounded by the locked column width plus a separator — definitely
	// not 200 runes.
	stripped := stripANSI(line)
	require.Less(t, utf8.RuneCountInString(stripped), len(longValue),
		"locked-width render should not expand to full value length")
}

// TestPadRight_TruncatesWithEllipsis verifies the truncation contract:
// inputs wider than w get cut to w-1 runes plus the ellipsis rune.
func TestPadRight_TruncatesWithEllipsis(t *testing.T) {
	out := padRight("abcdefghij", 5)
	require.True(t, strings.HasSuffix(out, "…"),
		"truncated padRight output must end with …, got %q", out)
	require.Equal(t, 5, utf8.RuneCountInString(out),
		"truncated padRight output must be exactly w runes wide")
}

// TestPadRight_UnicodeWidthBoundary feeds a multibyte string in and
// asserts that truncation respects rune boundaries (no mid-rune slice
// or replacement character leak).
func TestPadRight_UnicodeWidthBoundary(t *testing.T) {
	// 6 runes, each 3 bytes in UTF-8.
	input := "中文测试字符" // 6 CJK runes
	require.Equal(t, 6, utf8.RuneCountInString(input))

	out := padRight(input, 4)
	require.Equal(t, 4, runewidth.StringWidth(out),
		"unicode truncation must yield exactly w terminal cells")
	require.True(t, utf8.ValidString(out),
		"truncated unicode output must be valid UTF-8 (no mid-rune slice)")
	require.True(t, strings.HasSuffix(strings.TrimRight(out, " "), "…"),
		"unicode truncation must end with the ellipsis rune")
}

func TestReview_width_measure_and_pad(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		width, cells      int
	}{
		{"ASCII", "abc", "abc  ", 5, 3},
		{"ASCII overflow", "abcdef", "abc…", 4, 6},
		{"ASCII width one", "abc", "a", 1, 3},
		{"CJK", "中文", "中文  ", 6, 4},
		{"CJK overflow", "中文测试", "中… ", 4, 8},
		{"CJK width one", "中", "…", 1, 2},
		{"combining", "e\u0301e\u0301", "e\u0301e\u0301  ", 4, 2},
		{"combining overflow", "e\u0301xyz", "e\u0301…", 2, 4},
		{"emoji", "🚀", "🚀 ", 3, 2},
		{"emoji width one", "🚀", "…", 1, 2},
		{"joined emoji", "👩‍💻abc", "👩‍💻…", 3, 5},
		{"modified emoji", "👍🏽abc", "👍🏽…", 3, 5},
		{"zero budget", "中", "", 0, 2},
		{"negative budget", "中", "", -1, 2},
		{"empty", "", "   ", 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.cells, displayWidth(tc.input))
			got := padRight(tc.input, tc.width)
			require.Equal(t, tc.want, got)
			require.True(t, utf8.ValidString(got))
			require.Equal(t, max(tc.width, 0), runewidth.StringWidth(got))
		})
	}
}

func TestReview_width_autosize(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "中文测试标题列", TypeName: "text"},
		{Name: "combining", TypeName: "text"},
		{Name: "emoji", TypeName: "text"},
	})
	require.Equal(t, 14, v.snapshot().widths[0], "empty results size headers in cells")
	v.AppendRows([]models.Row{{Values: []any{
		"中文测试标题列值", strings.Repeat("e\u0301", 12), strings.Repeat("🚀", 7),
	}}})
	require.Equal(t, []int{16, 12, 14}, v.snapshot().widths)
}

func TestReview_width_truncate_clusters(t *testing.T) {
	for _, input := range []string{"中文测试", "e\u0301xyz", "👩‍💻abc", "👍🏽abc"} {
		for w := 0; w <= 8; w++ {
			got := truncateToWidth(input, w)
			require.True(t, utf8.ValidString(got))
			require.LessOrEqual(t, runewidth.StringWidth(got), w)
		}
	}
	require.Equal(t, "👩‍💻…", truncateToWidth("👩‍💻abc", 3))
	require.Equal(t, "e\u0301…", truncateToWidth("e\u0301xyz", 2))
}

func TestReview_width_eastasian_markers(t *testing.T) {
	// tcell fixes its locale policy at startup; mutating go-runewidth alone
	// no longer exercises the actual native width branch.
	if os.Getenv("PGSAVVY_NATIVE_WIDTH_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestReview_width_eastasian_markers$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "RUNEWIDTH_EASTASIAN=1", "PGSAVVY_NATIVE_WIDTH_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return
	}
	require.Equal(t, 2, displayWidth("…"), "exercise the wide-marker branch")
	for _, input := range []string{"abcdefgh", "中文测试", "e\u0301xyz", "👩‍💻abc", "👍🏽abc"} {
		for w := 0; w <= 8; w++ {
			var padded, truncated string
			require.NotPanics(t, func() {
				padded = padRight(input, w)
				truncated = truncateToWidth(input, w)
			})
			require.True(t, utf8.ValidString(padded))
			require.True(t, utf8.ValidString(truncated))
			require.Equal(t, w, displayWidth(padded))
			require.LessOrEqual(t, displayWidth(truncated), w)
		}
	}
	require.Equal(t, ".", padRight("中", 1))
	require.Equal(t, "a", padRight("abc", 1))
	require.Equal(t, "ab…", padRight("abcdefgh", 4))
	leftArrow, rightArrow := "◄", "►"
	if displayWidth(leftArrow) != 1 {
		leftArrow = "<"
	}
	if displayWidth(rightArrow) != 1 {
		rightArrow = ">"
	}
	for _, line := range []string{"abcdef", "中文测试", "👩‍💻abc"} {
		out := overlayColumnArrows(line, true, true)
		require.Equal(t, displayWidth(line), displayWidth(out))
		require.True(t, strings.HasPrefix(out, " "+leftArrow))
		require.True(t, strings.HasSuffix(out, rightArrow+" "))
	}
	value := "中文界"
	padded := padRight(value, 4)
	span := []highlightSpan{{byteStart: 3, byteEnd: 6}}
	require.Equal(t, wholeCellFallback(padded, span), applyMatchHighlights(value, padded, 4, span))
}

// TestSetColumns_SeedsWidthsFromHeaders verifies that SetColumns sizes
// column widths from header labels so an empty-table result (no AppendRows)
// renders full-width headings instead of truncating to MinColumnWidth.
func TestSetColumns_SeedsWidthsFromHeaders(t *testing.T) {
	v := NewView()
	v.SetColumns([]models.ColumnMeta{
		{Name: "id", TypeName: "int4"},
		{Name: "published_at", TypeName: "timestamptz"},
	})
	// No AppendRows — simulates an empty table.
	snap := v.snapshot()
	w0 := effectiveWidth(snap.widths, 0)
	w1 := effectiveWidth(snap.widths, 1)
	require.GreaterOrEqual(t, w0, MinColumnWidth,
		"short header should still meet MinColumnWidth")
	require.Greater(t, w1, MinColumnWidth,
		"'published_at' (12 runes) should be wider than MinColumnWidth (6)")
	require.GreaterOrEqual(t, w1, len("published_at"),
		"width must accommodate the full header label")
}

// stripANSI removes ANSI SGR escape sequences from s for length-based
// assertions. Matches \x1b[...m sequences.
func stripANSI(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}
