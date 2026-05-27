package controllers

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// fakeReverseTree records Pop invocations for the picker controller.
type fakeReverseTree struct{ pops int }

func (f *fakeReverseTree) Pop() error { f.pops++; return nil }

// fakeReverseRunner returns the canned RunHandle / err on every RunQuery.
// The captured query is exposed for assertion.
type fakeReverseRunner struct {
	mu  sync.Mutex
	rh  *session.RunHandle
	err error
	got models.Query
}

func (f *fakeReverseRunner) RunQuery(_ context.Context, q models.Query) (*session.RunHandle, error) {
	f.mu.Lock()
	f.got = q
	f.mu.Unlock()
	return f.rh, f.err
}

func (f *fakeReverseRunner) Captured() models.Query {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got
}

// fakeReverseTabs records OpenResultTab calls.
type fakeReverseTabs struct {
	openLabel string
	openRH    *session.RunHandle
	openErr   error
	calls     int
}

func (f *fakeReverseTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	f.calls++
	f.openLabel = label
	f.openRH = rh
	return f.openErr
}

// fakeReverseJumps records JumpEntry pushes.
type fakeReverseJumps struct {
	pushed []ui.JumpEntry
}

func (f *fakeReverseJumps) Push(e ui.JumpEntry) { f.pushed = append(f.pushed, e) }

// fakeReverseToast records messages so tests can assert toasts fired.
type fakeReverseToast struct {
	msgs []string
}

func (f *fakeReverseToast) Show(msg string, _ time.Duration) { f.msgs = append(f.msgs, msg) }

// fakeReverseOrigin satisfies FKReverseOriginTab.
type fakeReverseOrigin struct {
	slot int
	id   int64
}

func (f *fakeReverseOrigin) Slot() int { return f.slot }
func (f *fakeReverseOrigin) ID() int64 { return f.id }

func newReversePickerContext() *guicontext.FKReversePickerContext {
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{
		Key:      guicontext.FKReversePickerContextKey,
		ViewName: string(guicontext.FKReversePickerContextKey),
		Kind:     types.TEMPORARY_POPUP,
	})
	return guicontext.NewFKReversePickerContext(base, guicontext.Deps{})
}

// --- ReversePanel.Body / renderReltuples ----------------------------------

func TestReversePanel_Body_SimpleFK(t *testing.T) {
	p := NewReversePanel(ReverseEntry{
		FK: models.ForeignKey{
			Schema:  "app",
			Table:   "orders",
			Columns: []string{"user_id"},
		},
		Reltuples: 50,
	})
	got := p.Body()
	if !strings.Contains(got, "app.orders(user_id)") {
		t.Errorf("Body missing referencing identity: %q", got)
	}
	if !strings.Contains(got, "~50 rows") {
		t.Errorf("Body missing reltuples line: %q", got)
	}
}

func TestReversePanel_Body_CompositeFK(t *testing.T) {
	p := NewReversePanel(ReverseEntry{
		FK: models.ForeignKey{
			Schema:  "app",
			Table:   "child",
			Columns: []string{"a", "b"},
		},
		Reltuples: 12,
	})
	if got := p.Body(); !strings.Contains(got, "app.child(a, b)") {
		t.Errorf("composite Body missing both cols: %q", got)
	}
}

func TestRenderReltuples_Cases(t *testing.T) {
	cases := []struct {
		in   float32
		want string
	}{
		{in: 50, want: "~50 rows"},
		{in: 0, want: "~0 rows"},
		{in: -1, want: "~? rows"},
		{in: 0.7, want: "~1 rows"},    // fractional → ceil
		{in: 1.0001, want: "~2 rows"}, // rounds UP
	}
	for _, c := range cases {
		if got := renderReltuples(c.in); got != c.want {
			t.Errorf("renderReltuples(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- buildFKReverseSQL ----------------------------------------------------

func TestBuildFKReverseSQL_SimpleFK_QuotesIdentifiers(t *testing.T) {
	sql := buildFKReverseSQL(models.ForeignKey{
		Schema:  "app",
		Table:   "orders",
		Columns: []string{"user_id"},
	})
	const want = `SELECT * FROM "app"."orders" WHERE "user_id"=$1`
	if sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
}

func TestBuildFKReverseSQL_CompositeFK_AndedPositionalArgs(t *testing.T) {
	sql := buildFKReverseSQL(models.ForeignKey{
		Schema:  "app",
		Table:   "child",
		Columns: []string{"a", "b"},
	})
	const want = `SELECT * FROM "app"."child" WHERE "a"=$1 AND "b"=$2`
	if sql != want {
		t.Errorf("composite sql = %q, want %q", sql, want)
	}
}

func TestBuildFKReverseSQL_UnqualifiedSchema(t *testing.T) {
	sql := buildFKReverseSQL(models.ForeignKey{
		Schema:  "",
		Table:   "orders",
		Columns: []string{"user_id"},
	})
	const want = `SELECT * FROM "orders" WHERE "user_id"=$1`
	if sql != want {
		t.Errorf("unqualified sql = %q, want %q", sql, want)
	}
}

func TestBuildFKReverseSQL_MixedCaseIdentifiersRoundTrip(t *testing.T) {
	sql := buildFKReverseSQL(models.ForeignKey{
		Schema:  "App",
		Table:   "Orders",
		Columns: []string{"UserID"},
	})
	const want = `SELECT * FROM "App"."Orders" WHERE "UserID"=$1`
	if sql != want {
		t.Errorf("mixed-case sql = %q, want %q", sql, want)
	}
}

// --- Open + tab cycling ---------------------------------------------------

func TestOpen_EmptyEntriesNoStateInstalled(t *testing.T) {
	ctx := newReversePickerContext()
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Context: ctx})
	if ok := c.Open(nil, nil, 0, 0); ok {
		t.Fatal("Open returned true with empty entries; want false")
	}
	if ctx.State() != nil {
		t.Error("Open with no entries leaked a popup state onto the context")
	}
}

func TestOpen_OneTabPerEntry_TitleIncludesReferencingColumn(t *testing.T) {
	ctx := newReversePickerContext()
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Context: ctx})
	entries := []ReverseEntry{
		{FK: models.ForeignKey{Schema: "app", Table: "orders", Columns: []string{"user_id"}}, Reltuples: 50, PKValues: []any{int64(1)}},
		{FK: models.ForeignKey{Schema: "app", Table: "comments", Columns: []string{"author_id"}}, Reltuples: -1, PKValues: []any{int64(1)}},
	}
	if !c.Open(entries, nil, 3, 0) {
		t.Fatal("Open returned false with non-empty entries")
	}
	state := ctx.State()
	if state == nil {
		t.Fatal("Open did not install state on context")
	}
	body := state.Body()
	// Header carries both titles; the active tab is at index 0.
	if !strings.Contains(body, "orders.user_id") {
		t.Errorf("body missing first tab title: %q", body)
	}
	if !strings.Contains(body, "comments.author_id") {
		t.Errorf("body missing second tab title: %q", body)
	}
	// Active panel body present.
	if !strings.Contains(body, "~50 rows") {
		t.Errorf("body missing active panel reltuples: %q", body)
	}
}

func TestNextTab_PrevTab_WrapAround(t *testing.T) {
	ctx := newReversePickerContext()
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Context: ctx})
	entries := []ReverseEntry{
		{FK: models.ForeignKey{Schema: "s", Table: "a", Columns: []string{"x"}}, PKValues: []any{1}},
		{FK: models.ForeignKey{Schema: "s", Table: "b", Columns: []string{"x"}}, PKValues: []any{1}},
	}
	c.Open(entries, nil, 0, 0)
	if ctx.State().Active() != 0 {
		t.Fatalf("Active() = %d, want 0 at construction", ctx.State().Active())
	}
	_ = c.NextTab(commands.ExecCtx{})
	if ctx.State().Active() != 1 {
		t.Errorf("NextTab → Active() = %d, want 1", ctx.State().Active())
	}
	_ = c.NextTab(commands.ExecCtx{})
	if ctx.State().Active() != 0 {
		t.Errorf("NextTab wrap → Active() = %d, want 0", ctx.State().Active())
	}
	_ = c.PrevTab(commands.ExecCtx{})
	if ctx.State().Active() != 1 {
		t.Errorf("PrevTab wrap → Active() = %d, want 1", ctx.State().Active())
	}
}

func TestClose_PopsTree(t *testing.T) {
	tree := &fakeReverseTree{}
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Tree: tree})
	if err := c.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if tree.pops != 1 {
		t.Errorf("pops = %d, want 1", tree.pops)
	}
}

func TestClose_NilTreeIsNoop(t *testing.T) {
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{})
	if err := c.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close with nil tree should no-op, got %v", err)
	}
}

// --- Select ---------------------------------------------------------------

func TestSelect_BuildsQuery_PushesJump_OpensTab_PopsPopup(t *testing.T) {
	ctx := newReversePickerContext()
	tree := &fakeReverseTree{}
	runner := &fakeReverseRunner{rh: nil}
	tabs := &fakeReverseTabs{}
	jumps := &fakeReverseJumps{}
	toast := &fakeReverseToast{}
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{
		Context: ctx, Tree: tree, Runner: runner, Tabs: tabs, Jumps: jumps, Toast: toast,
	})

	entries := []ReverseEntry{
		{
			FK: models.ForeignKey{
				Schema:     "app",
				Table:      "orders",
				Columns:    []string{"user_id"},
				RefSchema:  "app",
				RefTable:   "users",
				RefColumns: []string{"id"},
			},
			Reltuples: 50,
			PKValues:  []any{int64(7)},
		},
	}
	origin := &fakeReverseOrigin{slot: 2, id: 99}
	c.Open(entries, origin, 5, 3)

	if err := c.Select(commands.ExecCtx{}); err != nil {
		t.Fatalf("Select: %v", err)
	}

	// Query was built correctly.
	got := runner.Captured()
	const wantSQL = `SELECT * FROM "app"."orders" WHERE "user_id"=$1`
	if got.SQL != wantSQL {
		t.Errorf("Select sql = %q, want %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 1 || got.Args[0] != int64(7) {
		t.Errorf("Select args = %v, want [7]", got.Args)
	}

	// Jump entry pushed before opening the tab (B5 invariant mirrored).
	if len(jumps.pushed) != 1 {
		t.Fatalf("jump pushes = %d, want 1", len(jumps.pushed))
	}
	je := jumps.pushed[0]
	if je.TabSlot != 2 || je.TabID != "99" || je.Row != 5 || je.Col != 3 {
		t.Errorf("JumpEntry = %+v, want slot=2 id=99 row=5 col=3", je)
	}

	// Result tab opened.
	if tabs.calls != 1 {
		t.Errorf("OpenResultTab calls = %d, want 1", tabs.calls)
	}
	if !strings.Contains(tabs.openLabel, "orders") || !strings.Contains(tabs.openLabel, "7") {
		t.Errorf("OpenResultTab label = %q, want to contain orders + 7", tabs.openLabel)
	}

	// Popup dismissed.
	if tree.pops != 1 {
		t.Errorf("tree.pops = %d, want 1", tree.pops)
	}
	// No toasts on the happy path.
	if len(toast.msgs) != 0 {
		t.Errorf("happy-path toasts = %v, want none", toast.msgs)
	}
}

func TestSelect_CompositeFK_BuildsAndedArgs(t *testing.T) {
	ctx := newReversePickerContext()
	runner := &fakeReverseRunner{}
	tabs := &fakeReverseTabs{}
	jumps := &fakeReverseJumps{}
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{
		Context: ctx, Tree: &fakeReverseTree{}, Runner: runner, Tabs: tabs, Jumps: jumps,
	})
	entries := []ReverseEntry{
		{
			FK: models.ForeignKey{
				Schema:     "app",
				Table:      "child",
				Columns:    []string{"a", "b"},
				RefSchema:  "app",
				RefTable:   "parent",
				RefColumns: []string{"a", "b"},
			},
			PKValues: []any{1, 2},
		},
	}
	c.Open(entries, nil, 0, 0)
	if err := c.Select(commands.ExecCtx{}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	got := runner.Captured()
	const wantSQL = `SELECT * FROM "app"."child" WHERE "a"=$1 AND "b"=$2`
	if got.SQL != wantSQL {
		t.Errorf("composite sql = %q, want %q", got.SQL, wantSQL)
	}
	if len(got.Args) != 2 || got.Args[0] != 1 || got.Args[1] != 2 {
		t.Errorf("composite args = %v, want [1 2]", got.Args)
	}
}

func TestSelect_PKMismatch_ToastsAndDoesNotRun(t *testing.T) {
	ctx := newReversePickerContext()
	runner := &fakeReverseRunner{}
	tabs := &fakeReverseTabs{}
	jumps := &fakeReverseJumps{}
	toast := &fakeReverseToast{}
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{
		Context: ctx, Tree: &fakeReverseTree{}, Runner: runner, Tabs: tabs, Jumps: jumps, Toast: toast,
	})
	entries := []ReverseEntry{
		{
			FK: models.ForeignKey{
				Schema: "app", Table: "child",
				Columns:    []string{"a", "b"},
				RefColumns: []string{"a", "b"},
			},
			PKValues: []any{1}, // mismatch — need 2
		},
	}
	c.Open(entries, nil, 0, 0)
	if err := c.Select(commands.ExecCtx{}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	if runner.Captured().SQL != "" {
		t.Errorf("runner should NOT have been invoked on pk mismatch; got %q", runner.Captured().SQL)
	}
	if tabs.calls != 0 {
		t.Error("OpenResultTab should NOT have fired on pk mismatch")
	}
	if len(toast.msgs) == 0 || !strings.Contains(toast.msgs[0], "pk value count mismatch") {
		t.Errorf("expected mismatch toast; got %v", toast.msgs)
	}
}

func TestSelect_RunnerError_ToastsAndDoesNotOpenTabOrPop(t *testing.T) {
	ctx := newReversePickerContext()
	tree := &fakeReverseTree{}
	runner := &fakeReverseRunner{err: errors.New("permission denied for table orders")}
	tabs := &fakeReverseTabs{}
	jumps := &fakeReverseJumps{}
	toast := &fakeReverseToast{}
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{
		Context: ctx, Tree: tree, Runner: runner, Tabs: tabs, Jumps: jumps, Toast: toast,
	})
	entries := []ReverseEntry{
		{
			FK: models.ForeignKey{
				Schema: "app", Table: "orders",
				Columns:    []string{"user_id"},
				RefColumns: []string{"id"},
			},
			PKValues: []any{1},
		},
	}
	c.Open(entries, nil, 0, 0)
	if err := c.Select(commands.ExecCtx{}); err != nil {
		t.Fatalf("Select: %v", err)
	}
	// Jump pushed BEFORE the runner call (mirrors B5 invariant).
	if len(jumps.pushed) != 1 {
		t.Errorf("jumps.pushed = %d, want 1 (push fires before runner)", len(jumps.pushed))
	}
	if tabs.calls != 0 {
		t.Error("OpenResultTab should not have fired on runner error")
	}
	if tree.pops != 0 {
		t.Error("Pop should not have fired on runner error — popup stays open")
	}
	if len(toast.msgs) == 0 || !strings.Contains(toast.msgs[0], "permission denied") {
		t.Errorf("expected runner-error toast; got %v", toast.msgs)
	}
}

func TestSelect_SelfReferencingFK_RendersAsSeparateTab(t *testing.T) {
	ctx := newReversePickerContext()
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Context: ctx})
	// Self-ref: tree.parent_id → tree.id. Schema/Table = RefSchema/RefTable.
	entries := []ReverseEntry{
		{
			FK: models.ForeignKey{
				Schema:     "app",
				Table:      "tree",
				Columns:    []string{"parent_id"},
				RefSchema:  "app",
				RefTable:   "tree",
				RefColumns: []string{"id"},
			},
			Reltuples: 5,
			PKValues:  []any{int64(1)},
		},
	}
	c.Open(entries, nil, 0, 0)
	state := ctx.State()
	if state == nil {
		t.Fatal("Open did not install state for self-ref FK")
	}
	body := state.Body()
	if !strings.Contains(body, "tree.parent_id") {
		t.Errorf("self-ref tab title missing referencing identity; body=%q", body)
	}
}

// --- Keybindings ----------------------------------------------------------

func TestGetKeybindings_FullSet(t *testing.T) {
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{})
	got := c.GetKeybindings(types.KeybindingsOpts{})
	if len(got) != 6 {
		t.Fatalf("len(bindings) = %d, want 6 (tab,],[,<cr>,<esc>,q)", len(got))
	}
	wantScopes := map[string]bool{
		FKReverseNextTab: false,
		FKReversePrevTab: false,
		FKReverseSelect:  false,
		FKReverseClose:   false,
	}
	for _, b := range got {
		if b.Scope != guicontext.FKReversePickerContextKey {
			t.Errorf("binding %s scope = %s, want fk_reverse_picker", b.ActionID, b.Scope)
		}
		if _, ok := wantScopes[b.ActionID]; ok {
			wantScopes[b.ActionID] = true
		}
	}
	for id, seen := range wantScopes {
		if !seen {
			t.Errorf("missing binding for action %q", id)
		}
	}
}

func TestRegisterActions_AllHandlersResolveThroughRegistry(t *testing.T) {
	reg := commands.NewRegistry()
	c := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{})
	c.RegisterActions(reg)
	for _, id := range []string{FKReverseNextTab, FKReversePrevTab, FKReverseSelect, FKReverseClose} {
		if !reg.Has(id) {
			t.Errorf("registry missing action %q", id)
		}
	}
}
