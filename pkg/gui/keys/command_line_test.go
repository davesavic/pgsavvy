package keys

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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
func (f *fakeHolder) GetTitle() string                                          { return "" }
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

// --- CaretToggler wiring (dbsavvy-tro.2) -------------------------------

// caretRecorder accumulates every CaretToggler call. The order matters
// in the Cancel/Submit assertions (caret must flip false AFTER Pop).
type caretRecorder struct{ log []bool }

func (c *caretRecorder) toggle(enabled bool) { c.log = append(c.log, enabled) }

func TestCommandOpenCommand_EnablesCaret(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{}
	caret := &caretRecorder{}
	cmd := CommandOpenCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, CaretToggler: caret.toggle,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !reflect.DeepEqual(caret.log, []bool{true}) {
		t.Errorf("caret log = %v, want [true]", caret.log)
	}
}

func TestCommandOpenCommand_PushErrorSkipsCaret(t *testing.T) {
	// If Push fails, we must NOT enable the caret — otherwise a
	// failed-to-open command line would still leave the global caret on,
	// painting at whatever view is current.
	stack := &fakeStack{pushErr: errors.New("push failed")}
	holder := &fakeHolder{}
	caret := &caretRecorder{}
	cmd := CommandOpenCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, CaretToggler: caret.toggle,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err == nil {
		t.Fatal("Handler: want push error, got nil")
	}
	if len(caret.log) != 0 {
		t.Errorf("caret log = %v, want empty (push failed)", caret.log)
	}
}

func TestCommandCancelCommand_DisablesCaretAfterPop(t *testing.T) {
	stack := &fakeStack{}
	caret := &caretRecorder{}
	cmd := CommandCancelCommand(CommandLineCommandDeps{
		Stack: stack, CaretToggler: caret.toggle,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if !reflect.DeepEqual(caret.log, []bool{false}) {
		t.Errorf("caret log = %v, want [false]", caret.log)
	}
}

func TestCommandSubmitCommand_DisablesCaretAfterPop(t *testing.T) {
	stack := &fakeStack{}
	holder := &fakeHolder{buf: ""}
	caret := &caretRecorder{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: NewExRegistry(), CaretToggler: caret.toggle,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if stack.popped != 1 {
		t.Errorf("popped = %d, want 1", stack.popped)
	}
	if !reflect.DeepEqual(caret.log, []bool{false}) {
		t.Errorf("caret log = %v, want [false]", caret.log)
	}
}

func TestCommandSubmitCommand_DisablesCaretEvenOnHandlerError(t *testing.T) {
	// Submit's defer must run on the error path too: a failing ex-command
	// handler must not leave the caret enabled.
	stack := &fakeStack{}
	holder := &fakeHolder{buf: "broken"}
	reg := NewExRegistry()
	_ = reg.Register(ExCommand{
		Name: "broken",
		Handler: func(_ []string, _ commands.ExecCtx) error {
			return errors.New("boom")
		},
	})
	caret := &caretRecorder{}
	cmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: reg,
		Toaster: func(string) {}, CaretToggler: caret.toggle,
	})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if !reflect.DeepEqual(caret.log, []bool{false}) {
		t.Errorf("caret log = %v, want [false]", caret.log)
	}
}

func TestCommandOpenCommand_NilCaretTogglerNoCrash(t *testing.T) {
	// CaretToggler is optional. Tests/bootstrap that don't supply one
	// must still operate normally — the handlers nil-check before call.
	stack := &fakeStack{}
	holder := &fakeHolder{}
	cmd := CommandOpenCommand(CommandLineCommandDeps{Stack: stack, Context: holder})
	if err := cmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Open Handler: %v", err)
	}
	cancelCmd := CommandCancelCommand(CommandLineCommandDeps{Stack: stack})
	if err := cancelCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Cancel Handler: %v", err)
	}
	submitCmd := CommandSubmitCommand(CommandLineCommandDeps{
		Stack: stack, Context: holder, ExRegistry: NewExRegistry(),
	})
	if err := submitCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("Submit Handler: %v", err)
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

// --- dbsavvy-tro.6 integration: Backspace through the default bindings -
//
// These tests pin the contract from the COMMAND_LINE feature's
// perspective: with the real DefaultCommandLineBindings (colon/esc/cr)
// installed in a Matcher, non-printable editor keys must NOT be
// shadowed by those bindings and must reach the master editor's
// Passthrough → DefaultEditor delegation.
//
// We assert at the Matcher boundary (Passthrough result) rather than
// at the TextArea level because the keys package cannot import gocui
// without inverting the dependency direction; the orchestrator package
// owns the matching TextArea assertion (master_editor_test.go's
// TestMasterEditor_PassthroughInsertModeDelegates already covers the
// printable variant of that wire).

// buildCommandLineMatcher constructs a Matcher pre-loaded with the
// three default COMMAND_LINE bindings (colon/esc/cr) and the supplied
// scope/mode. We assemble the trie inline rather than calling Build —
// Build also resolves command IDs to handlers (out-of-scope for a
// gate test) and registering action handlers would muddy the test.
func buildCommandLineMatcher(t *testing.T, scope types.ContextKey, mode types.Mode) *Matcher {
	t.Helper()
	bindings := DefaultCommandLineBindings()
	builders := map[TrieSetKey]*TrieBuilder{}
	for _, b := range bindings {
		k := TrieSetKey{Mode: b.Mode, Scope: b.Scope}
		tb, ok := builders[k]
		if !ok {
			tb = NewTrieBuilder()
			builders[k] = tb
		}
		// Stub Command — handler is irrelevant because the test only
		// drives keys for which we expect Passthrough, not Dispatched.
		tb.InsertDefault(b, &commands.Command{
			ID:      b.ActionID,
			Handler: func(commands.ExecCtx) error { return nil },
		})
	}
	ts := NewTrieSet()
	for k, tb := range builders {
		trie, err := tb.Build()
		if err != nil {
			t.Fatalf("build trie for %+v: %v", k, err)
		}
		ts.Set(k.Mode, k.Scope, trie)
	}

	store := NewModeStore()
	store.Set(scope, mode)
	m, err := NewMatcher(ts, MatcherConfig{
		Modes:       store,
		TimeoutLen:  30 * time.Millisecond,
		TtimeoutLen: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	return m
}

// TestCommandLine_BackspaceReachesPassthrough is the load-bearing
// end-to-end assertion for the COMMAND_LINE feature: with the real
// default bindings wired, Backspace in ModeCommand reaches the
// matcher's Passthrough gate (and from there, in production,
// gocui.DefaultEditor.BackSpaceChar mutates v.TextArea). If a future
// refactor moves the gate or accidentally registers a <bs> binding,
// this test fails — Backspace would either return Dispatched (binding
// added by mistake) or FellThrough (gate narrowed).
func TestCommandLine_BackspaceReachesPassthrough(t *testing.T) {
	m := buildCommandLineMatcher(t, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, Key{Special: KeyBs})
	if err != nil {
		t.Fatalf("Dispatch <bs>: %v", err)
	}
	if res != Passthrough {
		t.Errorf("res = %v, want Passthrough (Backspace must reach DefaultEditor)", res)
	}
}

// TestCommandLine_PrintableTypingReachesPassthrough confirms the
// :abc typing path still resolves to Passthrough through the same
// matcher (no regression on the existing happy-path).
func TestCommandLine_PrintableTypingReachesPassthrough(t *testing.T) {
	m := buildCommandLineMatcher(t, types.COMMAND_LINE, types.ModeCommand)

	for _, r := range "abc" {
		res, err := m.Dispatch(types.COMMAND_LINE, Key{Code: r})
		if err != nil {
			t.Fatalf("Dispatch %q: %v", string(r), err)
		}
		if res != Passthrough {
			t.Errorf("%q in ModeCommand: res = %v, want Passthrough", string(r), res)
		}
	}
}

// TestCommandLine_EscStillCancels guards that the gate change has
// NOT swallowed the <esc> binding — Esc must still resolve to
// Dispatched (the CommandCancel handler) because esc is wired into
// the default trie.
func TestCommandLine_EscStillCancels(t *testing.T) {
	m := buildCommandLineMatcher(t, types.COMMAND_LINE, types.ModeCommand)

	res, err := m.Dispatch(types.COMMAND_LINE, Key{Special: KeyEsc})
	if err != nil {
		t.Fatalf("Dispatch <esc>: %v", err)
	}
	if res != Dispatched {
		t.Errorf("<esc> res = %v, want Dispatched", res)
	}
}

// TestCommandLine_DeleteAndArrowsReachPassthrough mirrors the
// Backspace assertion across the rest of the editor-safe Special
// set, anchored to the real default bindings. A future "we forgot
// arrow X" regression on the gate is caught here without bloating
// matcher_test.go.
func TestCommandLine_DeleteAndArrowsReachPassthrough(t *testing.T) {
	cases := []struct {
		name string
		sp   SpecialKey
	}{
		{"Delete", KeyDel},
		{"Left", KeyLeft},
		{"Right", KeyRight},
		{"Home", KeyHome},
		{"End", KeyEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildCommandLineMatcher(t, types.COMMAND_LINE, types.ModeCommand)
			res, err := m.Dispatch(types.COMMAND_LINE, Key{Special: tc.sp})
			if err != nil {
				t.Fatalf("Dispatch %s: %v", tc.name, err)
			}
			if res != Passthrough {
				t.Errorf("%s: res = %v, want Passthrough", tc.name, res)
			}
		})
	}
}
