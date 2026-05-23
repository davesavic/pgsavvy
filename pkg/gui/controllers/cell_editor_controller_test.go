package controllers_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeGridPicker is a hand-rolled controllers.GridStatePicker fake.
// Every field is read-only from the controller's perspective; tests
// twiddle them directly to drive the precondition-disabled paths.
type fakeGridPicker struct {
	editable           bool
	streaming          bool
	supportsInlineEdit bool
	readOnly           bool
	disabledReason     string

	cell      any
	column    models.ColumnMeta
	pk        []any
	cellOK    bool
	snapshots int
}

func (f *fakeGridPicker) Editable() bool           { return f.editable }
func (f *fakeGridPicker) IsStreaming() bool        { return f.streaming }
func (f *fakeGridPicker) SupportsInlineEdit() bool { return f.supportsInlineEdit }
func (f *fakeGridPicker) IsReadOnly() bool         { return f.readOnly }
func (f *fakeGridPicker) DisabledReason() string   { return f.disabledReason }

func (f *fakeGridPicker) CellSnapshot() (any, models.ColumnMeta, []any, bool) {
	f.snapshots++
	// Return a fresh PK slice each call so the controller's defensive
	// copy in CellEditorContext.Open isn't accidentally relying on
	// shared backing memory.
	pk := append([]any(nil), f.pk...)
	return f.cell, f.column, pk, f.cellOK
}

func (f *fakeGridPicker) FormatForEdit(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// fakeEditStore is a hand-rolled controllers.PendingEditStore fake.
// Backed by a slice rather than the real PendingEditSet so the test
// can introspect insertion order without reaching into models package
// internals.
type fakeEditStore struct {
	adds    []models.PendingEdit
	removes []removeArgs
	addErr  error
	preset  []models.PendingEdit
}

type removeArgs struct {
	PK  []any
	Col string
}

func (f *fakeEditStore) Add(e models.PendingEdit) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.adds = append(f.adds, e)
	return nil
}

func (f *fakeEditStore) Remove(pk []any, col string) {
	f.removes = append(f.removes, removeArgs{PK: append([]any(nil), pk...), Col: col})
	out := f.preset[:0]
	for _, p := range f.preset {
		if p.Column == col && pkEqual(p.PrimaryKey, pk) {
			continue
		}
		out = append(out, p)
	}
	f.preset = out
}

func (f *fakeEditStore) HasEdit(pk []any, col string) bool {
	for _, p := range f.preset {
		if p.Column == col && pkEqual(p.PrimaryKey, pk) {
			return true
		}
	}
	return false
}

func pkEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

// fakeFocusTree is a hand-rolled controllers.FocusPopper fake. Tracks
// Push / Pop call counts + the last pushed context so tests can assert
// the popup lifecycle without spinning up the real *gui.ContextTree.
type fakeFocusTree struct {
	pushes    int
	pops      int
	pushedCtx types.IBaseContext
	pushErr   error
	popErr    error
}

func (f *fakeFocusTree) Push(c types.IBaseContext) error {
	f.pushes++
	f.pushedCtx = c
	return f.pushErr
}

func (f *fakeFocusTree) Pop() error { f.pops++; return f.popErr }

func newCellEditorTestCtx() *guicontext.CellEditorContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.CellEditorKey(),
		ViewName: string(guicontext.CellEditorKey()),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewCellEditorContext(base, guicontext.Deps{})
}

func newCellEditorWithGate(picker *fakeGridPicker) (
	*controllers.CellEditorController,
	*guicontext.CellEditorContext,
	*fakeFocusTree,
	*fakeEditStore,
) {
	ctx := newCellEditorTestCtx()
	tree := &fakeFocusTree{}
	store := &fakeEditStore{}
	ctrl := controllers.NewCellEditorController(nil, controllers.HelperBag{}, ctx, tree, picker, store)
	return ctrl, ctx, tree, store
}

// TestCellEditorControllerKeybindings asserts the controller publishes
// the seven bindings the AC mandates (i / cr / esc / c-c / c-n / c-t /
// c-d / c-e), scoped + moded correctly.
func TestCellEditorControllerKeybindings(t *testing.T) {
	ctrl, _, _, _ := newCellEditorWithGate(&fakeGridPicker{})

	type sigKey struct {
		Action string
		Scope  types.ContextKey
		Mode   types.Mode
	}
	have := map[sigKey]bool{}
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		have[sigKey{kb.ActionID, kb.Scope, kb.Mode}] = true
	}

	scope := guicontext.CellEditorKey()
	want := []sigKey{
		{controllers.CellEditEnter, types.RESULT_GRID, types.ModeNormal},
		{controllers.CellEditCommit, scope, types.ModeInsert},  // <cr>
		{controllers.CellEditDiscard, scope, types.ModeInsert}, // <c-c>
		{controllers.CellEditSetNull, scope, types.ModeInsert}, // A2
		{controllers.CellEditExprNow, scope, types.ModeInsert}, // A2
		{controllers.CellEditExprCurrentDate, scope, types.ModeInsert},
		{controllers.CellEditExprPrompt, scope, types.ModeInsert},
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing binding for action=%s scope=%s mode=%v", w.Action, w.Scope, w.Mode)
		}
	}

	// Both <cr> and <esc> map to Commit — assert by counting Commit
	// entries in the binding slice.
	commits := 0
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.ActionID == controllers.CellEditCommit {
			commits++
		}
	}
	if commits != 2 {
		t.Errorf("CellEditCommit bound %d times; want 2 (<cr> and <esc>)", commits)
	}
}

// TestCellEditorControllerEnterPushesOnEditableGrid asserts the happy
// path: editable grid, all gates pass, Enter snapshots the cell and
// pushes the CELL_EDITOR popup.
func TestCellEditorControllerEnterPushesOnEditableGrid(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(1)},
		cellOK:             true,
	}
	ctrl, ctx, tree, _ := newCellEditorWithGate(picker)

	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if tree.pushes != 1 {
		t.Errorf("tree.pushes = %d, want 1", tree.pushes)
	}
	if !ctx.Active() {
		t.Error("ctx.Active() = false after Enter; want true")
	}
	if got := ctx.OriginalValue(); got != "alice" {
		t.Errorf("OriginalValue = %v, want %q", got, "alice")
	}
	if ctx.Buffer() != "alice" {
		t.Errorf("Buffer = %q, want seeded original", ctx.Buffer())
	}
}

// TestCellEditorControllerEnterDisabledReasons walks every disable
// path and asserts the registered command's GetDisabled predicate
// returns the AC-mandated reason string. Priority: read_only →
// driver-cap → streaming → editable.
func TestCellEditorControllerEnterDisabledReasons(t *testing.T) {
	cases := []struct {
		name   string
		picker *fakeGridPicker
		reason string
	}{
		{
			name:   "read-only connection",
			picker: &fakeGridPicker{readOnly: true, supportsInlineEdit: true, editable: true},
			reason: "read-only connection",
		},
		{
			name:   "driver does not support inline edit",
			picker: &fakeGridPicker{supportsInlineEdit: false, editable: true},
			reason: "driver does not support inline edit",
		},
		{
			name:   "streaming",
			picker: &fakeGridPicker{supportsInlineEdit: true, streaming: true, editable: true},
			reason: "wait for current stream to finish",
		},
		{
			name:   "not editable with explicit reason",
			picker: &fakeGridPicker{supportsInlineEdit: true, editable: false, disabledReason: "no row identity"},
			reason: "no row identity",
		},
		{
			name:   "not editable with empty reason falls back to generic",
			picker: &fakeGridPicker{supportsInlineEdit: true, editable: false},
			reason: "result is not inline-editable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl, _, _, _ := newCellEditorWithGate(tc.picker)
			reg := commands.NewRegistry()
			ctrl.RegisterActions(reg)

			cmd, ok := reg.Get(controllers.CellEditEnter)
			if !ok || cmd == nil {
				t.Fatal("CellEditEnter not registered")
			}
			reason, disabled := cmd.Disabled(commands.ExecCtx{})
			if !disabled {
				t.Fatalf("Disabled() = false; want true for case %q", tc.name)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}

// TestCellEditorControllerEnterDisabledWithNilPicker asserts the no-
// picker case is reported as disabled (the controller can't gate
// without state, so it defaults to safe-off).
func TestCellEditorControllerEnterDisabledWithNilPicker(t *testing.T) {
	ctx := newCellEditorTestCtx()
	ctrl := controllers.NewCellEditorController(nil, controllers.HelperBag{}, ctx, &fakeFocusTree{}, nil, nil)
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	cmd, _ := reg.Get(controllers.CellEditEnter)
	reason, disabled := cmd.Disabled(commands.ExecCtx{})
	if !disabled {
		t.Fatal("Disabled = false with nil picker; want true")
	}
	if reason != "no active result grid" {
		t.Errorf("reason = %q, want %q", reason, "no active result grid")
	}
}

// TestCellEditorControllerCommitWithChangeAddsPendingEdit asserts the
// AC happy path: typed buffer differs from original → PendingEdit is
// staged with the right (pk, col, OldValue, NewValue, Kind=Literal).
func TestCellEditorControllerCommitWithChangeAddsPendingEdit(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(7)},
		cellOK:             true,
	}
	ctrl, ctx, tree, store := newCellEditorWithGate(picker)

	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	ctx.SetBuffer("bob")
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if len(store.adds) != 1 {
		t.Fatalf("store.adds = %d, want 1", len(store.adds))
	}
	e := store.adds[0]
	if e.Column != "name" {
		t.Errorf("Column = %q, want %q", e.Column, "name")
	}
	if e.NewValue != "bob" {
		t.Errorf("NewValue = %v, want %q", e.NewValue, "bob")
	}
	if e.OldValue != "alice" {
		t.Errorf("OldValue = %v, want %q", e.OldValue, "alice")
	}
	if e.Kind != models.Literal {
		t.Errorf("Kind = %v, want models.Literal", e.Kind)
	}
	if !reflect.DeepEqual(e.PrimaryKey, []any{int64(7)}) {
		t.Errorf("PrimaryKey = %v, want [7]", e.PrimaryKey)
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	if ctx.Active() {
		t.Error("ctx.Active() = true after Commit; want false")
	}
}

// TestCellEditorControllerCommitNoChangeElides asserts the AC: typing
// the same value back (or just <esc>'ing without typing) MUST NOT
// stage a PendingEdit.
func TestCellEditorControllerCommitNoChangeElides(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(1)},
		cellOK:             true,
	}
	ctrl, _, tree, store := newCellEditorWithGate(picker)

	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	// Buffer still == seeded original.
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if len(store.adds) != 0 {
		t.Errorf("store.adds = %d, want 0 (no change)", len(store.adds))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
}

// TestCellEditorControllerDiscardCleanCellPopsWithoutRecording
// asserts the AC: <c-c> on a clean cell pops without recording.
func TestCellEditorControllerDiscardCleanCellPopsWithoutRecording(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(1)},
		cellOK:             true,
	}
	ctrl, ctx, tree, store := newCellEditorWithGate(picker)
	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	ctx.SetBuffer("bob")
	if err := ctrl.Discard(commands.ExecCtx{}); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if len(store.adds) != 0 {
		t.Errorf("store.adds = %d, want 0", len(store.adds))
	}
	if len(store.removes) != 0 {
		t.Errorf("store.removes = %d, want 0 on clean cell", len(store.removes))
	}
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
}

// TestCellEditorControllerDiscardDirtyCellRemovesAndToasts asserts the
// AC: <c-c> on a cell with a pre-existing PendingEdit removes the
// staged edit AND emits the discard toast.
func TestCellEditorControllerDiscardDirtyCellRemovesAndToasts(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(7)},
		cellOK:             true,
	}
	ctx := newCellEditorTestCtx()
	tree := &fakeFocusTree{}
	store := &fakeEditStore{
		preset: []models.PendingEdit{
			{PrimaryKey: []any{int64(7)}, Column: "name", NewValue: "prior", Kind: models.Literal},
		},
	}
	toast := &fakeToast{}
	helpers := controllers.HelperBag{Toast: toast}
	ctrl := controllers.NewCellEditorController(nil, helpers, ctx, tree, picker, store)

	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if err := ctrl.Discard(commands.ExecCtx{}); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if len(store.removes) != 1 {
		t.Fatalf("store.removes = %d, want 1", len(store.removes))
	}
	if store.removes[0].Col != "name" {
		t.Errorf("removed col = %q, want %q", store.removes[0].Col, "name")
	}
	if !reflect.DeepEqual(store.removes[0].PK, []any{int64(7)}) {
		t.Errorf("removed pk = %v, want [7]", store.removes[0].PK)
	}
	if len(toast.msgs) != 1 {
		t.Fatalf("toast count = %d, want 1", len(toast.msgs))
	}
	if got := toast.msgs[0].Msg; got == "" {
		t.Error("toast message empty; want discard wording")
	}
	if toast.msgs[0].TTL <= 0 || toast.msgs[0].TTL > 5*time.Second {
		t.Errorf("toast TTL = %v, want >0 and ≤5s", toast.msgs[0].TTL)
	}
}

// TestCellEditorControllerCommitReplacesExistingEdit asserts the AC
// edge case: a pre-existing PendingEdit on (pk, col), re-edit, commit
// new value → PendingEditSet.Add semantics replace in place. The
// controller delegates this to the store; the test just verifies the
// controller submits a fresh Add (the store's replacement semantics
// are F3's territory).
func TestCellEditorControllerCommitOnReeditSubmitsAdd(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "alice",
		column:             models.ColumnMeta{Name: "name"},
		pk:                 []any{int64(1)},
		cellOK:             true,
	}
	ctx := newCellEditorTestCtx()
	tree := &fakeFocusTree{}
	store := &fakeEditStore{
		preset: []models.PendingEdit{
			{PrimaryKey: []any{int64(1)}, Column: "name", NewValue: "prior", Kind: models.Literal},
		},
	}
	ctrl := controllers.NewCellEditorController(nil, controllers.HelperBag{}, ctx, tree, picker, store)
	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	ctx.SetBuffer("carol")
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(store.adds) != 1 {
		t.Fatalf("store.adds = %d, want 1", len(store.adds))
	}
	if store.adds[0].NewValue != "carol" {
		t.Errorf("NewValue = %v, want %q", store.adds[0].NewValue, "carol")
	}
}

// TestCellEditorControllerCommitCompositePKCarriesAllValues asserts
// the AC: composite-PK row → PendingEdit.PrimaryKey contains all PK
// column values in order.
func TestCellEditorControllerCommitCompositePKCarriesAllValues(t *testing.T) {
	picker := &fakeGridPicker{
		editable:           true,
		supportsInlineEdit: true,
		cell:               "v",
		column:             models.ColumnMeta{Name: "x"},
		pk:                 []any{int64(1), "a", true},
		cellOK:             true,
	}
	ctrl, ctx, _, store := newCellEditorWithGate(picker)
	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	ctx.SetBuffer("changed")
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(store.adds) != 1 {
		t.Fatalf("store.adds = %d, want 1", len(store.adds))
	}
	got := store.adds[0].PrimaryKey
	want := []any{int64(1), "a", true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrimaryKey = %v, want %v", got, want)
	}
}

// TestCellEditorControllerNilCollaboratorsAreSafe asserts every handler
// no-ops rather than panics when ctx / tree / store are nil. Production
// wiring (Z1) sets all three, but partial test setups must not crash.
func TestCellEditorControllerNilCollaboratorsAreSafe(t *testing.T) {
	ctrl := controllers.NewCellEditorController(nil, controllers.HelperBag{}, nil, nil, nil, nil)
	if err := ctrl.Enter(commands.ExecCtx{}); err != nil {
		t.Errorf("Enter nil-collaborators: %v", err)
	}
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Errorf("Commit nil-collaborators: %v", err)
	}
	if err := ctrl.Discard(commands.ExecCtx{}); err != nil {
		t.Errorf("Discard nil-collaborators: %v", err)
	}
}

// TestCellEditorControllerRegisterActionsHandlesNopBindings asserts
// the SetNull / Expr* IDs are registered (so the Matcher resolves them)
// but their handlers no-op (A2 will replace).
func TestCellEditorControllerRegisterActionsHandlesNopBindings(t *testing.T) {
	ctrl, _, _, _ := newCellEditorWithGate(&fakeGridPicker{})
	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)

	for _, id := range []string{
		controllers.CellEditSetNull,
		controllers.CellEditExprNow,
		controllers.CellEditExprCurrentDate,
		controllers.CellEditExprPrompt,
	} {
		cmd, ok := reg.Get(id)
		if !ok || cmd == nil {
			t.Errorf("ID %q not registered", id)
			continue
		}
		if err := cmd.Handler(commands.ExecCtx{}); err != nil {
			t.Errorf("noop handler for %q returned %v", id, err)
		}
	}
}

// TestCellEditorControllerCommitOnInactiveContextNoOps asserts a
// stale dispatch (Commit fired after the popup is already closed)
// is a silent no-op rather than a double-record.
func TestCellEditorControllerCommitOnInactiveContextNoOps(t *testing.T) {
	picker := &fakeGridPicker{
		editable: true, supportsInlineEdit: true, cellOK: true,
		cell: "v", column: models.ColumnMeta{Name: "x"}, pk: []any{int64(1)},
	}
	ctrl, _, tree, store := newCellEditorWithGate(picker)
	// No Enter — context never activated.
	if err := ctrl.Commit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(store.adds) != 0 || tree.pops != 0 {
		t.Errorf("stale Commit produced adds=%d pops=%d; want 0/0", len(store.adds), tree.pops)
	}
}
