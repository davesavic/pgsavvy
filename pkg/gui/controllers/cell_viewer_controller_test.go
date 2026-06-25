package controllers_test

import (
	"errors"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func newCellViewerTestCtx() *guicontext.CellViewerContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.CellViewerKey(),
		ViewName: string(guicontext.CellViewerKey()),
		Kind:     types.PERSISTENT_POPUP,
	})
	return guicontext.NewCellViewerContext(base, guicontext.Deps{})
}

func newCellViewerWithGate(picker *fakeGridPicker) (
	*controllers.CellViewerController,
	*guicontext.CellViewerContext,
	*fakeFocusTree,
) {
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, ctx, tree, picker)
	return ctrl, ctx, tree
}

func TestCellViewer_OpenFromGrid(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "hello world",
		column: models.ColumnMeta{Name: "greeting", TypeName: "text"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, ctx, tree := newCellViewerWithGate(picker)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.ResultViewCellOpen)
	if !ok {
		t.Fatal("ResultViewCellOpen not registered")
	}

	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if tree.pushes != 1 {
		t.Errorf("tree.pushes = %d, want 1", tree.pushes)
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after Open; want true")
	}
	if got := ctx.OriginalValue(); got != "hello world" {
		t.Errorf("OriginalValue = %v, want %q", got, "hello world")
	}
	if ctx.Colname() != "greeting" {
		t.Errorf("Colname() = %q, want %q", ctx.Colname(), "greeting")
	}
}

func TestCellViewer_OpenNilPickerNoOps(t *testing.T) {
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, ctx, tree, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open nil picker: %v", err)
	}
}

func TestCellViewer_OpenAlreadyActiveNoOps(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, tree := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.ResultViewCellOpen)

	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	pushesBefore := tree.pushes

	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if tree.pushes != pushesBefore {
		t.Errorf("pushes = %d after duplicate Open, want %d", tree.pushes, pushesBefore)
	}
}

func TestCellViewer_OpenCellNotOkNoOps(t *testing.T) {
	picker := &fakeGridPicker{
		cellOK: false,
	}
	ctrl, ctx, tree := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(commands.ResultViewCellOpen)

	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open cellOK=false: %v", err)
	}
	if tree.pushes != 0 {
		t.Errorf("tree.pushes = %d, want 0 (cellOK=false)", tree.pushes)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after cellOK=false Open; want false")
	}
}

func TestCellViewer_Dismiss(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "data",
		column: models.ColumnMeta{Name: "col"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, ctx, tree := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	dismissCmd, ok := reg.Get("cell_viewer.dismiss")
	if !ok {
		t.Fatal("cell_viewer.dismiss not registered")
	}

	if err := dismissCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Dismiss; want false")
	}
}

func TestCellViewer_DismissInactiveNoOps(t *testing.T) {
	ctrl, ctx, tree := newCellViewerWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	dismissCmd, _ := reg.Get("cell_viewer.dismiss")

	if err := dismissCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Dismiss inactive: %v", err)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after no-op Dismiss; want false")
	}
	if tree.pops != 0 {
		t.Errorf("tree.pops = %d, want 0 (context was never active)", tree.pops)
	}
}

func TestCellViewer_WrapToggle(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, ctx, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if !ctx.Wrap() {
		t.Error("Wrap() = false after Open; want true")
	}

	wrapCmd, ok := reg.Get("cell_viewer.toggle_wrap")
	if !ok {
		t.Fatal("cell_viewer.toggle_wrap not registered")
	}
	if err := wrapCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ToggleWrap: %v", err)
	}
	if ctx.Wrap() {
		t.Error("Wrap() = true after toggle; want false")
	}

	if err := wrapCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ToggleWrap second: %v", err)
	}
	if !ctx.Wrap() {
		t.Error("Wrap() = false after second toggle; want true")
	}
}

func TestCellViewer_WrapToggleInactiveNoOps(t *testing.T) {
	ctrl, ctx, _ := newCellViewerWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	wrapCmd, _ := reg.Get("cell_viewer.toggle_wrap")
	if err := wrapCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ToggleWrap inactive: %v", err)
	}
	if !ctx.Wrap() {
		t.Error("Wrap() changed on inactive context")
	}
}

func TestCellViewer_FormatToggleJSON(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   `{"a":1}`,
		column: models.ColumnMeta{Name: "meta", TypeName: "jsonb"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, ctx, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	if !ctx.Pretty() {
		t.Error("Pretty() = false after Open; want true")
	}

	prettyCmd, ok := reg.Get("cell_viewer.toggle_pretty")
	if !ok {
		t.Fatal("cell_viewer.toggle_pretty not registered")
	}
	if err := prettyCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TogglePretty: %v", err)
	}
	if ctx.Pretty() {
		t.Error("Pretty() = true after toggle; want false")
	}

	if err := prettyCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TogglePretty second: %v", err)
	}
	if !ctx.Pretty() {
		t.Error("Pretty() = false after second toggle; want true")
	}
}

func TestCellViewer_FormatToggleNonJSONShowsToast(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "plain text",
		column: models.ColumnMeta{Name: "c", TypeName: "text"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	toast := &fakeToast{}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: toast}, ctx, tree, picker)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	prettyCmd, _ := reg.Get("cell_viewer.toggle_pretty")
	if err := prettyCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TogglePretty: %v", err)
	}

	if len(toast.msgs) != 1 {
		t.Fatalf("toast count = %d, want 1", len(toast.msgs))
	}
	if toast.msgs[0].TTL <= 0 || toast.msgs[0].TTL > 5*time.Second {
		t.Errorf("toast TTL = %v, want >0 and <=5s", toast.msgs[0].TTL)
	}
	if !ctx.Pretty() {
		t.Error("Pretty() = false on non-JSON column; should stay true")
	}
}

func TestCellViewer_FormatToggleInactiveNoOps(t *testing.T) {
	ctrl, ctx, _ := newCellViewerWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	prettyCmd, _ := reg.Get("cell_viewer.toggle_pretty")
	if err := prettyCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("TogglePretty inactive: %v", err)
	}
	if !ctx.Pretty() {
		t.Error("Pretty() changed on inactive context")
	}
}

func TestCellViewer_Yank(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "copied text",
		column: models.ColumnMeta{Name: "content", TypeName: "text"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	toast := &fakeToast{}
	cb := &fakeClipboard{}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: toast}, ctx, tree, picker)
	ctrl.SetClipboard(cb)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	yankCmd, ok := reg.Get("cell_viewer.yank")
	if !ok {
		t.Fatal("cell_viewer.yank not registered")
	}
	if err := yankCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Yank: %v", err)
	}

	if len(cb.writes) != 1 {
		t.Fatalf("clipboard writes = %d, want 1", len(cb.writes))
	}
	if cb.writes[0] != "copied text" {
		t.Errorf("clipboard write = %q, want %q", cb.writes[0], "copied text")
	}
	if len(toast.msgs) != 1 {
		t.Fatalf("toast count = %d, want 1 yank confirmation", len(toast.msgs))
	}
}

func TestCellViewer_YankNilClipboardNoOps(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, _ := newCellViewerWithGate(picker)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	_ = openCmd.Handler(commands.ExecCtx{})

	yankCmd, _ := reg.Get("cell_viewer.yank")
	if err := yankCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Yank nil clipboard: %v", err)
	}
}

func TestCellViewer_YankClipboardErrorShowsToast(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	toast := &fakeToast{}
	cb := &fakeClipboard{writeErr: errors.New("clipboard error")}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: toast}, ctx, tree, picker)
	ctrl.SetClipboard(cb)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	_ = openCmd.Handler(commands.ExecCtx{})

	yankCmd, _ := reg.Get("cell_viewer.yank")
	if err := yankCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Yank: %v", err)
	}

	if len(toast.msgs) != 1 {
		t.Fatalf("toast count = %d, want 1 error toast", len(toast.msgs))
	}
}

func TestCellViewer_Scroll(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "line",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, ctx, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	downCmd, _ := reg.Get("cell_viewer.scroll_down")
	upCmd, _ := reg.Get("cell_viewer.scroll_up")

	if err := downCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown: %v", err)
	}
	if ctx.ScrollY() != 1 {
		t.Errorf("ScrollY = %d after j, want 1", ctx.ScrollY())
	}

	if err := downCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown second: %v", err)
	}
	if ctx.ScrollY() != 2 {
		t.Errorf("ScrollY = %d after second j, want 2", ctx.ScrollY())
	}

	if err := upCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollUp: %v", err)
	}
	if ctx.ScrollY() != 1 {
		t.Errorf("ScrollY = %d after k, want 1", ctx.ScrollY())
	}

	if err := upCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollUp second: %v", err)
	}
	if ctx.ScrollY() != 0 {
		t.Errorf("ScrollY = %d after second k, want 0", ctx.ScrollY())
	}
}

func TestCellViewer_ScrollInactiveNoOps(t *testing.T) {
	ctrl, ctx, _ := newCellViewerWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	downCmd, _ := reg.Get("cell_viewer.scroll_down")
	if err := downCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("ScrollDown inactive: %v", err)
	}
	if ctx.ScrollY() != 0 {
		t.Errorf("ScrollY = %d on inactive, want 0", ctx.ScrollY())
	}
}

func TestCellViewer_EditBridgeDisabled(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctx := newCellViewerTestCtx()
	tree := &fakeFocusTree{}
	toast := &fakeToast{}
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{Toast: toast}, ctx, tree, picker)

	cellEditor := controllers.NewCellEditorController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil, nil, nil)
	ctrl.SetCellEditor(cellEditor)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	editCmd, ok := reg.Get("cell_viewer.edit")
	if !ok {
		t.Fatal("cell_viewer.edit not registered")
	}
	if err := editCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Edit: %v", err)
	}

	if len(toast.msgs) != 1 {
		t.Fatalf("toast count = %d, want 1 disabled reason", len(toast.msgs))
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after failed edit bridge; viewer should stay open")
	}
}

func TestCellViewer_EditBridgeInactiveNoOps(t *testing.T) {
	ctrl, _, _ := newCellViewerWithGate(&fakeGridPicker{})
	cellEditor := controllers.NewCellEditorController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil, nil, nil)
	ctrl.SetCellEditor(cellEditor)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	editCmd, _ := reg.Get("cell_viewer.edit")
	if err := editCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Edit inactive: %v", err)
	}
}

func TestCellViewer_EditBridgeNilEditorNoOps(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, _ := newCellViewerWithGate(picker)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	_ = openCmd.Handler(commands.ExecCtx{})

	editCmd, _ := reg.Get("cell_viewer.edit")
	if err := editCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Edit nil editor: %v", err)
	}
}

func TestCellViewerControllerKeybindings(t *testing.T) {
	ctrl, _, _ := newCellViewerWithGate(&fakeGridPicker{})

	type sigKey struct {
		Action string
		Scope  types.ContextKey
		Mode   types.Mode
	}
	have := map[sigKey]bool{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		have[sigKey{kb.ActionID, kb.Scope, kb.Mode}] = true
	}

	viewerScope := guicontext.CellViewerKey()
	want := []sigKey{
		{commands.ResultViewCellOpen, types.RESULT_GRID, types.ModeNormal},
		{"cell_viewer.scroll_down", viewerScope, types.ModeNormal},
		{"cell_viewer.scroll_up", viewerScope, types.ModeNormal},
		{"cell_viewer.half_page_down", viewerScope, types.ModeNormal},
		{"cell_viewer.half_page_up", viewerScope, types.ModeNormal},
		{"cell_viewer.page_down", viewerScope, types.ModeNormal},
		{"cell_viewer.page_up", viewerScope, types.ModeNormal},
		{"cell_viewer.jump_top", viewerScope, types.ModeNormal},
		{"cell_viewer.jump_bottom", viewerScope, types.ModeNormal},
		{"cell_viewer.scroll_left", viewerScope, types.ModeNormal},
		{"cell_viewer.scroll_right", viewerScope, types.ModeNormal},
		{"cell_viewer.toggle_wrap", viewerScope, types.ModeNormal},
		{"cell_viewer.toggle_pretty", viewerScope, types.ModeNormal},
		{"cell_viewer.yank", viewerScope, types.ModeNormal},
		{"cell_viewer.edit", viewerScope, types.ModeNormal},
		{"cell_viewer.dismiss", viewerScope, types.ModeNormal},
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing binding for action=%s scope=%s mode=%v", w.Action, w.Scope, w.Mode)
		}
	}

	dismissCount := 0
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.ActionID == "cell_viewer.dismiss" {
			dismissCount++
		}
	}
	if dismissCount != 2 {
		t.Errorf("cell_viewer.dismiss bound %d times; want 2 (<esc> and <c-c>)", dismissCount)
	}
}

func TestCellViewer_NilCollaboratorsAreSafe(t *testing.T) {
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, id := range []string{
		commands.ResultViewCellOpen,
		"cell_viewer.scroll_down",
		"cell_viewer.scroll_up",
		"cell_viewer.toggle_wrap",
		"cell_viewer.toggle_pretty",
		"cell_viewer.yank",
		"cell_viewer.edit",
		"cell_viewer.dismiss",
	} {
		cmd, ok := reg.Get(id)
		if !ok || cmd == nil {
			t.Errorf("ID %q not registered", id)
			continue
		}
		if err := cmd.Handler(commands.ExecCtx{}); err != nil {
			t.Errorf("handler for %q returned %v", id, err)
		}
	}
}

func TestCellViewer_GetDisabledViewerAlreadyActive(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get(commands.ResultViewCellOpen)
	if !ok {
		t.Fatal("ResultViewCellOpen not registered")
	}

	reason, disabled := cmd.Disabled(commands.ExecCtx{})
	if disabled {
		t.Fatalf("Open disabled before first open: %s", reason)
	}

	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open: %v", err)
	}

	reason, disabled = cmd.Disabled(commands.ExecCtx{})
	if !disabled {
		t.Fatal("Open enabled when viewer already active")
	}
	if reason != "viewer already active" {
		t.Errorf("reason = %q, want %q", reason, "viewer already active")
	}
}

func TestCellViewer_GetDisabledScrollLeftRightInWrapMode(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	_ = openCmd.Handler(commands.ExecCtx{})

	for _, id := range []string{"cell_viewer.scroll_left", "cell_viewer.scroll_right"} {
		cmd, ok := reg.Get(id)
		if !ok {
			t.Fatalf("%q not registered", id)
		}
		reason, disabled := cmd.Disabled(commands.ExecCtx{})
		if !disabled {
			t.Fatalf("%q not disabled in wrap mode", id)
		}
		if reason != "disabled in wrap mode" {
			t.Errorf("%q reason = %q, want %q", id, reason, "disabled in wrap mode")
		}
	}
}

func TestCellViewer_GetDisabledEditWhenViewerInactive(t *testing.T) {
	ctrl, _, _ := newCellViewerWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, ok := reg.Get("cell_viewer.edit")
	if !ok {
		t.Fatal("cell_viewer.edit not registered")
	}
	reason, disabled := cmd.Disabled(commands.ExecCtx{})
	if !disabled {
		t.Fatal("Edit enabled when no active viewer")
	}
	if reason != "no active viewer" {
		t.Errorf("reason = %q, want %q", reason, "no active viewer")
	}
}

func TestCellViewer_GetDisabledEditWhenEditorNotWired(t *testing.T) {
	picker := &fakeGridPicker{
		cell:   "x",
		column: models.ColumnMeta{Name: "c"},
		pk:     []any{int64(1)},
		cellOK: true,
	}
	ctrl, _, _ := newCellViewerWithGate(picker)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	openCmd, _ := reg.Get(commands.ResultViewCellOpen)
	_ = openCmd.Handler(commands.ExecCtx{})

	cmd, _ := reg.Get("cell_viewer.edit")
	reason, disabled := cmd.Disabled(commands.ExecCtx{})
	if !disabled {
		t.Fatal("Edit enabled when editor not wired")
	}
	if reason != "editor not wired" {
		t.Errorf("reason = %q, want %q", reason, "editor not wired")
	}
}

func TestCellViewer_RegisterActionsHandlesNilRegistry(t *testing.T) {
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil, nil)
	ctrl.RegisterActions(nil)
}

func TestCellViewer_AttachToContextNilSafe(t *testing.T) {
	ctrl := controllers.NewCellViewerController(nil, controllers.CoreDeps{}, controllers.UIDeps{}, nil, nil, nil)
	ctrl.AttachToContext(nil)
}
