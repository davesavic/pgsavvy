package controllers

import (
	"fmt"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// PromptController owns the PROMPT TEMPORARY_POPUP line buffer and its
// keybindings (dbsavvy-m47.1). The buffer is a []rune; printable ASCII
// runes append, <bs> pops the last rune, <cr> calls helper.Submit with
// string(buf), and <esc> calls helper.Cancel. The controller subscribes
// to helper resets via PromptHelper.SetResetHandler so every new
// helper.Prompt(label, initial, ...) re-seeds the buffer with the new
// initial value.
//
// Concurrency: every handler runs on the gocui MainLoop per D8, but
// the helper's reset callback fires from inside helper.Prompt which a
// data-helper goroutine may invoke. The mutex defends the buffer
// against that fan-in.
type PromptController struct {
	baseController

	mu  sync.Mutex
	buf []rune
}

// NewPromptController constructs the controller and (if helpers.Prompt
// is non-nil) registers itself as the reset handler so future
// helper.Prompt invocations re-seed the buffer.
func NewPromptController(c *common.Common, helpers HelperBag) *PromptController {
	p := &PromptController{baseController: newBase(c, helpers)}
	if helpers.Prompt != nil {
		helpers.Prompt.SetResetHandler(p.Reset)
	}
	return p
}

// Reset seeds the buffer with initial's runes. Invoked by the helper
// from inside Prompt(label, initial, ...) BEFORE the popup is pushed.
func (p *PromptController) Reset(initial string) {
	p.mu.Lock()
	p.buf = []rune(initial)
	p.mu.Unlock()
}

// Buffer returns a copy of the current line buffer as a string. Test
// accessor; production code reads the buffer through Submit.
func (p *PromptController) Buffer() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.buf)
}

// Submit hands the current buffer to helper.Submit, clears the buffer
// and propagates the helper's error (matches MenuController shape).
func (p *PromptController) Submit(_ commands.ExecCtx) error {
	if p.helpers.Prompt == nil {
		return nil
	}
	p.mu.Lock()
	value := string(p.buf)
	p.buf = nil
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
	p.buf = nil
	p.mu.Unlock()
	return p.wrapErr("prompt.cancel", p.helpers.Prompt.Cancel())
}

// Backspace pops the last rune. No-op (and no callback) when the
// buffer is already empty per the acceptance criteria.
func (p *PromptController) Backspace(_ commands.ExecCtx) error {
	p.mu.Lock()
	if n := len(p.buf); n > 0 {
		p.buf = p.buf[:n-1]
	}
	p.mu.Unlock()
	return nil
}

// InsertRune appends r to the buffer. Public so unit tests can exercise
// non-ASCII runes (the v1 binding catch-all covers ASCII 0x20..0x7E
// only — non-ASCII paths reach the buffer through this method directly
// once a future epic wires a wider dispatch surface).
func (p *PromptController) InsertRune(r rune) error {
	p.mu.Lock()
	p.buf = append(p.buf, r)
	p.mu.Unlock()
	return nil
}

// GetKeybindings returns the PROMPT-scope bindings: <cr>, <esc>, <bs>,
// and one binding per printable ASCII rune (0x20..0x7E inclusive).
func (p *PromptController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := p.tr()
	out := make([]*types.ChordBinding, 0, 3+0x7F-0x20)
	out = append(out,
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       types.PROMPT,
			ActionID:    commands.PromptSubmit,
			Description: tr.Actions.Confirm,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       types.PROMPT,
			ActionID:    commands.PromptCancel,
			Description: tr.Actions.Cancel,
		},
		&types.ChordBinding{
			Sequence:    []types.ChordKey{{Special: types.KeyBs}},
			Mode:        types.ModeNormal,
			Scope:       types.PROMPT,
			ActionID:    commands.PromptBackspace,
			Description: tr.Actions.Cancel, // closest in-scope label; cosmetic only.
		},
	)
	for r := rune(0x20); r <= rune(0x7E); r++ {
		out = append(out, &types.ChordBinding{
			Sequence:    []types.ChordKey{{Code: r}},
			Mode:        types.ModeNormal,
			Scope:       types.PROMPT,
			ActionID:    runeActionID(r),
			Description: "",
		})
	}
	return out
}

// RegisterActions registers the submit / cancel / backspace handlers
// plus one per-rune insert closure with reg.
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
	_ = reg.Register(&commands.Command{
		ID:          commands.PromptBackspace,
		Description: "Erase last character",
		Handler:     p.Backspace,
	})
	for r := rune(0x20); r <= rune(0x7E); r++ {
		r := r // capture per-iteration
		_ = reg.Register(&commands.Command{
			ID:          runeActionID(r),
			Description: "Insert character",
			Handler: func(_ commands.ExecCtx) error {
				return p.InsertRune(r)
			},
		})
	}
}

// AttachToContext registers GetKeybindings on the PROMPT context.
func (p *PromptController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(p.GetKeybindings)
}

// runeActionID returns the stable action ID for the printable rune r.
// IDs use lowercase hex so they remain dot-namespaced and unique
// without colliding with the surrounding "prompt.*" namespace.
func runeActionID(r rune) string {
	return fmt.Sprintf("prompt.rune.%02x", r)
}
