package controllers_test

import (
	"context"
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// fakeConnectInvoker records Connect calls.
type fakeConnectInvoker struct {
	calls []*models.Connection
	err   error
}

func (f *fakeConnectInvoker) Connect(_ context.Context, profile *models.Connection) error {
	f.calls = append(f.calls, profile)
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

type (
	fakeToast struct{ msgs []toastMsg }
	toastMsg  struct {
		Msg string
		TTL time.Duration
	}
)

func (f *fakeToast) Show(msg string, ttl time.Duration) {
	f.msgs = append(f.msgs, toastMsg{msg, ttl})
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

type fakeMenuPush struct {
	pushed int
	popped int
}

func (f *fakeMenuPush) PushMenu() error { f.pushed++; return nil }
func (f *fakeMenuPush) PopMenu() error  { f.popped++; return nil }

// Pickers.
type fakeConnectionPicker struct{ sel *models.Connection }

func (f *fakeConnectionPicker) SelectedConnection() *models.Connection { return f.sel }

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
	ConnPicker   *fakeConnectionPicker
	SchemaPicker *fakeSchemaPicker
	TablePicker  *fakeTablePicker
	Active       *fakeActiveConnection
	Logger       *recordingLogger
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
		ConnPicker:   &fakeConnectionPicker{},
		SchemaPicker: &fakeSchemaPicker{},
		TablePicker:  &fakeTablePicker{},
		Active:       &fakeActiveConnection{},
		Logger:       &recordingLogger{},
	}
	b.HelperBag = controllers.HelperBag{
		Logger:           b.Logger,
		Connect:          b.Connect,
		SchemasHelper:    b.Schemas,
		ConnectionForm:   b.ConnForm,
		Confirm:          b.Confirm,
		Toast:            b.Toast,
		Tip:              b.Tip,
		TableDouble:      b.TableDouble,
		Menu:             b.Menu,
		Connections:      b.ConnPicker,
		Schemas:          b.SchemaPicker,
		Tables:           b.TablePicker,
		ActiveConnection: b.Active,
		HiddenPatterns:   func() ([]string, []string) { return []string{"pg_*"}, []string{"audit"} },
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

// buildRegistryFor returns a fresh Registry with reg.RegisterActions
// invoked on the supplied bundle. Used by tests that want to dispatch
// a binding's ActionID through the live handler.
func buildRegistryFor(actions ...func(*commands.Registry)) *commands.Registry {
	reg := commands.NewRegistry()
	for _, a := range actions {
		if a != nil {
			a(reg)
		}
	}
	return reg
}
