package controllers

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// PromptController owns the PROMPT TEMPORARY_POPUP submit / cancel
// wiring.
//
// The line buffer is no longer owned here: the PROMPT
// view is editable and the master gocui.Editor's Passthrough branch
// delegates printable runes / Backspace / Delete / Left / Right /
// Home / End / bracketed-paste to gocui.DefaultEditor — which writes
// directly into v.TextArea. The controller's only remaining roles
// are:
//
//   - Translate the PROMPT-scope <cr> binding into a helper.Submit
//     call carrying the typed value read from the view.
//   - Translate <esc> into helper.Cancel.
//   - Re-seed the view's TextArea (via the bufferStore) with the
//     supplied initial value when helper.Prompt fires the reset hook —
//     this is invoked before the popup is pushed so the layout pass's
//     fresh-view branch finds the seeded TextArea and skips re-typing
//     the initial.
//
// Concurrency: per D8 every method runs on the gocui MainLoop. The
// mutex defends the test-mode buf against the same forward-compat
// surface ConfirmHelper calls out.
type PromptController struct {
	baseController

	mu     sync.Mutex
	buf    string
	reader PromptBufferReader
}

// PromptBufferReader is the read-and-clear surface PromptController
// reads on submit. *guicontext.PromptContext satisfies it; tests can
// inject a fake. Nil-safe: a nil reader falls back to the internal
// test-mode buf.
type PromptBufferReader interface {
	ReadAndClearBuffer() string
	SetBuffer(string)
}

// NewPromptController constructs the controller. If helpers.Prompt is
// non-nil it registers itself as the reset handler so future
// helper.Prompt invocations re-seed the view buffer.
func NewPromptController(c *common.Common, core CoreDeps, ui UIDeps) *PromptController {
	p := &PromptController{baseController: newBase(c, HelperBag{CoreDeps: core, UIDeps: ui})}
	if ui.Prompt != nil {
		ui.Prompt.SetResetHandler(p.Reset)
	}
	return p
}

// SetBufferReader wires the read-and-clear surface the production
// runtime backs with PromptContext (which reads from v.TextArea). Nil
// is treated as "use the test-mode buf" — keeps existing unit tests
// (which don't wire a real view) source-compatible.
func (p *PromptController) SetBufferReader(r PromptBufferReader) {
	p.mu.Lock()
	p.reader = r
	p.mu.Unlock()
}

// Reset seeds the view buffer with initial's content. Invoked by the
// helper from inside Prompt(label, initial, ...) BEFORE the popup is
// pushed.
func (p *PromptController) Reset(initial string) {
	p.mu.Lock()
	p.buf = initial
	if p.reader != nil {
		p.reader.SetBuffer(initial)
	}
	p.mu.Unlock()
}

// Buffer returns the current line buffer. When a reader is wired
// (production path) it reflects the view's TextArea; otherwise it
// returns the test-mode buf.
func (p *PromptController) Buffer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reader != nil {
		// Best-effort: many readers don't expose a non-destructive
		// peek. Production *PromptContext.Buffer() is non-destructive;
		// fall back to test-mode buf when no Buffer() peek is available.
		if peek, ok := p.reader.(interface{ Buffer() string }); ok {
			return peek.Buffer()
		}
	}
	return p.buf
}

// Submit hands the current buffer to helper.Submit, clears the buffer
// and propagates the helper's error (matches MenuController shape).
func (p *PromptController) Submit(_ commands.ExecCtx) error {
	if p.helpers.Prompt == nil {
		return nil
	}
	p.mu.Lock()
	var value string
	if p.reader != nil {
		value = p.reader.ReadAndClearBuffer()
		p.buf = ""
	} else {
		value = p.buf
		p.buf = ""
	}
	p.mu.Unlock()
	return p.wrapErr("prompt.submit", p.helpers.Prompt.Submit(value))
}

// Cancel calls helper.Cancel, clears the buffer and propagates the
// helper's error.
func (p *PromptController) Cancel(_ commands.ExecCtx) error {
	if p.helpers.Prompt == nil {
		return nil
	}
	p.mu.Lock()
	if p.reader != nil {
		_ = p.reader.ReadAndClearBuffer()
	}
	p.buf = ""
	p.mu.Unlock()
	return p.wrapErr("prompt.cancel", p.helpers.Prompt.Cancel())
}

// GetKeybindings returns the PROMPT-scope bindings: <cr> and <esc>.
// Printable runes / Backspace / arrow keys / paste all flow through
// the master Editor's Passthrough branch into gocui.DefaultEditor,
// which writes them into v.TextArea — so per-key shims for those keys
// are intentionally absent (gocui rejects char-key keybindings on
// editable views via matchView).
func (p *PromptController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := p.tr()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeCommand,
			Scope:       types.PROMPT,
			ActionID:    commands.PromptSubmit,
			Description: tr.Actions.Confirm,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeCommand,
			Scope:       types.PROMPT,
			ActionID:    commands.PromptCancel,
			Description: tr.Actions.Cancel,
		},
	}
}

// RegisterActions registers the submit / cancel handlers with reg.
func (p *PromptController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.PromptSubmit,
		Description: "Submit prompt",
		Handler:     p.Submit,
	})
	_ = reg.Register(&commands.Command{
		ID:          commands.PromptCancel,
		Description: "Cancel prompt",
		Handler:     p.Cancel,
	})
}

// AttachToContext registers GetKeybindings on the PROMPT context.
func (p *PromptController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(p.GetKeybindings)
}
