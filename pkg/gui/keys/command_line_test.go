package keys

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// --- Fakes -------------------------------------------------------------

type fakeStack struct {
	pushed []types.IBaseContext
	popped int
	pushErr error
	popErr  error
}

func (f *fakeStack) Push(c types.IBaseContext) error {
	f.pushed = append(f.pushed, c)
	return f.pushErr
}
func (f *fakeStack) Pop() error {
	f.popped++
	return f.popErr
}

// fakeHolder satisfies CommandLineHolder. Only ReadAndClearBuffer is
// load-bearing; the IBaseContext methods are stubbed to no-ops so the
// fake is independent of BaseContext.
type fakeHolder struct {
	buf string
}

func (f *fakeHolder) ReadAndClearBuffer() string {
	s := f.buf
	f.buf = ""
	return s
}
func (f *fakeHolder) GetKey() types.ContextKey                                  { return types.COMMAND_LINE }
func (f *fakeHolder) GetViewName() string                                       { return string(types.COMMAND_LINE) }
func (f *fakeHolder) GetWindowName() string                                     { return string(types.COMMAND_LINE) }
func (f *fakeHolder) GetKind() types.ContextKind                                { return types.TEMPORARY_POPUP }
func (f *fakeHolder) HandleFocus(types.OnFocusOpts) error                       { return nil }
func (f *fakeHolder) HandleFocusLost(types.OnFocusLostOpts) error               { return nil }
func (f *fakeHolder) HandleRender() error                                       { return nil }
func (f *fakeHolder) HandleRenderToMain() error                                 { return nil }
func (f *fakeHolder) HandleQuit() error                                         { return nil }
func (f *fakeHolder) NeedsRerenderOnHeightChange() bool                         { return false }
func (f *fakeHolder) NeedsRerenderOnWidthChange() bool                          { return false }
func (f *fakeHolder) AddKeybindingsFn(types.KeybindingsFn)                      {}
func (f *fakeHolder) GetKeybindings(types.KeybindingsOpts) []*types.ChordBinding {
	return nil
}
func (f *fakeHolder) GetMouseKeybindings(types.KeybindingsOpts) []types.MouseBinding {
	return nil
}

type cmdLineToaster struct {
	messages []string
}

func (c *cmdLineToaster) toast(m string) { c.messages = append(c.messages, m) }

// --- CommandOpenCommand ------------------------------------------------

func TestCommandOpenCommand_PushesContext(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{}
	cmd := CommandOpenCommand(CommandLineCommandDeps{Stack: stack, Context: holder})
	if cmd.ID != commands.CommandOpen {
		t.Errorf("ID = %q, want %q", cmd.ID, commands.CommandOpen)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(stack.pushed) != 1 || stack.pushed[0] != holder {
		t.Errorf("pushed = %v, want [holder]", stack.pushed)
	}
}

func TestCommandOpenCommand_NilDepsNoOp(t *testing.T) {
	cmd := CommandOpenCommand(CommandLineCommandDeps{})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Errorf("Handler with nil deps: %v", err)
	}
}

// --- CommandCancelCommand ----------------------------------------------

func TestCommandCancelCommand_PopsStack(t *testing.T) {
	stack := &fakeStack{}
	cmd := CommandCancelCommand(CommandLineCommandDeps{Stack: stack})
	if cmd.ID != commands.CommandCancel {
		t.Errorf("ID = %q, want %q", cmd.ID, commands.CommandCancel)
	}
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
}

func TestCommandCancelCommand_NilStackNoOp(t *testing.T) {
	cmd := CommandCancelCommand(CommandLineCommandDeps{})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Errorf("Handler: %v", err)
	}
}

// --- CommandSubmitCommand ----------------------------------------------

func TestCommandSubmitCommand_EmptyBufferPopsSilently(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: ""}
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: NewExRegistry(), Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 0 {
		t.Errorf("toaster messages = %v, want none", toaster.messages)
	}
}

func TestCommandSubmitCommand_WhitespaceBufferPopsSilently(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "   \t  "}
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: NewExRegistry(), Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 0 {
		t.Errorf("toaster messages = %v, want none", toaster.messages)
	}
}

func TestCommandSubmitCommand_KnownCommandNoArgs(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "reload"}
	reg := NewExRegistry()
	var receivedArgs []string
	receivedCtx := commands.ExecCtx{}
	_ = reg.Register(ExCommand{
		Name: "reload",
		Handler: func(args []string, ctx commands.ExecCtx) error {
			receivedArgs = append([]string(nil), args...)
			receivedCtx = ctx
			return nil
		},
	})
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: reg, Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{Mode: types.ModeCommand, Scope: types.COMMAND_LINE}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if len(receivedArgs) != 0 {
		t.Errorf("args = %v, want []", receivedArgs)
	}
	if receivedCtx.Mode != types.ModeCommand || receivedCtx.Scope != types.COMMAND_LINE {
		t.Errorf("ExecCtx not propagated: %+v", receivedCtx)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 0 {
		t.Errorf("unexpected toasts on success: %v", toaster.messages)
	}
}

func TestCommandSubmitCommand_KnownCommandWithArgs(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "reload foo bar"}
	reg := NewExRegistry()
	var receivedArgs []string
	_ = reg.Register(ExCommand{
		Name: "reload",
		Handler: func(args []string, _ commands.ExecCtx) error {
			receivedArgs = append([]string(nil), args...)
			return nil
		},
	})
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: reg, Toaster: func(string) {},
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !reflect.DeepEqual(receivedArgs, []string{"foo", "bar"}) {
		t.Errorf("args = %v, want [foo bar]", receivedArgs)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
}

func TestCommandSubmitCommand_UnknownCommandToasts(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "bogus arg"}
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: NewExRegistry(), Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 1 || !strings.Contains(toaster.messages[0], "unknown ex-command") || !strings.Contains(toaster.messages[0], "bogus") {
		t.Errorf("toaster messages = %v, want one 'unknown ex-command: bogus' entry", toaster.messages)
	}
}

func TestCommandSubmitCommand_HandlerErrorToasts(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "reload"}
	reg := NewExRegistry()
	_ = reg.Register(ExCommand{
		Name: "reload",
		Handler: func(_ []string, _ commands.ExecCtx) error {
			return errors.New("disk on fire")
		},
	})
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: reg, Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 1 || toaster.messages[0] != "disk on fire" {
		t.Errorf("toaster messages = %v, want [disk on fire]", toaster.messages)
	}
}

func TestCommandSubmitCommand_NilRegistryUnknown(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "anything"}
	toaster := &cmdLineToaster{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, Toaster: toaster.toast,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if len(toaster.messages) != 1 || !strings.Contains(toaster.messages[0], "unknown ex-command") {
		t.Errorf("toaster messages = %v, want unknown-ex-command entry", toaster.messages)
	}
}

// --- DefaultCommandLineBindings ----------------------------------------

func TestDefaultCommandLineBindings(t *testing.T) {
	bs := DefaultCommandLineBindings()
	if len(bs) != 3 {
		t.Fatalf("DefaultCommandLineBindings len = %d, want 3", len(bs))
	}
	// Index 0: ':' (open) — Mode=Normal, Scope=all, Action=command.open.
	if bs[0].ActionID != commands.CommandOpen {
		t.Errorf("bs[0].ActionID = %q, want %q", bs[0].ActionID, commands.CommandOpen)
	}
	if bs[0].Mode != types.ModeNormal {
		t.Errorf("bs[0].Mode = %v, want ModeNormal", bs[0].Mode)
	}
	if bs[0].Scope != "all" {
		t.Errorf("bs[0].Scope = %q, want \"all\"", bs[0].Scope)
	}
	if bs[0].Source != ShippedDefault {
		t.Errorf("bs[0].Source = %v, want ShippedDefault", bs[0].Source)
	}
	if len(bs[0].Sequence) == 0 {
		t.Error("bs[0].Sequence empty")
	}
	// Index 1: <esc> (cancel) — Mode=Command, Scope=COMMAND_LINE.
	if bs[1].ActionID != commands.CommandCancel {
		t.Errorf("bs[1].ActionID = %q, want %q", bs[1].ActionID, commands.CommandCancel)
	}
	if bs[1].Mode != types.ModeCommand {
		t.Errorf("bs[1].Mode = %v, want ModeCommand", bs[1].Mode)
	}
	if bs[1].Scope != types.COMMAND_LINE {
		t.Errorf("bs[1].Scope = %q, want COMMAND_LINE", bs[1].Scope)
	}
	if len(bs[1].Sequence) == 0 {
		t.Error("bs[1].Sequence empty")
	}
	// Index 2: <cr> (submit) — Mode=Command, Scope=COMMAND_LINE.
	if bs[2].ActionID != commands.CommandSubmit {
		t.Errorf("bs[2].ActionID = %q, want %q", bs[2].ActionID, commands.CommandSubmit)
	}
	if bs[2].Mode != types.ModeCommand {
		t.Errorf("bs[2].Mode = %v, want ModeCommand", bs[2].Mode)
	}
	if bs[2].Scope != types.COMMAND_LINE {
		t.Errorf("bs[2].Scope = %q, want COMMAND_LINE", bs[2].Scope)
	}
	if len(bs[2].Sequence) == 0 {
		t.Error("bs[2].Sequence empty")
	}
}
