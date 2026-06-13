package grid

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHighlightRuneSpans_WrapsSpanWithSearchSGR proves a [start,end) rune span
// is wrapped with the (non-current) Search SGR and the rest is left untouched.
func TestHighlightRuneSpans_WrapsSpanWithSearchSGR(t *testing.T) {
	got := HighlightRuneSpans("order_email", [][2]int{{0, 3}})
	require.Equal(t, searchSGR+"ord"+ansiReset+"er_email", got)
	require.NotContains(t, got, curSearchSGR, "must use SearchHighlight, not CurSearch")
}

// TestHighlightRuneSpans_EmptySpansUnchanged proves nil/empty spans return the
// input verbatim.
func TestHighlightRuneSpans_EmptySpansUnchanged(t *testing.T) {
	require.Equal(t, "order_email", HighlightRuneSpans("order_email", nil))
	require.Equal(t, "order_email", HighlightRuneSpans("order_email", [][2]int{}))
}

// TestHighlightRuneSpans_Multibyte proves spans wrap on rune boundaries, never
// mid-byte, for non-ASCII input.
func TestHighlightRuneSpans_Multibyte(t *testing.T) {
	// "café" has 4 runes (c, a, f, é); é is 2 bytes. Highlight runes [2,4) = "fé".
	got := HighlightRuneSpans("café", [][2]int{{2, 4}})
	require.Equal(t, "ca"+searchSGR+"fé"+ansiReset, got)
}

// TestHighlightRuneSpans_MultibyteFirstRune highlights only the leading
// multibyte rune.
func TestHighlightRuneSpans_MultibyteFirstRune(t *testing.T) {
	// "éclair": é(0) c(1) l(2) ... highlight rune [0,1) = "é".
	got := HighlightRuneSpans("éclair", [][2]int{{0, 1}})
	require.Equal(t, searchSGR+"é"+ansiReset+"clair", got)
}

// TestHighlightRuneSpans_OutOfRangeClamped proves an end past the string is
// clamped to the rune length without panic.
func TestHighlightRuneSpans_OutOfRangeClamped(t *testing.T) {
	// "café" has 4 runes; end=99 clamps to 4.
	got := HighlightRuneSpans("café", [][2]int{{2, 99}})
	require.Equal(t, "ca"+searchSGR+"fé"+ansiReset, got)
}

// TestHighlightRuneSpans_NegativeStartClamped proves a negative start clamps to
// zero.
func TestHighlightRuneSpans_NegativeStartClamped(t *testing.T) {
	got := HighlightRuneSpans("café", [][2]int{{-5, 2}})
	require.Equal(t, searchSGR+"ca"+ansiReset+"fé", got)
}

// TestHighlightRuneSpans_InvertedDropped proves an inverted span (start>=end)
// is dropped, leaving the string unchanged.
func TestHighlightRuneSpans_InvertedDropped(t *testing.T) {
	require.Equal(t, "café", HighlightRuneSpans("café", [][2]int{{3, 1}}))
	require.Equal(t, "café", HighlightRuneSpans("café", [][2]int{{2, 2}}))
}

// TestHighlightRuneSpans_OutOfRangeAfterClampInverted proves a span that
// becomes empty after clamping (e.g. start beyond length) is dropped safely.
func TestHighlightRuneSpans_OutOfRangeAfterClampInverted(t *testing.T) {
	// start=10 clamps to 4 (len), end=99 clamps to 4 -> start>=end -> dropped.
	require.Equal(t, "café", HighlightRuneSpans("café", [][2]int{{10, 99}}))
}

// TestHighlightRuneSpans_MultipleSpans proves several valid spans each get
// wrapped.
func TestHighlightRuneSpans_MultipleSpans(t *testing.T) {
	got := HighlightRuneSpans("order_email", [][2]int{{0, 3}, {6, 11}})
	require.Equal(t, searchSGR+"ord"+ansiReset+"er_"+searchSGR+"email"+ansiReset, got)
}

// TestHighlightRuneSpans_MixedValidAndInvalid proves invalid spans are dropped
// while valid ones survive in the same call.
func TestHighlightRuneSpans_MixedValidAndInvalid(t *testing.T) {
	got := HighlightRuneSpans("order_email", [][2]int{{2, 1}, {0, 3}})
	require.Equal(t, searchSGR+"ord"+ansiReset+"er_email", got)
}
