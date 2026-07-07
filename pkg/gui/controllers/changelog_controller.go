package controllers

import (
	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

const (
	changelogScrollDown = "changelog.scroll_down"
	changelogScrollUp   = "changelog.scroll_up"
	changelogDismiss    = "changelog.dism"
)

type ChangelogController struct {
	baseController

	ctx  *guicontext.ChangelogContext
	tree FocusPopper

	onDismiss func()
}

func NewChangelogController(
	c *common.Common,
	core CoreDeps,
	ctx *guicontext.ChangelogContext,
	tree FocusPopper,
) *ChangelogController {
	return &ChangelogController{
		baseController: newBase(c, HelperBag{CoreDeps: core}),
		ctx:            ctx,
		tree:           tree,
	}
}

func (ch *ChangelogController) SetOnDismiss(fn func()) { ch.onDismiss = fn }

func (ch *ChangelogController) scrollDown(_ commands.ExecCtx) error {
	if ch.ctx == nil || !ch.ctx.Active() {
		return nil
	}
	ch.ctx.Scroll(1)
	return nil
}

func (ch *ChangelogController) scrollUp(_ commands.ExecCtx) error {
	if ch.ctx == nil || !ch.ctx.Active() {
		return nil
	}
	ch.ctx.Scroll(-1)
	return nil
}

func (ch *ChangelogController) dismiss(_ commands.ExecCtx) error {
	if ch.ctx == nil || !ch.ctx.Active() {
		return nil
	}
	ch.ctx.Close()
	if ch.onDismiss != nil {
		ch.onDismiss()
	}
	if ch.tree != nil {
		return ch.wrapErr("changelog.dismiss", ch.tree.Pop())
	}
	return nil
}

func (ch *ChangelogController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := ch.tr()
	scope := guicontext.ChangelogKey()
	return []*types.ChordBinding{
		{
			Sequence:    []types.ChordKey{{Code: 'j'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    changelogScrollDown,
			Description: tr.Actions.ChangelogScrollDown,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'k'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    changelogScrollUp,
			Description: tr.Actions.ChangelogScrollUp,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEsc}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    changelogDismiss,
			Description: tr.Actions.ChangelogDismiss,
		},
		{
			Sequence:    []types.ChordKey{{Special: types.KeyEnter}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    changelogDismiss,
			Description: tr.Actions.ChangelogDismiss,
		},
		{
			Sequence:    []types.ChordKey{{Code: 'q'}},
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    changelogDismiss,
			Description: tr.Actions.ChangelogDismiss,
		},
	}
}

func (ch *ChangelogController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	tr := ch.tr()
	_ = reg.Register(&commands.Command{
		ID:          changelogScrollDown,
		Description: tr.Actions.ChangelogScrollDown,
		Tag:         "",
		Handler:     ch.scrollDown,
	})
	_ = reg.Register(&commands.Command{
		ID:          changelogScrollUp,
		Description: tr.Actions.ChangelogScrollUp,
		Tag:         "",
		Handler:     ch.scrollUp,
	})
	_ = reg.Register(&commands.Command{
		ID:          changelogDismiss,
		Description: tr.Actions.ChangelogDismiss,
		Tag:         "",
		Handler:     ch.dismiss,
	})
}

func (ch *ChangelogController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(ch.GetKeybindings)
}
