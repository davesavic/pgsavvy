package controllers

import (
	"strings"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// columnHeader / indexHeader are the fixed header rows prepended to the
// aligned column/index tables.
var (
	columnHeader = []string{"NAME", "TYPE", "NULL", "DEFAULT"}
	indexHeader  = []string{"NAME", "FLAGS", "COLUMNS", "METHOD"}
)

// inspectTree is the narrow focus-stack surface TableInspectController
// uses to dismiss the popup. The orchestrator's *gui.ContextTree
// satisfies it. Kept as an interface so the controller stays free of
// the pkg/gui import (controllers must not depend on the orchestrator).
type inspectTree interface {
	Pop() error
}

// ColumnsPanel renders the columns tab of the TABLE_INSPECT popup. It
// is stateless: Body() reads ColumnsContext.Items() on every call so it
// always reflects the most recent refresh.
type ColumnsPanel struct {
	ctx *context.ColumnsContext
}

// NewColumnsPanel returns a ColumnsPanel bound to ctx. ctx may be nil;
// Body() then returns the empty-state placeholder.
func NewColumnsPanel(ctx *context.ColumnsContext) *ColumnsPanel {
	return &ColumnsPanel{ctx: ctx}
}

// Body returns the aligned column table (header + one row per column),
// or the empty-state placeholder.
func (p *ColumnsPanel) Body() string {
	if p == nil || p.ctx == nil {
		return "(no columns)"
	}
	rows := make([][]string, 0, len(p.ctx.Items())+1)
	for _, it := range p.ctx.Items() {
		if c := asColumn(it); c != nil {
			rows = append(rows, columnCells(c))
		}
	}
	if len(rows) == 0 {
		return "(no columns)"
	}
	return alignRows(append([][]string{columnHeader}, rows...))
}

// HandleKey is the popup.Panel side of the contract; this panel does
// not handle keys (navigation runs through the controller's bindings).
func (p *ColumnsPanel) HandleKey(types.Key) bool { return false }

func asColumn(it any) *models.Column {
	switch v := it.(type) {
	case *models.Column:
		return v
	case models.Column:
		return &v
	}
	return nil
}

// columnCells renders a single column as the cells of an aligned row:
// {name, type, null-marker, default}. Every DB-supplied string passes
// through config.SafeText (AMD-5c).
func columnCells(c *models.Column) []string {
	null := ""
	if !c.Nullable {
		null = "NOT NULL"
	}
	def := ""
	if c.Default != "" {
		def = "default=" + config.SafeText(c.Default)
	}
	return []string{
		config.SafeText(c.Name),
		config.SafeText(c.DataType),
		null,
		def,
	}
}

// IndexesPanel renders the indexes tab of the TABLE_INSPECT popup.
type IndexesPanel struct {
	ctx *context.IndexesContext
}

// NewIndexesPanel returns an IndexesPanel bound to ctx. ctx may be nil.
func NewIndexesPanel(ctx *context.IndexesContext) *IndexesPanel {
	return &IndexesPanel{ctx: ctx}
}

// Body renders the aligned index table (header + one row per index), or
// the empty-state placeholder.
func (p *IndexesPanel) Body() string {
	if p == nil || p.ctx == nil {
		return "(no indexes)"
	}
	rows := make([][]string, 0, len(p.ctx.Items())+1)
	for _, it := range p.ctx.Items() {
		if idx := asIndex(it); idx != nil {
			rows = append(rows, indexCells(idx))
		}
	}
	if len(rows) == 0 {
		return "(no indexes)"
	}
	return alignRows(append([][]string{indexHeader}, rows...))
}

// HandleKey is the popup.Panel side of the contract; this panel does
// not handle keys.
func (p *IndexesPanel) HandleKey(types.Key) bool { return false }

func asIndex(it any) *models.Index {
	switch v := it.(type) {
	case *models.Index:
		return v
	case models.Index:
		return &v
	}
	return nil
}

// indexCells renders a single index as the cells of an aligned row:
// {name, flags, columns, method}. Flags are "PK"/"UNIQUE" (space-joined);
// columns are wrapped in parentheses. Every DB-supplied string passes
// through config.SafeText.
func indexCells(idx *models.Index) []string {
	flags := make([]string, 0, 2)
	if idx.IsPrimary {
		flags = append(flags, "PK")
	}
	if idx.IsUnique {
		flags = append(flags, "UNIQUE")
	}
	cols := ""
	if len(idx.Columns) > 0 {
		safeCols := make([]string, 0, len(idx.Columns))
		for _, col := range idx.Columns {
			safeCols = append(safeCols, config.SafeText(col))
		}
		cols = "(" + strings.Join(safeCols, ", ") + ")"
	}
	return []string{
		config.SafeText(idx.Name),
		strings.Join(flags, " "),
		cols,
		config.SafeText(idx.Method),
	}
}

// alignRows renders rows as a fixed-width table: each column is padded to
// the widest cell in that column so values line up vertically. Cells are
// separated by two spaces; trailing empty cells produce no padding, so
// lines carry no trailing whitespace. Rows are joined by '\n'.
func alignRows(rows [][]string) string {
	widths := columnWidths(rows)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, padRow(row, widths))
	}
	return strings.Join(lines, "\n")
}

// columnWidths returns the max rune width of each column across all rows.
func columnWidths(rows [][]string) []int {
	n := 0
	for _, row := range rows {
		if len(row) > n {
			n = len(row)
		}
	}
	widths := make([]int, n)
	for _, row := range rows {
		for i, cell := range row {
			if w := utf8.RuneCountInString(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}
	return widths
}

// padRow joins a row's cells with a two-space gap, padding each cell to
// its column width. The last non-empty cell (and any empty cells after
// it) are not padded, so no trailing whitespace is emitted.
func padRow(row []string, widths []int) string {
	last := -1
	for i, cell := range row {
		if cell != "" {
			last = i
		}
	}
	var b strings.Builder
	for i := 0; i <= last; i++ {
		if i > 0 {
			b.WriteString("  ")
		}
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		b.WriteString(cell)
		if i < last {
			if pad := widths[i] - utf8.RuneCountInString(cell); pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
	}
	return b.String()
}

// Table-inspect scroll action IDs. Local to the
// controller — like the cheatsheet scroll IDs they only ever fire under
// the TABLE_INSPECT scope and carry no user-facing config knob (the
// bindings are shipped defaults).
const (
	tableInspectScrollDownID  = "table_inspect.scroll_down"
	tableInspectScrollUpID    = "table_inspect.scroll_up"
	tableInspectScrollLeftID  = "table_inspect.scroll_left"
	tableInspectScrollRightID = "table_inspect.scroll_right"
	tableInspectPageDownID    = "table_inspect.page_down"
	tableInspectPageUpID      = "table_inspect.page_up"
	tableInspectTopID         = "table_inspect.scroll_top"
	tableInspectBottomID      = "table_inspect.scroll_bottom"
)

// tableInspectHalfPage is the fixed line delta for <c-d>/<c-u>; the
// viewport height is not known to the controller. tableInspectHStep is
// the per-press horizontal delta for h/l — wide column descriptions read
// faster a few columns at a time than one. tableInspectScrollBottom is
// the `G` sentinel; the layout pass clamps it to the content's last page.
const (
	tableInspectHalfPage     = 10
	tableInspectHStep        = 8
	tableInspectScrollBottom = 1 << 20
)

// TableInspectController owns the TABLE_INSPECT popup bindings.
// All state lives on the supplied *context.TableInspectContext
// and is mutated through its installed *popup.TabbedPopup.
//
//   - <tab> / ]               cycle to next tab
//   - [                       cycle to previous tab
//   - j/k, <c-d>/<c-u>, gg/G  scroll the body vertically
//   - h/l                     scroll the body horizontally
//   - <esc> / q               pop the popup off the focus stack
type TableInspectController struct {
	baseController
	inspectCtx *context.TableInspectContext
	tree       inspectTree
}

// NewTableInspectController constructs a TableInspectController. Either
// dependency may be nil during unit tests; handlers nil-check on use.
func NewTableInspectController(
	c *common.Common,
	core CoreDeps,
	inspectCtx *context.TableInspectContext,
	tree inspectTree,
) *TableInspectController {
	return &TableInspectController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		inspectCtx:     inspectCtx,
		tree:           tree,
	}
}

// NextTab advances the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (t *TableInspectController) NextTab(_ commands.ExecCtx) error {
	if t.inspectCtx == nil {
		return nil
	}
	if s := t.inspectCtx.State(); s != nil {
		s.NextTab()
		t.inspectCtx.SetScrollX(0)
		t.inspectCtx.SetScrollY(0)
	}
	return nil
}

// PrevTab rewinds the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (t *TableInspectController) PrevTab(_ commands.ExecCtx) error {
	if t.inspectCtx == nil {
		return nil
	}
	if s := t.inspectCtx.State(); s != nil {
		s.PrevTab()
		t.inspectCtx.SetScrollX(0)
		t.inspectCtx.SetScrollY(0)
	}
	return nil
}

// Close pops the TABLE_INSPECT context off the focus stack. No-op when
// the tree is unwired.
func (t *TableInspectController) Close(_ commands.ExecCtx) error {
	if t.tree == nil {
		return nil
	}
	_ = t.tree.Pop()
	return nil
}

// scroll moves the inspect view origin by (dx, dy). The context clamps
// the top-left edge; the layout pass clamps the bottom-right against the
// rendered content extent.
func (t *TableInspectController) scroll(dx, dy int) error {
	if t.inspectCtx != nil {
		t.inspectCtx.Scroll(dx, dy)
	}
	return nil
}

// ScrollDown / ScrollUp move one line; ScrollLeft / ScrollRight move one
// horizontal step; PageDown / PageUp move a half page.
func (t *TableInspectController) ScrollDown(commands.ExecCtx) error { return t.scroll(0, 1) }
func (t *TableInspectController) ScrollUp(commands.ExecCtx) error   { return t.scroll(0, -1) }
func (t *TableInspectController) ScrollLeft(commands.ExecCtx) error {
	return t.scroll(-tableInspectHStep, 0)
}

func (t *TableInspectController) ScrollRight(commands.ExecCtx) error {
	return t.scroll(tableInspectHStep, 0)
}

func (t *TableInspectController) PageDown(commands.ExecCtx) error {
	return t.scroll(0, tableInspectHalfPage)
}

func (t *TableInspectController) PageUp(commands.ExecCtx) error {
	return t.scroll(0, -tableInspectHalfPage)
}

// ScrollTop / ScrollBottom jump to the first / last page (vertical),
// preserving the horizontal origin.
func (t *TableInspectController) ScrollTop(commands.ExecCtx) error {
	if t.inspectCtx != nil {
		t.inspectCtx.SetScrollY(0)
	}
	return nil
}

func (t *TableInspectController) ScrollBottom(commands.ExecCtx) error {
	if t.inspectCtx != nil {
		t.inspectCtx.SetScrollY(tableInspectScrollBottom)
	}
	return nil
}

// GetKeybindings returns the TABLE_INSPECT-scope bindings: <tab>+] for
// NextTab, [ for PrevTab, <esc>+q for Close, plus j/k+arrows+<c-d>/<c-u>+
// gg/G for vertical scroll and h/l+arrows for horizontal scroll. All
// ModeNormal.
func (t *TableInspectController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := t.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyTab}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    commands.TableInspectNextTab,
			Description: tr.Actions.TableInspectNextTab,
		},
		{
			Sequence:    []types.ChordKey{{Code: ']'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    commands.TableInspectNextTab,
			Description: tr.Actions.TableInspectNextTab,
		},
		{
			Sequence:    []types.ChordKey{{Code: '['}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    commands.TableInspectPrevTab,
			Description: tr.Actions.TableInspectPrevTab,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    commands.TableInspectClose,
			Description: tr.Actions.TableInspectClose,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    commands.TableInspectClose,
			Description: tr.Actions.TableInspectClose,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollDownID,
			Description: "Table inspect scroll down",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyDown}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollDownID,
			Description: "Table inspect scroll down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollUpID,
			Description: "Table inspect scroll up",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyUp}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollUpID,
			Description: "Table inspect scroll up",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'h'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollLeftID,
			Description: "Table inspect scroll left",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyLeft}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollLeftID,
			Description: "Table inspect scroll left",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'l'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollRightID,
			Description: "Table inspect scroll right",
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyRight}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectScrollRightID,
			Description: "Table inspect scroll right",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'd', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectPageDownID,
			Description: "Table inspect half-page down",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'u', Mod: types.ChordModCtrl}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectPageUpID,
			Description: "Table inspect half-page up",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'g'}, {Code: 'g'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectTopID,
			Description: "Table inspect scroll to top",
		},
		{
			Sequence:    []types.ChordKey{{Code: 'G'}},
			Mode:        types.ModeNormal,
			Scope:       types.TABLE_INSPECT,
			ActionID:    tableInspectBottomID,
			Description: "Table inspect scroll to bottom",
		},
	}
}

// RegisterActions registers the next-tab / prev-tab / close handlers.
func (t *TableInspectController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.TableInspectNextTab,
		Description: "Table inspect next tab",
		Handler:     t.NextTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TableInspectPrevTab,
		Description: "Table inspect prev tab",
		Handler:     t.PrevTab,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.TableInspectClose,
		Description: "Table inspect close",
		Handler:     t.Close,
	})
	for _, b := range []struct {
		id      string
		desc    string
		handler func(commands.ExecCtx) error
	}{
		{tableInspectScrollDownID, "Table inspect scroll down", t.ScrollDown},
		{tableInspectScrollUpID, "Table inspect scroll up", t.ScrollUp},
		{tableInspectScrollLeftID, "Table inspect scroll left", t.ScrollLeft},
		{tableInspectScrollRightID, "Table inspect scroll right", t.ScrollRight},
		{tableInspectPageDownID, "Table inspect half-page down", t.PageDown},
		{tableInspectPageUpID, "Table inspect half-page up", t.PageUp},
		{tableInspectTopID, "Table inspect scroll to top", t.ScrollTop},
		{tableInspectBottomID, "Table inspect scroll to bottom", t.ScrollBottom},
	} {
		_ = reg.Register(&commands.Command{
			ID:          b.id,
			Description: b.desc,
			Handler:     b.handler,
		})
	}
}

// AttachToContext registers GetKeybindings on the TABLE_INSPECT context.
func (t *TableInspectController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(t.GetKeybindings)
}
