package controllers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// Package-level ActionID aliases. Canonical constants live in
// pkg/gui/commands/actions.go (upstreamed by Z1 Phase A,
// dbsavvy-bwq.23). Aliases retain the controllers.FKReverse* names so
// existing callers (notably this package's tests) keep compiling.
const (
	FKReverseMenu    = commands.FKReverseMenu
	FKReverseNextTab = commands.FKReverseNextTab
	FKReversePrevTab = commands.FKReversePrevTab
	FKReverseSelect  = commands.FKReverseSelect
	FKReverseClose   = commands.FKReverseClose
)

const (
	fkReverseToastTTL = 4 * time.Second
	// fkReverseTabLabelMax bounds the result-tab title rendered after a
	// successful select. Mirrors the cap used by the forward FK helper.
	fkReverseTabLabelMax = 40
)

// fkReverseTree is the narrow focus-stack surface the controller uses to
// dismiss the popup. The orchestrator's *gui.ContextTree satisfies it.
// Mirrors inspectTree in table_inspect_controller.go.
type fkReverseTree interface {
	Pop() error
}

// FKReverseQueryRunner is the narrow runner surface used to dispatch the
// parameterized referencing-table SELECT. *data.QueryRunner satisfies it
// via its RunQuery method. Tests inject a fake.
type FKReverseQueryRunner interface {
	RunQuery(ctx context.Context, q models.Query) (*session.RunHandle, error)
}

// FKReverseTabsManager is the narrow surface used to open the new result
// tab after a tab selection. *ui.ResultTabsHelper satisfies it.
type FKReverseTabsManager interface {
	OpenResultTab(label string, rh *session.RunHandle) error
}

// FKReverseJumpList is the narrow surface used to push a JumpEntry before
// opening the new tab. *ui.ResultJumpList satisfies it.
type FKReverseJumpList interface {
	Push(e ui.JumpEntry)
}

// FKReverseToaster is the narrow toast surface used for failure / queued
// notifications. May be nil — toasts then no-op.
type FKReverseToaster interface {
	Show(message string, ttl time.Duration)
}

// FKReverseOriginTab is the narrow surface the controller queries to
// stamp the JumpEntry pushed before opening the new result tab. The
// active *ui.Tab satisfies it (Slot + ID). May be nil — the JumpEntry is
// then pushed with zero TabSlot / "" TabID, which still records the
// position-by-cursor for Back/Forward.
type FKReverseOriginTab interface {
	Slot() int
	ID() int64
}

// ReverseEntry is one inbound FK in the picker. The caller (Z1 wiring)
// resolves PKValues from the result cursor row and Reltuples from the
// driver (or via session.FKCache once it grows a reltuples accessor).
//
// PKValues bind positionally to FK.RefColumns ($1..$N in the generated
// SELECT). When len(PKValues) != len(FK.RefColumns) the select handler
// surfaces "fk reverse: pk value count mismatch" via the toast and the
// popup stays open.
type ReverseEntry struct {
	// FK describes the referencing constraint. Schema/Table/Columns
	// point at the REFERENCING table; RefSchema/RefTable/RefColumns
	// point at the CURRENT (origin) table.
	FK models.ForeignKey
	// Reltuples is the pg_class.reltuples upper-bound for FK.Table.
	// Positive: ceil(Reltuples) → `~N rows`. Zero: `~0 rows`. Negative
	// (typically -1, no ANALYZE): `~? rows`.
	Reltuples float32
	// PKValues are the origin-table primary-key values bound to the
	// generated SELECT. Aligned with FK.RefColumns positionally.
	PKValues []any
}

// ReversePanel renders the body of one reverse-FK tab. Stateless —
// Body() rebuilds the rendered string on every call so changes to the
// underlying ReverseEntry (e.g. a fresh reltuples fetch) take effect on
// the next render.
type ReversePanel struct {
	entry ReverseEntry
}

// NewReversePanel constructs a panel for a single ReverseEntry.
func NewReversePanel(e ReverseEntry) *ReversePanel { return &ReversePanel{entry: e} }

// Body renders the panel body. Layout:
//
//	<schema>.<table>(<col1>[, col2 ...])
//	<reltuples-line>
//
// reltuples-line:
//
//	reltuples >  0 → "~<ceil(reltuples)> rows"
//	reltuples == 0 → "~0 rows"
//	reltuples <  0 → "~? rows"   (no ANALYZE since reset)
//
// All DB-supplied identifiers go through config.SafeText (AD-17).
func (p *ReversePanel) Body() string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(config.SafeText(p.entry.FK.Schema))
	if p.entry.FK.Schema != "" {
		b.WriteString(".")
	}
	b.WriteString(config.SafeText(p.entry.FK.Table))
	b.WriteString("(")
	for i, c := range p.entry.FK.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(config.SafeText(c))
	}
	b.WriteString(")\n")
	b.WriteString(renderReltuples(p.entry.Reltuples))
	return b.String()
}

// HandleKey is the popup.Panel side of the contract; this panel does
// not handle keys (navigation runs through the controller's bindings).
func (p *ReversePanel) HandleKey(types.Key) bool { return false }

// renderReltuples formats the reltuples float into the user-facing
// "~N rows" / "~0 rows" / "~? rows" string per the amendment in
// dbsavvy-bwq.17.
func renderReltuples(rt float32) string {
	switch {
	case rt < 0:
		return "~? rows"
	case rt == 0:
		return "~0 rows"
	default:
		// math.Ceil on float64 to avoid float32 precision drift on
		// fractional values like 0.7 → 1.
		n := int64(math.Ceil(float64(rt)))
		return fmt.Sprintf("~%d rows", n)
	}
}

// reverseTabTitle returns the tab header title for an inbound FK. We
// pick the first referencing column to keep the title compact; for
// composite FKs the panel body shows the full column list. Example:
// "orders.user_id". When the FK has no Columns (degenerate) we fall
// back to the bare table name.
func reverseTabTitle(fk models.ForeignKey) string {
	if len(fk.Columns) == 0 {
		return config.SafeText(fk.Table)
	}
	return fmt.Sprintf("%s.%s", config.SafeText(fk.Table), config.SafeText(fk.Columns[0]))
}

// FKReversePickerController owns the FK_REVERSE_PICKER popup bindings
// (dbsavvy-bwq.17). State lives on the supplied
// *guicontext.FKReversePickerContext and is mutated through its installed
// *popup.TabbedPopup.
//
//   - <tab> / ]               cycle to next tab
//   - [                       cycle to previous tab
//   - <esc> / q               pop the popup off the focus stack
//   - <cr>                    select active tab → push jump + open result tab
type FKReversePickerController struct {
	baseController

	ctx    *guicontext.FKReversePickerContext
	tree   fkReverseTree
	runner FKReverseQueryRunner
	tabs   FKReverseTabsManager
	jumps  FKReverseJumpList
	toast  FKReverseToaster

	// entries is the slice the active TabbedPopup was built from. Indexed
	// by the popup's Active() to find the entry behind `<CR>`. Reset by
	// every Open call.
	entries []ReverseEntry

	// origin captures the originating tab at Open time so Select can push
	// a faithful JumpEntry. May be nil — JumpEntry then carries zero
	// TabSlot / "" TabID.
	origin    FKReverseOriginTab
	originRow int
	originCol int
}

// FKReversePickerDeps bundles the controller's collaborators. ctx is
// required; tree / runner / tabs / jumps wire the Select handler. toast
// is optional.
type FKReversePickerDeps struct {
	Context *guicontext.FKReversePickerContext
	Tree    fkReverseTree
	Runner  FKReverseQueryRunner
	Tabs    FKReverseTabsManager
	Jumps   FKReverseJumpList
	Toast   FKReverseToaster
}

// NewFKReversePickerController constructs a controller. Every dep is
// nil-safe at construction; handlers nil-check on use so unit tests can
// wire whichever subset they exercise.
func NewFKReversePickerController(c *common.Common, helpers HelperBag, deps FKReversePickerDeps) *FKReversePickerController {
	return &FKReversePickerController{
		baseController: newBase(c, helpers),
		ctx:            deps.Context,
		tree:           deps.Tree,
		runner:         deps.Runner,
		tabs:           deps.Tabs,
		jumps:          deps.Jumps,
		toast:          deps.Toast,
	}
}

// Open installs a fresh TabbedPopup on the context, one tab per
// inbound FK. The originating tab + cursor (row, col) are stamped so a
// later Select pushes a faithful JumpEntry. Returns false (no popup
// installed) when entries is empty — the caller (Z1 binding) then
// surfaces the "no inbound foreign keys" DisabledReason.
func (f *FKReversePickerController) Open(entries []ReverseEntry, origin FKReverseOriginTab, cursorRow, cursorCol int) bool {
	if f.ctx == nil || len(entries) == 0 {
		return false
	}
	tabs := make([]popup.Tab, 0, len(entries))
	for _, e := range entries {
		tabs = append(tabs, popup.Tab{
			Title: reverseTabTitle(e.FK),
			Panel: NewReversePanel(e),
		})
	}
	f.entries = append(f.entries[:0], entries...)
	f.origin = origin
	f.originRow = cursorRow
	f.originCol = cursorCol
	f.ctx.SetState(popup.NewTabbedPopup(tabs))
	return true
}

// Entries returns the live entry slice (read-only). Test accessor.
func (f *FKReversePickerController) Entries() []ReverseEntry {
	out := make([]ReverseEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

// NextTab advances the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (f *FKReversePickerController) NextTab(_ commands.ExecCtx) error {
	if f.ctx == nil {
		return nil
	}
	if s := f.ctx.State(); s != nil {
		s.NextTab()
	}
	return nil
}

// PrevTab rewinds the active tab on the installed TabbedPopup state.
// No-op when the context or state is unwired.
func (f *FKReversePickerController) PrevTab(_ commands.ExecCtx) error {
	if f.ctx == nil {
		return nil
	}
	if s := f.ctx.State(); s != nil {
		s.PrevTab()
	}
	return nil
}

// Close pops the picker off the focus stack. Safe to call when the tree
// is unwired (no-op).
func (f *FKReversePickerController) Close(_ commands.ExecCtx) error {
	if f.tree == nil {
		return nil
	}
	_ = f.tree.Pop()
	return nil
}

// Select builds the parameterized SELECT against the referencing table
// of the active tab, pushes a JumpEntry, opens a new result tab via the
// ResultTabsManager, and pops the picker. Errors at any step are
// surfaced via the toast; the popup stays open on failure so the user
// can pick a different tab.
func (f *FKReversePickerController) Select(_ commands.ExecCtx) error {
	if f.ctx == nil {
		return nil
	}
	state := f.ctx.State()
	if state == nil {
		return nil
	}
	idx := state.Active()
	if idx < 0 || idx >= len(f.entries) {
		f.emitToast("fk reverse: no active tab")
		return nil
	}
	entry := f.entries[idx]

	if len(entry.PKValues) != len(entry.FK.RefColumns) {
		f.emitToast(fmt.Sprintf("fk reverse: pk value count mismatch (have %d, need %d)",
			len(entry.PKValues), len(entry.FK.RefColumns)))
		return nil
	}
	if f.runner == nil || f.tabs == nil || f.jumps == nil {
		f.emitToast("fk reverse: runner / tabs / jumps not wired")
		return nil
	}

	sql := buildFKReverseSQL(entry.FK)
	q := models.Query{SQL: sql, Args: append([]any(nil), entry.PKValues...)}

	// Push the jump entry BEFORE running. If RunQuery / OpenResultTab
	// fail we still want the entry in the list so the user can <C-o>
	// back (the originating cell hasn't moved). Mirrors B5.
	je := ui.JumpEntry{
		Row: f.originRow,
		Col: f.originCol,
		At:  time.Now(),
	}
	if f.origin != nil {
		je.TabSlot = f.origin.Slot()
		je.TabID = fmt.Sprintf("%d", f.origin.ID())
	}
	f.jumps.Push(je)

	rh, err := f.runner.RunQuery(context.Background(), q)
	if err != nil {
		f.emitToast(fmt.Sprintf("fk reverse: run: %v", err))
		return nil
	}

	label := buildFKReverseLabel(entry)
	if openErr := f.tabs.OpenResultTab(label, rh); openErr != nil {
		if errors.Is(openErr, ui.ErrTabCapReached) {
			f.emitToast("result tabs at cap; unpin a tab to free a slot")
		} else {
			f.emitToast(fmt.Sprintf("fk reverse: open result tab: %v", openErr))
		}
		return nil
	}

	// Dismiss popup on success. Tree may be nil in unit tests; the no-op
	// keeps assertions simple.
	if f.tree != nil {
		_ = f.tree.Pop()
	}
	return nil
}

// buildFKReverseSQL composes the parameterized SELECT against the
// REFERENCING table. fk.Schema/Table/Columns are the referencing side;
// fk.RefColumns supplies the count of $-positions to bind. All
// identifiers go through pg.QuoteIdent / pg.QuoteQualified per ADR-21.
func buildFKReverseSQL(fk models.ForeignKey) string {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(pg.QuoteQualified(fk.Schema, fk.Table))
	b.WriteString(" WHERE ")
	for i, col := range fk.Columns {
		if i > 0 {
			b.WriteString(" AND ")
		}
		b.WriteString(pg.QuoteIdent(col))
		fmt.Fprintf(&b, "=$%d", i+1)
	}
	return b.String()
}

// buildFKReverseLabel produces the result-tab title rendered after a
// successful Select. Layout: `← <referencing_table>(<pk_col>=<pk_val> …)`.
// Truncated at fkReverseTabLabelMax with an ellipsis suffix.
func buildFKReverseLabel(e ReverseEntry) string {
	var inner string
	if len(e.FK.RefColumns) == 1 && len(e.PKValues) == 1 {
		inner = fmt.Sprintf("%v", e.PKValues[0])
	} else {
		parts := make([]string, 0, len(e.FK.RefColumns))
		for i, c := range e.FK.RefColumns {
			if i >= len(e.PKValues) {
				break
			}
			parts = append(parts, fmt.Sprintf("%s=%v", c, e.PKValues[i]))
		}
		inner = strings.Join(parts, ", ")
	}
	full := fmt.Sprintf("← %s(%s)", e.FK.Table, inner)
	return truncateReverseLabel(full, fkReverseTabLabelMax)
}

// truncateReverseLabel byte-clips s to max bytes with an ellipsis suffix
// when truncation occurs. The label is purely cosmetic.
func truncateReverseLabel(s string, max int) string {
	if max <= 0 {
		return s
	}
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	if max <= len(ellipsis) {
		return s[:max]
	}
	return s[:max-len(ellipsis)] + ellipsis
}

// GetKeybindings returns the FK_REVERSE_PICKER-scope bindings. Mirrors
// TableInspectController's set: <tab>/] NextTab, [ PrevTab, <esc>/q
// Close, <cr> Select. Z1 owns the central scope wiring; the constant
// FK_REVERSE_PICKER_KEY is sourced from the picker context.
func (f *FKReversePickerController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	scope := guicontext.FKReversePickerContextKey
	return []*types.ChordBinding{
		{
			Sequence: []types.ChordKey{{Special: types.KeyTab}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReverseNextTab,
		},
		{
			Sequence: []types.ChordKey{{Code: ']'}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReverseNextTab,
		},
		{
			Sequence: []types.ChordKey{{Code: '['}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReversePrevTab,
		},
		{
			Sequence: []types.ChordKey{{Special: types.KeyEnter}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReverseSelect,
		},
		{
			Sequence: []types.ChordKey{{Special: types.KeyEsc}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReverseClose,
		},
		{
			Sequence: []types.ChordKey{{Code: 'q'}},
			Mode:     types.ModeNormal,
			Scope:    scope,
			ActionID: FKReverseClose,
		},
	}
}

// RegisterActions registers the next/prev/close/select handlers under
// the local action IDs. Z1 will re-route these to the canonical
// commands.* constants when it promotes the IDs centrally.
func (f *FKReversePickerController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{ID: FKReverseNextTab, Description: "FK reverse picker next tab", Handler: f.NextTab})
	_ = reg.Register(&commands.Command{ID: FKReversePrevTab, Description: "FK reverse picker prev tab", Handler: f.PrevTab})
	_ = reg.Register(&commands.Command{ID: FKReverseSelect, Description: "FK reverse picker select", Handler: f.Select})
	_ = reg.Register(&commands.Command{ID: FKReverseClose, Description: "FK reverse picker close", Handler: f.Close})
}

// AttachToContext registers GetKeybindings on the FK_REVERSE_PICKER
// context. Mirrors TableInspectController.AttachToContext.
func (f *FKReversePickerController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(f.GetKeybindings)
}

func (f *FKReversePickerController) emitToast(msg string) {
	if f.toast == nil {
		return
	}
	f.toast.Show(msg, fkReverseToastTTL)
}
