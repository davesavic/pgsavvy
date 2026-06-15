package helpers_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	helpers "github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

// --- Fakes ---------------------------------------------------------------

type fakeCache struct {
	fks []models.ForeignKey
	err error
}

func (f *fakeCache) Get(ctx context.Context, schema, table string) ([]models.ForeignKey, error) {
	_ = ctx
	_ = schema
	_ = table
	if f.err != nil {
		return nil, f.err
	}
	return f.fks, nil
}

type fakeRunner struct {
	gotQuery models.Query
	rh       *session.RunHandle
	err      error
	calls    int
}

func (f *fakeRunner) RunQuery(ctx context.Context, q models.Query) (*session.RunHandle, error) {
	_ = ctx
	f.gotQuery = q
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.rh, nil
}

type fakeTabs struct {
	label    string
	gotRH    *session.RunHandle
	err      error
	openCall int
}

func (f *fakeTabs) OpenResultTab(label string, rh *session.RunHandle) error {
	f.label = label
	f.gotRH = rh
	f.openCall++
	return f.err
}

type fakeJumpList struct {
	pushed []ui.JumpEntry
}

func (f *fakeJumpList) Push(e ui.JumpEntry) { f.pushed = append(f.pushed, e) }

type fakeToast struct {
	messages []string
}

func (f *fakeToast) Show(msg string, ttl time.Duration) {
	_ = ttl
	f.messages = append(f.messages, msg)
}

type fakeBusy struct {
	busy bool
}

func (f *fakeBusy) IsBusy() bool { return f.busy }

type fakeTab struct {
	slot   int
	id     int64
	schema string
	table  string
	cols   []string
	rows   map[int][]any
}

func (f *fakeTab) Slot() int                       { return f.slot }
func (f *fakeTab) ID() int64                       { return f.id }
func (f *fakeTab) BaseTable() (string, string)     { return f.schema, f.table }
func (f *fakeTab) ColumnNames() []string           { return f.cols }
func (f *fakeTab) RowValues(row int) ([]any, bool) { v, ok := f.rows[row]; return v, ok }

// --- Helpers -------------------------------------------------------------

func simpleFK() models.ForeignKey {
	return models.ForeignKey{
		Name:       "fk_orders_user",
		Schema:     "app",
		Table:      "orders",
		Columns:    []string{"user_id"},
		RefSchema:  "app",
		RefTable:   "users",
		RefColumns: []string{"id"},
	}
}

func compositeFK() models.ForeignKey {
	return models.ForeignKey{
		Name:       "fk_child_parent",
		Schema:     "app",
		Table:      "child",
		Columns:    []string{"a", "b"},
		RefSchema:  "app",
		RefTable:   "parent",
		RefColumns: []string{"a", "b"},
	}
}

func newHelper(t *testing.T, deps helpers.FKForwardDeps) *helpers.FKForwardHelper {
	t.Helper()
	return helpers.NewFKForwardHelper(deps)
}

// --- Tests ---------------------------------------------------------------

func TestFKForward_SimpleFK_OpensTabWithParameterisedSQL(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 0, id: 7,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id", "total"},
		rows: map[int][]any{0: {int64(1), int64(42), 99.95}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, Limit: 500,
	})

	if err := h.Jump(context.Background(), tab, 0, 1); err != nil {
		t.Fatalf("Jump: %v", err)
	}

	wantSQL := `SELECT * FROM "app"."users" WHERE "id"=$1 LIMIT 500`
	if runner.gotQuery.SQL != wantSQL {
		t.Errorf("SQL = %q, want %q", runner.gotQuery.SQL, wantSQL)
	}
	if len(runner.gotQuery.Args) != 1 || runner.gotQuery.Args[0] != int64(42) {
		t.Errorf("Args = %+v, want [42]", runner.gotQuery.Args)
	}
	if tabs.openCall != 1 {
		t.Errorf("OpenResultTab calls = %d, want 1", tabs.openCall)
	}
	wantLabel := "→ users(42)"
	if tabs.label != wantLabel {
		t.Errorf("label = %q, want %q", tabs.label, wantLabel)
	}
	if len(jumps.pushed) != 1 {
		t.Fatalf("jump entries = %d, want 1", len(jumps.pushed))
	}
	je := jumps.pushed[0]
	if je.TabSlot != 0 || je.TabID != "7" || je.Row != 0 || je.Col != 1 {
		t.Errorf("JumpEntry = %+v, want slot=0 id=7 row=0 col=1", je)
	}
}

func TestFKForward_CompositeFK_BindsAllColumns(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{compositeFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 1, id: 9,
		schema: "app", table: "child",
		cols: []string{"a", "b", "extra"},
		rows: map[int][]any{0: {int32(10), int32(20), "x"}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	// Cursor on "b" (col index 1) — both columns must be bound.
	if err := h.Jump(context.Background(), tab, 0, 1); err != nil {
		t.Fatalf("Jump: %v", err)
	}

	if !strings.Contains(runner.gotQuery.SQL, `"a"=$1`) ||
		!strings.Contains(runner.gotQuery.SQL, `"b"=$2`) {
		t.Errorf("SQL = %q; want both composite columns bound", runner.gotQuery.SQL)
	}
	if len(runner.gotQuery.Args) != 2 {
		t.Errorf("Args len = %d, want 2", len(runner.gotQuery.Args))
	}
	if tabs.label != "→ parent(a=10, b=20)" {
		t.Errorf("label = %q, want \"→ parent(a=10, b=20)\"", tabs.label)
	}
}

func TestFKForward_CompositeMissingColumns_DisablesWithReason(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{compositeFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	// Result projection has "a" but NOT "b" — composite guard must fire.
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "child",
		cols: []string{"a", "extra"},
		rows: map[int][]any{0: {int32(10), "x"}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	err := h.Jump(context.Background(), tab, 0, 0)
	if !errors.Is(err, helpers.ErrCompositeMissingColumns) {
		t.Fatalf("err = %v, want ErrCompositeMissingColumns", err)
	}
	if !strings.Contains(err.Error(), "fk_child_parent") || !strings.Contains(err.Error(), "b") {
		t.Errorf("err message = %q, want it to name the FK and the missing column", err.Error())
	}
	if runner.calls != 0 || tabs.openCall != 0 || len(jumps.pushed) != 0 {
		t.Errorf("guard failed to short-circuit: runner=%d tabs=%d jumps=%d",
			runner.calls, tabs.openCall, len(jumps.pushed))
	}
}

func TestFKForward_NullCellValue_DisablesWithReason(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), nil}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	err := h.Jump(context.Background(), tab, 0, 1)
	if !errors.Is(err, helpers.ErrFKValueNull) {
		t.Fatalf("err = %v, want ErrFKValueNull", err)
	}
	if runner.calls != 0 || tabs.openCall != 0 || len(jumps.pushed) != 0 {
		t.Errorf("guard failed to short-circuit: runner=%d tabs=%d jumps=%d",
			runner.calls, tabs.openCall, len(jumps.pushed))
	}
}

func TestFKForward_RowNotLoaded_DisablesWithReason(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	// Cursor points at row 5 but only row 0 is loaded.
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), int64(2)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	err := h.Jump(context.Background(), tab, 5, 1)
	if !errors.Is(err, helpers.ErrRowNotLoaded) {
		t.Fatalf("err = %v, want ErrRowNotLoaded", err)
	}
	if runner.calls != 0 || len(jumps.pushed) != 0 {
		t.Errorf("guard failed to short-circuit: runner=%d jumps=%d", runner.calls, len(jumps.pushed))
	}
}

func TestFKForward_NoFKOnColumn_ReturnsErrFKNotFound(t *testing.T) {
	t.Parallel()
	// Cache returns FKs on "user_id" but cursor is on "total" — no match.
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id", "total"},
		rows: map[int][]any{0: {int64(1), int64(2), 99.95}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	err := h.Jump(context.Background(), tab, 0, 2)
	if !errors.Is(err, helpers.ErrFKNotFound) {
		t.Fatalf("err = %v, want ErrFKNotFound", err)
	}
}

func TestFKForward_MultipleFKsOnColumn_PicksFirstLexAndToasts(t *testing.T) {
	t.Parallel()
	// Two FKs on the same column; chosen by FK.Name lex order.
	fkA := models.ForeignKey{
		Name: "fk_a_user", Schema: "app", Table: "events",
		Columns: []string{"actor_id"}, RefSchema: "app", RefTable: "agents", RefColumns: []string{"id"},
	}
	fkB := models.ForeignKey{
		Name: "fk_b_user", Schema: "app", Table: "events",
		Columns: []string{"actor_id"}, RefSchema: "app", RefTable: "humans", RefColumns: []string{"id"},
	}
	cache := &fakeCache{fks: []models.ForeignKey{fkB, fkA}} // unsorted
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	toast := &fakeToast{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "events",
		cols: []string{"id", "actor_id"},
		rows: map[int][]any{0: {int64(1), int64(99)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, Toast: toast,
	})

	if err := h.Jump(context.Background(), tab, 0, 1); err != nil {
		t.Fatalf("Jump: %v", err)
	}
	// Lex-first = fk_a_user → agents.
	if !strings.Contains(tabs.label, "agents(") {
		t.Errorf("label = %q, want it to reference agents (lex-first FK)", tabs.label)
	}
	foundToast := false
	for _, m := range toast.messages {
		if strings.Contains(m, "multiple FKs") && strings.Contains(m, "fk_a_user") {
			foundToast = true
			break
		}
	}
	if !foundToast {
		t.Errorf("toast.messages = %+v, want one mentioning multiple FKs + fk_a_user", toast.messages)
	}
}

func TestFKForward_TabCapReached_ToastsAndPropagates(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{err: ui.ErrTabCapReached}
	jumps := &fakeJumpList{}
	toast := &fakeToast{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), int64(2)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, Toast: toast,
	})

	err := h.Jump(context.Background(), tab, 0, 1)
	if !errors.Is(err, ui.ErrTabCapReached) {
		t.Fatalf("err = %v, want errors.Is ErrTabCapReached", err)
	}
	foundToast := false
	for _, m := range toast.messages {
		if strings.Contains(m, "unpin a tab") {
			foundToast = true
			break
		}
	}
	if !foundToast {
		t.Errorf("toast.messages = %+v, want one mentioning 'unpin a tab'", toast.messages)
	}
}

// TestFKForward_BusyChecker_DoesNotEmitQueuedToast asserts last-wins:
// even when the session reports busy, Jump preempts
// the parked stream rather than queueing, so the misleading
// "queued behind active stream" toast is NEVER emitted.
func TestFKForward_BusyChecker_DoesNotEmitQueuedToast(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	toast := &fakeToast{}
	busy := &fakeBusy{busy: true}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), int64(2)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, Toast: toast, Busy: busy,
	})

	if err := h.Jump(context.Background(), tab, 0, 1); err != nil {
		t.Fatalf("Jump: %v", err)
	}
	for _, m := range toast.messages {
		if strings.Contains(m, "queued behind") {
			t.Errorf("toast.messages = %+v, want NO message mentioning 'queued behind' (last-wins preempts, not queues)", toast.messages)
		}
	}
	// The jump entry must still be pushed for <C-o> back-nav.
	if len(jumps.pushed) != 1 {
		t.Errorf("jumps.pushed = %d, want 1 (back-nav entry must survive)", len(jumps.pushed))
	}
}

func TestFKForward_DefaultLimitWhenZero(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{simpleFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), int64(2)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs, // Limit unset → default
	})

	if err := h.Jump(context.Background(), tab, 0, 1); err != nil {
		t.Fatalf("Jump: %v", err)
	}
	if !strings.HasSuffix(runner.gotQuery.SQL, "LIMIT 1000") {
		t.Errorf("SQL = %q, want it to end with LIMIT 1000 (DefaultFKForwardLimit)", runner.gotQuery.SQL)
	}
}

func TestFKForward_CacheError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("driver boom")
	cache := &fakeCache{err: sentinel}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "orders",
		cols: []string{"id", "user_id"},
		rows: map[int][]any{0: {int64(1), int64(2)}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	err := h.Jump(context.Background(), tab, 0, 1)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want errors.Is sentinel", err)
	}
	if runner.calls != 0 || len(jumps.pushed) != 0 {
		t.Errorf("guard failed: runner=%d jumps=%d", runner.calls, len(jumps.pushed))
	}
}

func TestFKForward_LabelTruncation_LongCompositeValues(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{fks: []models.ForeignKey{compositeFK()}}
	runner := &fakeRunner{}
	tabs := &fakeTabs{}
	jumps := &fakeJumpList{}
	bigVal := strings.Repeat("x", 200)
	tab := &fakeTab{
		slot: 0, id: 1,
		schema: "app", table: "child",
		cols: []string{"a", "b"},
		rows: map[int][]any{0: {bigVal, bigVal}},
	}
	h := newHelper(t, helpers.FKForwardDeps{
		Cache: cache, JumpList: jumps, Runner: runner, Tabs: tabs,
	})

	if err := h.Jump(context.Background(), tab, 0, 0); err != nil {
		t.Fatalf("Jump: %v", err)
	}
	if len(tabs.label) > 40 {
		t.Errorf("label length = %d, want <= 40 (truncated). label=%q", len(tabs.label), tabs.label)
	}
}
