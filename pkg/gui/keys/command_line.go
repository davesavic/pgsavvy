package keys

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// CommandLineHolder is the surface command.open / command.submit need
// from the COMMAND_LINE context. Implemented by
// *context.CommandLineContext; the interface lives here so the keys
// package does not import pkg/gui/context (which would be circular —
// context already depends on types, not keys).
type CommandLineHolder interface {
	types.IBaseContext
	ReadAndClearBuffer() string
}

// StackOps is the minimal focus-stack surface command.open /
// command.cancel need. Implemented by *gui.ContextTree.Push / Pop.
type StackOps interface {
	Push(c types.IBaseContext) error
	Pop() error
}

// CaretToggler flips the global terminal caret on/off. Bootstrap wires
// this to driver.SetCaretEnabled; tests pass a recording func. Nil is
// treated as a no-op so existing call sites that don't care about the
// caret keep compiling.
type CaretToggler func(enabled bool)

// CommandLineCommandDeps groups the dependencies for command.open,
// command.cancel, and command.submit. Bootstrap (dlp.8c) supplies the
// concrete focus stack, the live CommandLineContext, the ExRegistry,
// the toast surface, and the caret toggler.
type CommandLineCommandDeps struct {
	Stack        StackOps
	Context      CommandLineHolder
	ExRegistry   *ExRegistry
	Toaster      ToastFunc
	CaretToggler CaretToggler
}

// CommandOpenCommand builds the `command.open` Command. Handler pushes
// the COMMAND_LINE context onto the focus stack; HandleFocus on the
// context sets ModeStore[COMMAND_LINE] = ModeCommand.
func CommandOpenCommand(deps CommandLineCommandDeps) *commands.Command {
	return &commands.Command{
		ID:          commands.CommandOpen,
		Description: "Open command line",
		Handler: func(_ commands.ExecCtx) error {
			if deps.Stack == nil || deps.Context == nil {
				return nil
			}
			if err := deps.Stack.Push(deps.Context); err != nil {
				return err
			}
			if deps.CaretToggler != nil {
				deps.CaretToggler(true)
			}
			return nil
		},
	}
}

// CommandCancelCommand builds the `command.cancel` Command. Handler pops
// the COMMAND_LINE context. ModeStore is reset via HandleFocusLost on
// the context's pop. The caret is disabled AFTER Pop so the focus has
// already moved off COMMAND_LINE — gocui draws the caret at the current
// view's cursor, so leaving caret=true pointed at a just-deleted view
// would briefly draw at (0,0) of whatever rail is now current.
func CommandCancelCommand(deps CommandLineCommandDeps) *commands.Command {
	return &commands.Command{
		ID:          commands.CommandCancel,
		Description: "Close command line",
		Handler: func(_ commands.ExecCtx) error {
			if deps.Stack == nil {
				return nil
			}
			if err := deps.Stack.Pop(); err != nil {
				return err
			}
			if deps.CaretToggler != nil {
				deps.CaretToggler(false)
			}
			return nil
		},
	}
}

// CommandSubmitCommand builds the `command.submit` Command. Handler reads
// the typed buffer, splits on whitespace, looks the first token up in
// ExRegistry, and invokes it. Empty/whitespace-only buffer → silent
// pop. Unknown command → toast + pop. The context is always popped
// after submit (success, error, or empty) so a half-typed line never
// outlives one <cr>.
func CommandSubmitCommand(deps CommandLineCommandDeps) *commands.Command {
	return &commands.Command{
		ID:          commands.CommandSubmit,
		Description: "Submit command line",
		Handler: func(ctx commands.ExecCtx) error {
			if deps.Context == nil || deps.Stack == nil {
				return nil
			}
			line := strings.TrimSpace(deps.Context.ReadAndClearBuffer())
			// Pop always — empty, unknown, success: same exit path.
			// CaretToggler runs after Pop for the same reason as Cancel:
			// caret must follow focus off COMMAND_LINE before disabling.
			defer func() {
				_ = deps.Stack.Pop()
				if deps.CaretToggler != nil {
					deps.CaretToggler(false)
				}
			}()
			if line == "" {
				return nil
			}
			tokens := strings.Fields(line)
			name := tokens[0]
			args := tokens[1:]
			if deps.ExRegistry == nil {
				if deps.Toaster != nil {
					deps.Toaster(fmt.Sprintf("unknown ex-command: %s", name))
				}
				return nil
			}
			cmd, ok := deps.ExRegistry.Get(name)
			if !ok {
				if deps.Toaster != nil {
					deps.Toaster(fmt.Sprintf("unknown ex-command: %s", name))
				}
				return nil
			}
			if err := cmd.Handler(args, ctx); err != nil {
				// gocui.ErrQuit must escape the submit handler so the
				// main loop unwinds; toasting it would swallow the quit.
				if errors.Is(err, gocui.ErrQuit) {
					return err
				}
				if deps.Toaster != nil {
					deps.Toaster(err.Error())
				}
			}
			return nil
		},
	}
}

// DefaultCommandLineBindings returns the three default ChordBindings
// that wire the COMMAND_LINE feature: `:` opens, `<esc>` cancels,
// `<cr>` submits.
//
// `:` is registered in Normal mode at `scope: all` so every non-popup
// context plus GLOBAL receives it. `<esc>` and `<cr>` are scoped to
// COMMAND_LINE under ModeCommand so they don't shadow other-context
// bindings.
func DefaultCommandLineBindings() []*ChordBinding {
	colonSeq, _ := SequenceFromShorthand(":")
	escSeq, _ := SequenceFromShorthand("<esc>")
	crSeq, _ := SequenceFromShorthand("<cr>")
	return []*ChordBinding{
		{
			Sequence:    colonSeq,
			Mode:        types.ModeNormal,
			Scope:       "all",
			ActionID:    commands.CommandOpen,
			Description: "Open command line",
			Source:      ShippedDefault,
			Origin:      "pkg/gui/keys/command_line.go",
		},
		{
			Sequence:    escSeq,
			Mode:        types.ModeCommand,
			Scope:       types.COMMAND_LINE,
			ActionID:    commands.CommandCancel,
			Description: "Close command line",
			Source:      ShippedDefault,
			Origin:      "pkg/gui/keys/command_line.go",
		},
		{
			Sequence:    crSeq,
			Mode:        types.ModeCommand,
			Scope:       types.COMMAND_LINE,
			ActionID:    commands.CommandSubmit,
			Description: "Submit command line",
			Source:      ShippedDefault,
			Origin:      "pkg/gui/keys/command_line.go",
		},
	}
}
