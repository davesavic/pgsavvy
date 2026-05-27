package grid

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
	"github.com/davesavic/dbsavvy/pkg/theme/builtin"
)

// resetThemeForTest ensures every cell test starts with the default-dark
// palette so SGR assertions are stable.
func resetThemeForTest(t *testing.T) {
	t.Helper()
	require.NoError(t, theme.Apply(builtin.DefaultDark()))
}

// TestRenderCellPlain_ArrayLiteral asserts a Postgres array column value
// (decoded by pgx into a Go slice) renders as Postgres array syntax
// {a,b,c} rather than Go's "[a b c]" slice formatting. The grid display
// and the edit-seed share this path, so what the user sees is a valid
// array literal they can edit and commit (dbsavvy-26i).
func TestRenderCellPlain_ArrayLiteral(t *testing.T) {
	resetThemeForTest(t)
	col := models.ColumnMeta{Name: "tags", TypeName: "_text"}
	visible := renderCellPlain([]any{"admin", "founder", "editor"}, col)
	require.Equal(t, "{admin,founder,editor}", visible)
}

// TestRenderCell_NullItalic asserts NULL cells emit the italic SGR and
// the literal "NULL".
func TestRenderCell_NullItalic(t *testing.T) {
	resetThemeForTest(t)
	col := models.ColumnMeta{Name: "x", TypeName: "text"}
	visible, decorated := renderCell(nil, col)
	require.Equal(t, "NULL", visible)
	require.Contains(t, decorated, "NULL")
	require.Contains(t, decorated, ansiItalic,
		"NULL cells must include the italic SGR (\\x1b[3m)")
}

// TestRenderCell_NumericStyled asserts an int4 cell carries some SGR
// foreground colour from NumericFg — the decorated output should differ
// from the plain text and include an SGR escape introducer.
func TestRenderCell_NumericStyled(t *testing.T) {
	resetThemeForTest(t)
	col := models.ColumnMeta{Name: "n", TypeName: "int4"}
	visible, decorated := renderCell(42, col)
	require.Equal(t, "42", visible)
	require.NotEqual(t, visible, decorated,
		"numeric cell should be styled, not identical to plain text")
	require.Contains(t, decorated, "\x1b[",
		"numeric cell should include an SGR escape introducer")
}

// TestRenderCell_JSONTruncated feeds a JSON cell of size > MaxCellRenderBytes
// and verifies the visible output ends with the ellipsis and is shorter
// than the original payload.
func TestRenderCell_JSONTruncated(t *testing.T) {
	resetThemeForTest(t)
	// Build a JSON payload bigger than MaxCellRenderBytes.
	big := strings.Repeat("a", MaxCellRenderBytes+200)
	col := models.ColumnMeta{Name: "doc", TypeName: "jsonb"}
	visible := renderCellPlain(big, col)
	require.True(t, strings.HasSuffix(visible, "…"),
		"oversize JSON cell must be truncated with the ellipsis rune")
	require.Less(t, len(visible), len(big),
		"truncated cell must be shorter than the original value")
}

// TestRenderCell_BlobPreview verifies the bytea preview format:
// "\xHEX (NB)" with the literal backslash-x prefix.
func TestRenderCell_BlobPreview(t *testing.T) {
	resetThemeForTest(t)
	col := models.ColumnMeta{Name: "b", TypeName: "bytea"}
	visible := renderCellPlain([]byte{0x48, 0x65, 0x6c, 0x6c, 0x6f}, col)
	require.True(t, strings.HasPrefix(visible, `\x`),
		"bytea preview should start with the literal \\x prefix, got %q", visible)
	require.Contains(t, visible, "(5B)",
		"bytea preview should declare the original byte length")
}

// TestSanitizeCellEscapes asserts the AD-16 stripping contract: CSI/OSC
// escapes and C0 controls (except \t / \n) are removed; plain text and
// tab/newline pass through unchanged. dbsavvy-uv0.7 implements the body
// previously stubbed by dbsavvy-uv0.8.
func TestSanitizeCellEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "plain text", "plain text"},
		{"keeps tab", "a\tb", "a\tb"},
		{"keeps newline", "line1\nline2", "line1\nline2"},
		{"strips CSI red", "with \x1b[31mred\x1b[0m escape", "with red escape"},
		{"strips OSC bell", "before\x1b]0;title\x07after", "beforeafter"},
		{"strips OSC st", "before\x1b]0;title\x1b\\after", "beforeafter"},
		{"strips bare ESC", "a\x1b(Bb", "ab"},
		{"strips bell", "a\x07b", "ab"},
		{"strips CR", "a\rb", "ab"},
		{"strips DEL", "a\x7fb", "ab"},
		{"truncated CSI", "abc\x1b[31m", "abc"},
		{"only escapes", "\x1b[2J\x1b[H", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeCellEscapes(tc.in); got != tc.want {
				t.Errorf("SanitizeCellEscapes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRenderCell_HugeCellTruncates feeds a 20KB text cell and verifies
// truncation kicks in and nothing panics.
func TestRenderCell_HugeCellTruncates(t *testing.T) {
	resetThemeForTest(t)
	col := models.ColumnMeta{Name: "txt", TypeName: "text"}
	huge := strings.Repeat("Q", 20*1024)
	var visible string
	require.NotPanics(t, func() {
		visible = renderCellPlain(huge, col)
	})
	require.Less(t, len(visible), len(huge),
		"20KB cell must be truncated below the input length")
	require.True(t, strings.HasSuffix(visible, "…"),
		"truncated huge cell must end with the ellipsis rune")
}

// TestCapCellBytes_RuneBoundary asserts the byte-cap truncation cuts on
// a rune boundary and never emits invalid UTF-8, even when a multibyte
// rune straddles the MaxCellRenderBytes cap. The previous byte-slice
// implementation could split a 3-byte CJK rune into mojibake.
// dbsavvy-fow.9 U22.
func TestCapCellBytes_RuneBoundary(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		// Each CJK rune is 3 bytes; repeat so total bytes overflow the cap
		// and the cap lands mid-rune.
		{"cjk", strings.Repeat("中", MaxCellRenderBytes)},
		// Emoji are 4 bytes each.
		{"emoji", strings.Repeat("😀", MaxCellRenderBytes)},
		// Combining marks: base 'e' + U+0301 combining acute.
		{"combining", strings.Repeat("é", MaxCellRenderBytes)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := capCellBytes(tc.in)
			require.True(t, utf8.ValidString(got),
				"capped cell must remain valid UTF-8, got %q", got)
			require.LessOrEqual(t, len(got), MaxCellRenderBytes,
				"capped cell must stay within the byte cap")
			require.True(t, strings.HasSuffix(got, "…"),
				"overflowing cell must end with the ellipsis rune")
		})
	}
}

// TestCapCellBytes_UnderCap leaves small cells untouched.
func TestCapCellBytes_UnderCap(t *testing.T) {
	in := "中文测试abc"
	require.Equal(t, in, capCellBytes(in),
		"cells within the cap must be returned unchanged")
}
