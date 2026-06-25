package grid

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestFormatViewerBody_NullReturnsSentinel(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "text"}
	body := FormatViewerBodyPlain(nil, col, false)
	require.Equal(t, ViewerCellNULL, body)
}

func TestFormatViewerBody_EmptyStringReturnsSentinel(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "text"}
	body := FormatViewerBodyPlain("", col, false)
	require.Equal(t, ViewerCellEmpty, body)
}

func TestFormatViewerBody_ScalarReturnsFormatted(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "int4"}
	body := FormatViewerBodyPlain(int64(42), col, false)
	require.Equal(t, "42", body)
}

func TestFormatViewerBody_TimeHandles(t *testing.T) {
	col := models.ColumnMeta{Name: "ts", TypeName: "timestamptz"}
	tm := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	body := FormatViewerBodyPlain(tm, col, false)
	require.Contains(t, body, "2025-01-15")
}

func TestFormatViewerBody_JSONPrettyPrints(t *testing.T) {
	col := models.ColumnMeta{Name: "data", TypeName: "jsonb"}
	v := map[string]any{"plan": "pro", "active": true}
	body := FormatViewerBodyPlain(v, col, true)
	require.Contains(t, body, `"plan": "pro"`)
	require.Contains(t, body, `"active": true`)
	require.Contains(t, body, "\n")
	require.Contains(t, body, "    ")
}

func TestFormatViewerBody_JSONPassthroughPrettyPrints(t *testing.T) {
	col := models.ColumnMeta{Name: "data", TypeName: "jsonb"}
	body := FormatViewerBodyPlain(`{"a":1}`, col, true)
	require.Contains(t, body, `"a": 1`)
}

func TestFormatViewerBody_JSONParseFailed(t *testing.T) {
	col := models.ColumnMeta{Name: "data", TypeName: "jsonb"}
	body := FormatViewerBodyPlain("not json at all", col, true)
	require.Contains(t, body, "not json at all")
	require.Contains(t, body, "(parse failed)")
}

func TestFormatViewerBody_ArrayNewlineJoined(t *testing.T) {
	col := models.ColumnMeta{Name: "tags", TypeName: "_text"}
	body := FormatViewerBodyPlain([]any{"admin", "founder", "editor"}, col, false)
	require.Equal(t, "admin\nfounder\neditor", body)
}

func TestFormatViewerBody_EmptyArrayReturnsSentinel(t *testing.T) {
	col := models.ColumnMeta{Name: "tags", TypeName: "_text"}
	body := FormatViewerBodyPlain([]any{}, col, false)
	require.Equal(t, ViewerCellEmpty, body)
}

func TestFormatViewerBody_ByteaUTF8Decodes(t *testing.T) {
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	body := FormatViewerBodyPlain([]byte("hello world"), col, false)
	require.Equal(t, "hello world", body)
}

func TestFormatViewerBody_ByteaNonUTF8HexDumps(t *testing.T) {
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	raw := []byte{0x00, 0xFF, 0x48, 0x65, 0x6c, 0x6c, 0x6f}
	body := FormatViewerBodyPlain(raw, col, false)
	require.Contains(t, body, "00")
	require.Contains(t, body, "ff")
}

func TestFormatViewerBody_ByteaEmptyReturnsSentinel(t *testing.T) {
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	body := FormatViewerBodyPlain([]byte{}, col, false)
	require.Equal(t, ViewerCellEmpty, body)
}

func TestFormatViewerBody_OneMBGateJSON(t *testing.T) {
	col := models.ColumnMeta{Name: "data", TypeName: "jsonb"}
	big := strings.Repeat("x", maxViewerCellBytes+100)
	body := FormatViewerBodyPlain(big, col, false)
	require.Contains(t, body, "cell too large")
	require.Contains(t, body, "... cell too large")
	require.Less(t, len(body), len(big))
}

func TestFormatViewerBody_OneMBGateArray(t *testing.T) {
	col := models.ColumnMeta{Name: "tags", TypeName: "_text"}
	elem := strings.Repeat("a", maxViewerCellBytes+10)
	body := FormatViewerBodyPlain([]any{elem}, col, false)
	require.Contains(t, body, "cell too large")
}

func TestFormatViewerBody_OneMBGateBytea(t *testing.T) {
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	big := make([]byte, maxViewerCellBytes+100)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	body := FormatViewerBodyPlain(big, col, false)
	require.Contains(t, body, "cell too large")
}

func TestFormatViewerBody_OneMBGateByteaNonUTF8(t *testing.T) {
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	big := make([]byte, maxViewerCellBytes+100)
	for i := range big {
		big[i] = byte(i % 256)
	}
	body := FormatViewerBodyPlain(big, col, false)
	require.True(t, strings.Contains(body, "cell too large") || len(body) < len(hex.Dump(big)))
}

func TestFormatViewerBody_NonByteArrayIsTreatedAsScalarByDefault(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "int4"}
	body := FormatViewerBodyPlain([]int32{1, 2, 3}, col, false)
	require.Equal(t, "1\n2\n3", body)
}

func TestFormatViewerBody_StyledHasANSI(t *testing.T) {
	col := models.ColumnMeta{Name: "n", TypeName: "int4"}
	body, parseFailed := FormatViewerBody(int64(42), col, false)
	require.False(t, parseFailed)
	require.Contains(t, body, "\x1b[")
	require.Contains(t, body, "42")
}

func TestFormatViewerBody_StyledNullNoStyle(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "text"}
	body, _ := FormatViewerBody(nil, col, false)
	require.Equal(t, ViewerCellNULL, body)
	require.NotContains(t, body, "\x1b[")
}

func TestFormatViewerBody_StyledEmptyNoStyle(t *testing.T) {
	col := models.ColumnMeta{Name: "x", TypeName: "text"}
	body, _ := FormatViewerBody("", col, false)
	require.Equal(t, ViewerCellEmpty, body)
}

func TestTruncatedPreview_UnderLimit(t *testing.T) {
	s := "short"
	got := truncatedPreview(s)
	require.Equal(t, s, got)
}

func TestTruncatedPreview_OverLimit(t *testing.T) {
	s := strings.Repeat("a", 2000)
	got := truncatedPreview(s)
	require.Contains(t, got, "... cell too large")
	require.Contains(t, got, "2000 bytes")
	require.LessOrEqual(t, len(got), maxTruncatedPreview+len("... cell too large (2000 bytes)"))
}

func TestWrapWindow_SingleLineFits(t *testing.T) {
	w := WrapWindow("hello", 80, 0, 10)
	require.Equal(t, 1, w.Lines())
	require.Equal(t, len("hello"), w.Bytes())
	require.Equal(t, []string{"hello"}, w.Slice())
}

func TestWrapWindow_SingleLineWraps(t *testing.T) {
	w := WrapWindow("abcdefghij", 3, 0, 10)
	require.Equal(t, 4, w.Lines())
	require.Equal(t, []string{"abc", "def", "ghi", "j"}, w.Slice())
}

func TestWrapWindow_Paging(t *testing.T) {
	parts := make([]string, 20)
	for i := range parts {
		parts[i] = "abcdef"
	}
	body := strings.Join(parts, "\n")
	w := WrapWindow(body, 6, 3, 5)
	require.Equal(t, 20, w.Lines())
	require.Equal(t, 5, len(w.Slice()))
	require.Equal(t, "abcdef", w.Slice()[0])
}

func TestWrapWindow_OffsetPastEnd(t *testing.T) {
	w := WrapWindow("hello", 80, 10, 5)
	require.Equal(t, 0, len(w.Slice()))
	require.Equal(t, 1, w.Lines())
}

func TestWrapWindow_ZeroViewHeight(t *testing.T) {
	w := WrapWindow("hello", 80, 0, 0)
	require.Equal(t, 0, len(w.Slice()))
	require.Equal(t, len("hello"), w.Bytes())
}

func TestWrapWindow_NewlinePreserved(t *testing.T) {
	w := WrapWindow("line1\n\nline3", 80, 0, 10)
	require.Equal(t, 3, w.Lines())
	require.Equal(t, "line1", w.Slice()[0])
	require.Equal(t, "", w.Slice()[1])
	require.Equal(t, "line3", w.Slice()[2])
}

func TestWrapWindow_WideRunes(t *testing.T) {
	w := WrapWindow("中文测试", 3, 0, 10)
	for _, line := range w.Slice() {
		require.True(t, utf8.ValidString(line))
	}
}

func TestWrapWindow_WidthZeroDefaultsToOne(t *testing.T) {
	w := WrapWindow("a", 0, 0, 10)
	require.Equal(t, []string{"a"}, w.Slice())
}

func TestWrapWindow_MidWindowSlice(t *testing.T) {
	body := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	w := WrapWindow(body, 80, 4, 3)
	require.Equal(t, []string{"line5", "line6", "line7"}, w.Slice())
	require.Equal(t, 10, w.Lines())
	require.Equal(t, len(body), w.Bytes())
}
