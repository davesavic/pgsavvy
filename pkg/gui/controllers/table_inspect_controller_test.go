package controllers_test

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/popup"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// newInspectContext builds a TABLE_INSPECT context wired with a
// TabbedPopup containing `n` stub tabs so NextTab / PrevTab can advance.
func newInspectContext(t *testing.T, n int) *context.TableInspectContext {
	t.Helper()
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.TABLE_INSPECT,
		ViewName: string(types.TABLE_INSPECT),
		Kind:     types.TEMPORARY_POPUP,
		Title:    "Table inspect",
	})
	ctx := context.NewTableInspectContext(base, types.ContextTreeDeps{})
	tabs := make([]popup.Tab, 0, n)
	for range n {
		tabs = append(tabs, popup.Tab{Title: "tab"})
	}
	ctx.SetState(popup.NewTabbedPopup(tabs))
	return ctx
}

// fakeTree records Pop() invocations for the Close action test.
type fakeTree struct{ pops atomic.Int32 }

func (f *fakeTree) Pop() error {
	f.pops.Add(1)
	return nil
}

func TestTableInspectController_GetKeybindings_ExactlyFive(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	kbs := ctrl.GetKeybindings(types.KeybindingsOpts{})
	if got, want := len(kbs), 5; got != want {
		t.Fatalf("len(GetKeybindings()) = %d, want %d", got, want)
	}
	for i, kb := range kbs {
		if kb.Scope != types.TABLE_INSPECT {
			t.Errorf("kbs[%d].Scope = %q, want %q", i, kb.Scope, types.TABLE_INSPECT)
		}
		if kb.Mode != types.ModeNormal {
			t.Errorf("kbs[%d].Mode = %v, want ModeNormal", i, kb.Mode)
		}
	}
}

// TestTableInspectController_GetKeybindings_NoShiftTabBinding asserts no
// binding maps a Shift+Tab chord. gocui has no Backtab; AMD-5b explicitly
// dropped the <S-tab> binding for this scope.
func TestTableInspectController_GetKeybindings_NoShiftTabBinding(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if len(kb.Sequence) != 1 {
			continue
		}
		k := kb.Sequence[0]
		if k.Special == types.KeyTab && k.Mod&types.ChordModShift != 0 {
			t.Fatalf("found <S-tab> binding (ActionID=%q); AMD-5b forbids it", kb.ActionID)
		}
	}
}

func TestTableInspectController_GetKeybindings_ActionIDs(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	counts := map[string]int{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		counts[kb.ActionID]++
	}
	if counts[commands.TableInspectNextTab] != 2 {
		t.Errorf("TableInspectNextTab bindings = %d, want 2 (Tab + ])", counts[commands.TableInspectNextTab])
	}
	if counts[commands.TableInspectPrevTab] != 1 {
		t.Errorf("TableInspectPrevTab bindings = %d, want 1 ([)", counts[commands.TableInspectPrevTab])
	}
	if counts[commands.TableInspectClose] != 2 {
		t.Errorf("TableInspectClose bindings = %d, want 2 (Esc + q)", counts[commands.TableInspectClose])
	}
}

func TestTableInspectController_NextTabAction_AdvancesState(t *testing.T) {
	ic := newInspectContext(t, 2)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)
	if got := ic.State().Active(); got != 0 {
		t.Fatalf("pre-NextTab Active() = %d, want 0", got)
	}
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("NextTab: %v", err)
	}
	if got := ic.State().Active(); got != 1 {
		t.Errorf("post-NextTab Active() = %d, want 1", got)
	}
}

func TestTableInspectController_PrevTabAction_RewindsState(t *testing.T) {
	ic := newInspectContext(t, 2)
	ic.State().SetActive(1)
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, ic, nil)
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Fatalf("PrevTab: %v", err)
	}
	if got := ic.State().Active(); got != 0 {
		t.Errorf("post-PrevTab Active() = %d, want 0", got)
	}
}

func TestTableInspectController_CloseAction_PopsContext(t *testing.T) {
	tree := &fakeTree{}
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, tree)
	if err := ctrl.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := tree.pops.Load(); got != 1 {
		t.Errorf("tree.Pop calls = %d, want 1", got)
	}
}

func TestTableInspectController_NextPrevAction_NoStateNoPanic(t *testing.T) {
	ctrl := controllers.NewTableInspectController(nil, controllers.CoreDeps{}, nil, nil)
	if err := ctrl.NextTab(commands.ExecCtx{}); err != nil {
		t.Errorf("NextTab nil ctx: %v", err)
	}
	if err := ctrl.PrevTab(commands.ExecCtx{}); err != nil {
		t.Errorf("PrevTab nil ctx: %v", err)
	}
	if err := ctrl.Close(commands.ExecCtx{}); err != nil {
		t.Errorf("Close nil tree: %v", err)
	}
}

func TestColumnsPanel_EmptyState(t *testing.T) {
	p := controllers.NewColumnsPanel(nil)
	if got := p.Body(); got != "(no columns)" {
		t.Errorf("nil ctx Body() = %q, want %q", got, "(no columns)")
	}
}

func TestIndexesPanel_EmptyState(t *testing.T) {
	p := controllers.NewIndexesPanel(nil)
	if got := p.Body(); got != "(no indexes)" {
		t.Errorf("nil ctx Body() = %q, want %q", got, "(no indexes)")
	}
}

func TestColumnsPanel_SafeText_StripsEscapes(t *testing.T) {
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.COLUMNS,
		ViewName: string(types.COLUMNS),
		Kind:     types.SIDE_CONTEXT,
	})
	cc := context.NewColumnsContext(base, types.ContextTreeDeps{})
	cc.SetItems([]any{&models.Column{
		Name:     "id\x1b[2J",
		DataType: "int\x1b[31m",
		Default:  "0\x1b[0m",
		Nullable: false,
	}})
	p := controllers.NewColumnsPanel(cc)
	body := p.Body()
	if strings.ContainsRune(body, '\x1b') {
		t.Errorf("Body() contains ESC byte: %q", body)
	}
	if !strings.Contains(body, "id") || !strings.Contains(body, "int") {
		t.Errorf("Body() missing expected sanitized content: %q", body)
	}
}

func TestIndexesPanel_SafeText_StripsEscapes(t *testing.T) {
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.INDEXES,
		ViewName: string(types.INDEXES),
		Kind:     types.SIDE_CONTEXT,
	})
	ic := context.NewIndexesContext(base, types.ContextTreeDeps{})
	ic.SetItems([]any{&models.Index{
		Name:    "idx_pk\x1b[2J",
		Columns: []string{"a\x1b[31m"},
		Method:  "btree\x1b[0m",
	}})
	p := controllers.NewIndexesPanel(ic)
	body := p.Body()
	if strings.ContainsRune(body, '\x1b') {
		t.Errorf("Body() contains ESC byte: %q", body)
	}
	if !strings.Contains(body, "idx_pk") {
		t.Errorf("Body() missing expected sanitized name: %q", body)
	}
}

func TestPanels_HandleKey_AlwaysFalse(t *testing.T) {
	cp := controllers.NewColumnsPanel(nil)
	ip := controllers.NewIndexesPanel(nil)
	// Sample of bare-rune and special chord keys; HandleKey must reject all.
	var zeroKey types.Key
	if cp.HandleKey(zeroKey) {
		t.Errorf("ColumnsPanel.HandleKey returned true")
	}
	if ip.HandleKey(zeroKey) {
		t.Errorf("IndexesPanel.HandleKey returned true")
	}
}

func TestColumnsPanel_FormatsNonNullAndDefault(t *testing.T) {
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.COLUMNS,
		ViewName: string(types.COLUMNS),
		Kind:     types.SIDE_CONTEXT,
	})
	cc := context.NewColumnsContext(base, types.ContextTreeDeps{})
	cc.SetItems([]any{
		&models.Column{Name: "id", DataType: "int", Nullable: false, Default: "nextval()"},
		&models.Column{Name: "note", DataType: "text", Nullable: true},
	})
	p := controllers.NewColumnsPanel(cc)
	body := p.Body()
	if !strings.Contains(body, "NOT NULL") {
		t.Errorf("Body() should contain NOT NULL marker for non-nullable column: %q", body)
	}
	if !strings.Contains(body, "default=nextval()") {
		t.Errorf("Body() should contain default=nextval(): %q", body)
	}
	if strings.Count(body, "\n") != 1 {
		t.Errorf("Body() expected one newline between rows: %q", body)
	}
}

func TestIndexesPanel_FormatsUniqueAndColumns(t *testing.T) {
	base := context.NewBaseContext(context.BaseContextOpts{
		Key:      types.INDEXES,
		ViewName: string(types.INDEXES),
		Kind:     types.SIDE_CONTEXT,
	})
	ic := context.NewIndexesContext(base, types.ContextTreeDeps{})
	ic.SetItems([]any{&models.Index{
		Name:     "u_email",
		IsUnique: true,
		Columns:  []string{"email"},
		Method:   "btree",
	}})
	p := controllers.NewIndexesPanel(ic)
	body := p.Body()
	if !strings.Contains(body, "UNIQUE") {
		t.Errorf("Body() should contain UNIQUE marker: %q", body)
	}
	if !strings.Contains(body, "(email)") {
		t.Errorf("Body() should contain (email): %q", body)
	}
	if !strings.Contains(body, "using btree") {
		t.Errorf("Body() should contain `using btree`: %q", body)
	}
}
