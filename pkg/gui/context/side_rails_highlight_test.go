package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// Local SGR consts under the implicit default-dark theme (SearchHighlight =
// "yellow", CurSearch = "black on yellow"), mirroring how
// grid/search_highlight_test.go relies on the default theme.
const (
	searchSGR    = "\x1b[33m"         // yellow fg (non-current match)
	curSearchSGR = "\x1b[30m\x1b[43m" // black fg + yellow bg (current match)
)

func newTablesCtx(drv *captureDriver, deps types.ContextTreeDeps) *TablesContext {
	deps.GuiDriver = drv
	base := NewBaseContext(BaseContextOpts{Key: types.TABLES, ViewName: string(types.TABLES), Kind: types.SIDE_CONTEXT})
	return NewTablesContext(base, deps)
}

func newSchemasCtx(drv *captureDriver, deps types.ContextTreeDeps) *SchemasContext {
	deps.GuiDriver = drv
	base := NewBaseContext(BaseContextOpts{Key: types.SCHEMAS, ViewName: string(types.SCHEMAS), Kind: types.SIDE_CONTEXT})
	return NewSchemasContext(base, deps)
}

func tableItems(names ...string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = &models.Table{Name: n}
	}
	return out
}

func schemaItems(names ...string) []any {
	out := make([]any, len(names))
	for i, n := range names {
		out[i] = models.Schema{Name: n}
	}
	return out
}

// stripSGR removes all CSI sequences so a row can be matched by its plain
// name even when the name is split by highlight escapes.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++ // consume final byte
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func rowFor(body, name string) string {
	for line := range strings.SplitSeq(strings.TrimRight(body, "\n"), "\n") {
		if strings.Contains(stripSGR(line), name) {
			return line
		}
	}
	return ""
}

// Tables: current match carries curSearchSGR, another match carries searchSGR.
func TestRailHighlight_TablesCurrentVsOther(t *testing.T) {
	drv := &captureDriver{}
	c := newTablesCtx(drv, types.ContextTreeDeps{})
	c.SetItems(tableItems("users", "user_roles", "orders"))
	c.SetCursor(0)
	c.SetSearch("user")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent

	usersRow := rowFor(body, "users")
	if !strings.Contains(usersRow, curSearchSGR+"user"+railAnsiReset) {
		t.Errorf("current-match row = %q, want current span %q", usersRow, curSearchSGR+"user"+railAnsiReset)
	}
	rolesRow := rowFor(body, "user_roles")
	if !strings.Contains(rolesRow, searchSGR+"user"+railAnsiReset) {
		t.Errorf("other-match row = %q, want non-current span %q", rolesRow, searchSGR+"user"+railAnsiReset)
	}
}

// Marker outside span, match at FIRST and LAST rune, no off-by-one.
func TestRailHighlight_MarkerOutsideSpanAndEdges(t *testing.T) {
	drv := &captureDriver{}
	c := newTablesCtx(drv, types.ContextTreeDeps{})
	// "ab" — search "a" hits first rune; search via two passes is awkward,
	// so use names where the same query hits start and end on distinct rows.
	c.SetItems(tableItems("xy_users", "users_xy"))
	c.SetCursor(0)
	c.SetSearch("users")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent

	// Row where "users" is at the LAST runes.
	lastRow := rowFor(body, "xy_users")
	if !strings.Contains(lastRow, searchSGR+"users"+railAnsiReset) &&
		!strings.Contains(lastRow, curSearchSGR+"users"+railAnsiReset) {
		t.Errorf("last-rune row = %q, want highlighted 'users' span", lastRow)
	}
	// Row where "users" is at the FIRST runes.
	firstRow := rowFor(body, "users_xy")
	if !strings.Contains(firstRow, searchSGR+"users"+railAnsiReset) &&
		!strings.Contains(firstRow, curSearchSGR+"users"+railAnsiReset) {
		t.Errorf("first-rune row = %q, want highlighted 'users' span", firstRow)
	}
	// Marker stays outside the SGR: the line begins with a plain "> "/"  "
	// marker, not an escape.
	for _, row := range []string{firstRow, lastRow} {
		if !strings.HasPrefix(row, "> ") && !strings.HasPrefix(row, "  ") {
			t.Errorf("row %q does not start with a plain marker", row)
		}
		// The first SGR introducer must appear AFTER the 2-byte marker.
		if idx := strings.Index(row, "\x1b["); idx >= 0 && idx < 2 {
			t.Errorf("SGR leaked into marker for row %q", row)
		}
	}
}

// Sanitize: embedded escape neutralized; span at correct offset of sanitized name.
func TestRailHighlight_SanitizesAndHighlightsCorrectOffset(t *testing.T) {
	drv := &captureDriver{}
	c := newTablesCtx(drv, types.ContextTreeDeps{})
	c.SetItems(tableItems("ev\x1b[31mil_users"))
	c.SetCursor(0)
	c.SetSearch("users")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent

	if strings.Contains(body, "\x1b[31m") {
		t.Errorf("embedded CSI \\x1b[31m leaked into rendered body %q", body)
	}
	if !strings.Contains(body, searchSGR+"users"+railAnsiReset) &&
		!strings.Contains(body, curSearchSGR+"users"+railAnsiReset) {
		t.Errorf("body %q missing highlighted 'users' span at sanitized offset", body)
	}
}

// Disconnected dim composition — the key test.
func TestRailHighlight_DisconnectedDimComposition(t *testing.T) {
	drv := &captureDriver{}
	c := newSchemasCtx(drv, types.ContextTreeDeps{
		IsDisconnected: func() bool { return true },
	})
	c.SetItems(schemaItems("public"))
	c.SetCursor(0)
	c.SetSearch("pub")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	row := rowFor(drv.lastContent, "public")
	if row == "" {
		t.Fatalf("no public row in %q", drv.lastContent)
	}

	// Row starts dimmed (after the plain marker).
	if !strings.HasPrefix(row, "  "+railAnsiDim) && !strings.HasPrefix(row, "> "+railAnsiDim) {
		t.Errorf("row %q does not start with dim baseline after marker", row)
	}
	// The matched span carries the highlight style.
	if !strings.Contains(row, curSearchSGR+"pub"+railAnsiReset) &&
		!strings.Contains(row, searchSGR+"pub"+railAnsiReset) {
		t.Errorf("row %q missing highlighted 'pub' span", row)
	}

	// Find the span's reset, then assert trailing bytes are re-dimmed and
	// carry no background SGR.
	_, after0, ok := strings.Cut(row, "pub"+railAnsiReset)
	if !ok {
		t.Fatalf("no 'pub'+reset in row %q", row)
	}
	after := after0
	if !strings.Contains(after, railAnsiDim) {
		t.Errorf("trailing bytes %q not re-dimmed", after)
	}
	if strings.Contains(after, "\x1b[43m") || strings.Contains(after, "48;2;") {
		t.Errorf("background SGR leaked past span in trailing %q", after)
	}
}

// Hidden-set single source: hidden row skipped, visible match after it
// highlighted on the correct line, offsets intact; hidden name never in
// Matches() and never rendered.
func TestRailHighlight_HiddenSetSingleSource(t *testing.T) {
	drv := &captureDriver{}
	c := newSchemasCtx(drv, types.ContextTreeDeps{
		HiddenSchemasForActiveConn: func() []string { return []string{"pg_catalog"} },
	})
	c.SetItems(schemaItems("public", "pg_catalog", "pg_temp"))
	c.SetCursor(0)
	c.SetSearch("pg")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent

	if strings.Contains(body, "pg_catalog") {
		t.Errorf("hidden schema pg_catalog rendered in %q", body)
	}
	// Visible match after the hidden row is highlighted.
	tempRow := rowFor(body, "pg_temp")
	if !strings.Contains(tempRow, searchSGR+"pg"+railAnsiReset) &&
		!strings.Contains(tempRow, curSearchSGR+"pg"+railAnsiReset) {
		t.Errorf("pg_temp row = %q, want highlighted 'pg' span", tempRow)
	}
	// The hidden name is never in Matches() (same set drives both).
	for _, m := range c.Matches() {
		if m.RowIndex == 1 { // pg_catalog raw index
			t.Errorf("Matches() includes hidden row index 1: %+v", m)
		}
	}
	// isRowVisible agrees with render.
	if c.isRowVisible(1) {
		t.Errorf("isRowVisible(1) = true for hidden pg_catalog")
	}
	if !c.isRowVisible(2) {
		t.Errorf("isRowVisible(2) = false for visible pg_temp")
	}
}

// Inactive is inert: connected and disconnected outputs carry no highlight SGR.
func TestRailHighlight_InactiveInert(t *testing.T) {
	for _, dim := range []bool{false, true} {
		drv := &captureDriver{}
		deps := types.ContextTreeDeps{}
		if dim {
			deps.IsDisconnected = func() bool { return true }
		}
		c := newTablesCtx(drv, deps)
		c.SetItems(tableItems("users", "orders"))
		c.SetCursor(0)
		if err := c.HandleRender(); err != nil {
			t.Fatalf("HandleRender: %v", err)
		}
		body := drv.lastContent
		if strings.Contains(body, searchSGR) || strings.Contains(body, "\x1b[30m") {
			t.Errorf("dim=%v inactive body %q carries highlight SGR", dim, body)
		}
		if dim {
			if !strings.Contains(body, railAnsiDim+"users"+railAnsiReset) {
				t.Errorf("dim inactive body %q lost the plain dim render", body)
			}
		} else {
			if strings.Contains(body, "\x1b[") {
				t.Errorf("connected inactive body %q carries any SGR", body)
			}
		}
	}
}

// Zero matches => no highlight SGR; duplicate substring => both spans wrapped.
func TestRailHighlight_ZeroAndDuplicateMatches(t *testing.T) {
	// Zero matches: query present, no row contains it.
	drv := &captureDriver{}
	c := newTablesCtx(drv, types.ContextTreeDeps{})
	c.SetItems(tableItems("users", "orders"))
	c.SetSearch("zzz")
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if strings.Contains(drv.lastContent, searchSGR) || strings.Contains(drv.lastContent, "\x1b[30m") {
		t.Errorf("zero-match body %q carries highlight SGR", drv.lastContent)
	}

	// Duplicate substring in one name: "aa_aa" search "aa" => two spans.
	drv2 := &captureDriver{}
	c2 := newTablesCtx(drv2, types.ContextTreeDeps{})
	c2.SetItems(tableItems("aa_aa"))
	c2.SetCursor(0)
	c2.SetSearch("aa")
	if err := c2.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	row := rowFor(drv2.lastContent, "aa_aa")
	// Two highlighted "aa" spans (one current at the landed match, one not).
	total := strings.Count(row, "aa"+railAnsiReset)
	if total != 2 {
		t.Errorf("row %q has %d highlighted 'aa' spans, want 2", row, total)
	}
}
