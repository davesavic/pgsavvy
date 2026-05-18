package orchestrator

import (
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/status"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// AppStatusViewName is the gocui view-name string the status bar
// renderer targets. The view itself is allocated by the layout manager
// in a later epic; until then SetContent on a missing view is a no-op
// at the renderer level (the driver surfaces the error, the renderer
// swallows it — the status bar is non-critical UI).
const AppStatusViewName = "app_status"

// StatusRenderDeps bundles every collaborator RenderStatusLine needs.
// Pulled into its own struct so the orchestrator can construct it once
// at wireWithDriver time and reuse the value for every render pass.
type StatusRenderDeps struct {
	Driver     types.GuiDriver
	Tree       *gui.ContextTree
	KbRuntime  *keys.Runtime
	ActiveConn func() *models.Connection
	Tr         *i18n.TranslationSet
}

// RenderStatusLine resolves the focused context's mode label, builds the
// status line via status.BuildStatusLine, and writes it to the
// AppStatus view through the driver.
//
// The options slot is populated by CollectOptionsForScope using the
// focused (mode, scope) pair from the focus tree plus the live
// TrieSet snapshot held by the Matcher; an empty result is rendered
// as no options (BuildStatusLine still appends the "?: more" hint).
//
// Skips silently when (a) the driver is nil, (b) the KbRuntime or its
// ModeStore is nil (defensive bootstrap-order guard per dlp.9 review
// notes), or (c) the focus tree is nil/empty. Any driver SetContent
// error is swallowed — the status bar is non-critical UI and the view
// may not be allocated yet during early bootstrap.
func RenderStatusLine(d StatusRenderDeps) {
	if d.Driver == nil {
		return
	}
	if d.KbRuntime == nil || d.KbRuntime.ModeStore == nil {
		return
	}
	if d.Tree == nil {
		return
	}

	focused := d.Tree.Current()
	var (
		label   string
		options []string
	)
	if focused != nil {
		key := focused.GetKey()
		mode := d.KbRuntime.ModeStore.Get(key)
		label = status.LabelForMode(mode, d.Tr, key.IsEditable())

		var trieSet *keys.TrieSet
		if d.KbRuntime.Matcher != nil {
			trieSet = d.KbRuntime.Matcher.TrieSet()
		}
		options = CollectOptionsForScope(trieSet, mode, key, d.Tr)
	}

	var conn *models.Connection
	if d.ActiveConn != nil {
		conn = d.ActiveConn()
	}

	line := status.BuildStatusLine(label, conn, options, d.Tr)
	_ = d.Driver.SetContent(AppStatusViewName, line)
}
