package controllers

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
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

// Body returns the formatted column list, or the empty-state placeholder.
func (p *ColumnsPanel) Body() string {
	if p == nil || p.ctx == nil {
		return "(no columns)"
	}
	items := p.ctx.Items()
	if len(items) == 0 {
		return "(no columns)"
	}
	var b strings.Builder
	first := true
	for _, it := range items {
		c := asColumn(it)
		if c == nil {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(formatColumn(c))
	}
	if first {
		return "(no columns)"
	}
	return b.String()
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

// formatColumn renders a single column row.
// Layout: "<name>  <type>[ NOT NULL][  default=<x>]".
// Every DB-supplied string passes through config.SafeText (AMD-5c).
func formatColumn(c *models.Column) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(config.SafeText(c.Name))
	if c.DataType != "" {
		b.WriteString("  ")
		b.WriteString(config.SafeText(c.DataType))
	}
	if !c.Nullable {
		b.WriteString("  NOT NULL")
	}
	if c.Default != "" {
		b.WriteString("  default=")
		b.WriteString(config.SafeText(c.Default))
	}
	return b.String()
}

// IndexesPanel renders the indexes tab of the TABLE_INSPECT popup.
type IndexesPanel struct {
	ctx *context.IndexesContext
}

// NewIndexesPanel returns an IndexesPanel bound to ctx. ctx may be nil.
func NewIndexesPanel(ctx *context.IndexesContext) *IndexesPanel {
	return &IndexesPanel{ctx: ctx}
}

// Body renders the index list, or the empty-state placeholder.
func (p *IndexesPanel) Body() string {
	if p == nil || p.ctx == nil {
		return "(no indexes)"
	}
	items := p.ctx.Items()
	if len(items) == 0 {
		return "(no indexes)"
	}
	var b strings.Builder
	first := true
	for _, it := range items {
		idx := asIndex(it)
		if idx == nil {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		first = false
		b.WriteString(formatIndex(idx))
	}
	if first {
		return "(no indexes)"
	}
	return b.String()
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

// formatIndex renders a single index row.
// Layout: "<name>[ UNIQUE][ PK]  (<cols>)[  using <method>]".
func formatIndex(idx *models.Index) string {
	if idx == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(config.SafeText(idx.Name))
	if idx.IsPrimary {
		b.WriteString(" PK")
	}
	if idx.IsUnique {
		b.WriteString(" UNIQUE")
	}
	if len(idx.Columns) > 0 {
		safeCols := make([]string, 0, len(idx.Columns))
		for _, col := range idx.Columns {
			safeCols = append(safeCols, config.SafeText(col))
		}
		fmt.Fprintf(&b, "  (%s)", strings.Join(safeCols, ", "))
	}
	if idx.Method != "" {
		b.WriteString("  using ")
		b.WriteString(config.SafeText(idx.Method))
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
	helpers HelperBag,
	inspectCtx *context.TableInspectContext,
	tree inspectTree,
) *TableInspectController {
	return &TableInspectController{
		baseController: newBase(c, helpers),
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
