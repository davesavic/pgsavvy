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

// TableInspectController owns the TABLE_INSPECT popup bindings
// (dbsavvy-3vf). All state lives on the supplied *context.TableInspectContext
// and is mutated through its installed *popup.TabbedPopup.
//
//   - <tab> / ]               cycle to next tab
//   - [                       cycle to previous tab
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

// GetKeybindings returns the TABLE_INSPECT-scope bindings. Five entries:
// <tab>+] for NextTab, [ for PrevTab, <esc>+q for Close. All ModeNormal.
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
}

// AttachToContext registers GetKeybindings on the TABLE_INSPECT context.
func (t *TableInspectController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(t.GetKeybindings)
}
