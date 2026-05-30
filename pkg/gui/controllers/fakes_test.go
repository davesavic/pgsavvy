package controllers_test

import (
	"context"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeConnectInvoker records Connect calls and the ctx each received so
// the U1 timeout AC (dbsavvy-fow.1) can assert a non-zero Deadline.
type fakeConnectInvoker struct {
	calls    []*models.Connection
	ctxs     []context.Context
	err      error
	deadline time.Time
	hasDL    bool
}

func (f *fakeConnectInvoker) Connect(ctx context.Context, profile *models.Connection) error {
	f.calls = append(f.calls, profile)
	f.ctxs = append(f.ctxs, ctx)
	if dl, ok := ctx.Deadline(); ok {
		f.deadline = dl
		f.hasDL = true
	}
	return f.err
}

// fakeSchemasInvoker records Hide/Unhide calls; UnhideErr lets a test
// inject ErrNeedsConfirmation.
type fakeSchemasInvoker struct {
	hideCalls   []hideArgs
	unhideCalls []unhideArgs
	hideErr     error
	unhideErr   error
}

type (
	hideArgs   struct{ ConnID, Name string }
	unhideArgs struct {
		ConnID, Name     string
		Builtin, Profile []string
	}
)

func (f *fakeSchemasInvoker) HideSchema(connID, name string) error {
	f.hideCalls = append(f.hideCalls, hideArgs{connID, name})
	return f.hideErr
}

func (f *fakeSchemasInvoker) UnhideSchema(connID, name string, builtin, profile []string) error {
	f.unhideCalls = append(f.unhideCalls, unhideArgs{connID, name, builtin, profile})
	return f.unhideErr
}

type fakeConnectionForm struct {
	called bool
	err    error
}

func (f *fakeConnectionForm) WalkAdd(_ context.Context) error {
	f.called = true
	return f.err
}

type fakeConfirm struct {
	calls []confirmCall
	err   error
	yes   int
	no    int
}

type confirmCall struct {
	Title, Body string
	OnYes       func() error
	OnNo        func() error
}

func (f *fakeConfirm) Confirm(title, body string, onYes, onNo func() error) error {
	f.calls = append(f.calls, confirmCall{title, body, onYes, onNo})
	return f.err
}

func (f *fakeConfirm) Yes() error { f.yes++; return nil }
func (f *fakeConfirm) No() error  { f.no++; return nil }

type (
	fakeToast struct {
		msgs    []toastMsg
		updates []toastUpdate
	}
	toastMsg struct {
		Msg string
		TTL time.Duration
	}
	toastUpdate struct {
		Key string
		Msg string
		TTL time.Duration
	}
)

func (f *fakeToast) Show(msg string, ttl time.Duration) {
	f.msgs = append(f.msgs, toastMsg{msg, ttl})
}

// ShowOrUpdate records keyed toast calls (dbsavvy-fow.1). The Connect
// path emits a "connecting" toast then clears/replaces it under the same
// key; tests inspect updates to assert that sequence.
func (f *fakeToast) ShowOrUpdate(key, msg string, ttl time.Duration) {
	f.updates = append(f.updates, toastUpdate{key, msg, ttl})
}

type fakeTip struct{ dismissed int }

func (f *fakeTip) DismissStartupTip() error {
	f.dismissed++
	return nil
}

type fakeTableDouble struct {
	calls []*models.Table
	err   error
}

func (f *fakeTableDouble) DoubleClickStub(t *models.Table) error {
	f.calls = append(f.calls, t)
	return f.err
}

// fakeRefresh records RefreshXxx calls so per-rail `r` binding tests
// can assert dispatch. dbsavvy-56u.1.
type fakeRefresh struct {
	schemas int
	tables  []string
	columns []refreshTC
	indexes []refreshTC
}

type refreshTC struct{ Schema, Table string }

func (f *fakeRefresh) RefreshSchemas(_ context.Context) error {
	f.schemas++
	return nil
}

func (f *fakeRefresh) RefreshTables(_ context.Context, schema string) error {
	f.tables = append(f.tables, schema)
	return nil
}

func (f *fakeRefresh) RefreshColumns(_ context.Context, schema, table string) error {
	f.columns = append(f.columns, refreshTC{schema, table})
	return nil
}

func (f *fakeRefresh) RefreshIndexes(_ context.Context, schema, table string) error {
	f.indexes = append(f.indexes, refreshTC{schema, table})
	return nil
}

type fakeMenuPush struct {
	pushed int
	popped int
}

func (f *fakeMenuPush) PushMenu() error { f.pushed++; return nil }
func (f *fakeMenuPush) PopMenu() error  { f.popped++; return nil }

// Pickers.

type fakeSchemaPicker struct {
	name        string
	toggleCount int
}

func (f *fakeSchemaPicker) SelectedSchemaName() string { return f.name }
func (f *fakeSchemaPicker) ToggleShowHidden()          { f.toggleCount++ }

type fakeTablePicker struct{ sel *models.Table }

func (f *fakeTablePicker) SelectedTable() *models.Table { return f.sel }

type fakeActiveConnection struct{ id string }

func (f *fakeActiveConnection) ActiveConnectionID() string { return f.id }

// recordingLogger captures Debug messages.
type recordingLogger struct{ msgs []string }

func (r *recordingLogger) Debug(msg string, _ ...any) {
	r.msgs = append(r.msgs, msg)
}

// newBag returns a HelperBag with every fake pre-wired and addressable.
type bag struct {
	HelperBag    controllers.HelperBag
	Connect      *fakeConnectInvoker
	Schemas      *fakeSchemasInvoker
	ConnForm     *fakeConnectionForm
	Confirm      *fakeConfirm
	Toast        *fakeToast
	Tip          *fakeTip
	TableDouble  *fakeTableDouble
	Menu         *fakeMenuPush
	SchemaPicker *fakeSchemaPicker
	TablePicker  *fakeTablePicker
	Active       *fakeActiveConnection
	Logger       *recordingLogger

	// WorkerCalls counts OnWorker dispatches. The wired closure runs its
	// fn inline so the connect path executes synchronously in tests
	// (dbsavvy-fow.1).
	WorkerCalls int
}

func newBag() *bag {
	b := &bag{
		Connect:      &fakeConnectInvoker{},
		Schemas:      &fakeSchemasInvoker{},
		ConnForm:     &fakeConnectionForm{},
		Confirm:      &fakeConfirm{},
		Toast:        &fakeToast{},
		Tip:          &fakeTip{},
		TableDouble:  &fakeTableDouble{},
		Menu:         &fakeMenuPush{},
		SchemaPicker: &fakeSchemaPicker{},
		TablePicker:  &fakeTablePicker{},
		Active:       &fakeActiveConnection{},
		Logger:       &recordingLogger{},
	}
	b.HelperBag = controllers.HelperBag{
		CoreDeps: controllers.CoreDeps{
			Logger: b.Logger,
		},
		NavDeps: controllers.NavDeps{
			Connect:          b.Connect,
			SchemasHelper:    b.Schemas,
			ConnectionForm:   b.ConnForm,
			Schemas:          b.SchemaPicker,
			Tables:           b.TablePicker,
			ActiveConnection: b.Active,
			HiddenPatterns:   func() ([]string, []string) { return []string{"pg_*"}, []string{"audit"} },
		},
		UIDeps: controllers.UIDeps{
			Confirm:     b.Confirm,
			Toast:       b.Toast,
			Tip:         b.Tip,
			TableDouble: b.TableDouble,
			Menu:        b.Menu,
		},
		ThreadingDeps: controllers.ThreadingDeps{
			OnWorker: func(fn func(gocui.Task) error) {
				b.WorkerCalls++
				if fn != nil {
					_ = fn(nil)
				}
			},
		},
	}
	return b
}

// fakeCursor satisfies controllers.SideListCursor.
type fakeCursor struct {
	idx   int
	items []any
}

func (f *fakeCursor) Cursor() int     { return f.idx }
func (f *fakeCursor) SetCursor(i int) { f.idx = i }
func (f *fakeCursor) Items() []any    { return f.items }

// isRune reports whether b is a single-keystroke ChordBinding whose
// first key is the bare rune r (no modifiers, no SpecialKey).
func isRune(b *types.ChordBinding, r rune) bool {
	if b == nil || len(b.Sequence) != 1 {
		return false
	}
	k := b.Sequence[0]
	return k.Special == types.KeyNone && k.Mod == 0 && k.Code == r
}

// isSpecial reports whether b is a single-keystroke ChordBinding whose
// first key is the given SpecialKey (no modifiers, no rune).
func isSpecial(b *types.ChordBinding, sp types.SpecialKey) bool {
	if b == nil || len(b.Sequence) != 1 {
		return false
	}
	k := b.Sequence[0]
	return k.Special == sp && k.Mod == 0 && k.Code == 0
}

// invokeAction resolves b.ActionID against reg and invokes the
// registered handler with a zero ExecCtx. Returns an error if no
// handler is registered for the binding's ActionID.
func invokeAction(reg *commands.Registry, b *types.ChordBinding) error {
	if reg == nil || b == nil {
		return nil
	}
	cmd, ok := reg.Get(b.ActionID)
	if !ok || cmd == nil || cmd.Handler == nil {
		return nil
	}
	return cmd.Handler(commands.ExecCtx{})
}
