package grid

import (
	"strings"
	"testing"

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
