package orchestrator

import (
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/status"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// AppStatusViewName is the gocui view-name string the status bar
// renderer targets. The value MUST match the boxlayout slot key
// returned by ui.GetWindowDimensions ("status") — RunLayout's Tier-4
// status pass uses dims[AppStatusViewName] to size the view and then
// hands the same name to driver.SetView before calling RenderStatusLine.
// Keeping the constant and the layout key in lock-step means the
// renderer's SetContent target and the layout's SetView target can never
// drift.
const AppStatusViewName = "status"

// ResultTabBarViewName is the gocui view-name for the result-pane tab-bar
// strip — a frameless 1-row view carved out of the top of the "secondary"
// (result) region. It is created/sized directly in RunLayout (not a
// boxlayout slot) and its content comes from ResultTabsHelper.RenderTabBar.
const ResultTabBarViewName = "result_tab_bar"

// ResultEmptyViewName is the gocui view-name for the always-visible empty
// state of the result pane. It sits behind any result_tab_<slot> views so
// tab views occlude it via SetViewOnTop; when no tabs are open the empty
// view shows through, keeping the layout stable.
const ResultEmptyViewName = "result_empty"

// ANSI SGR sequences used to give success / error toasts a
// distinguishable foreground style at the cell-content level. gocui's
// escape interpreter (escape.go in the vendored lazygit fork) parses
// these inline and lifts them to per-cell Attribute values, so the
// recorder driver's stored buffer carries the style as plain bytes —
// which is exactly the observable the AC asserts.
//
// SafeText is applied to the user-supplied toast message BEFORE the
// ANSI wrapper is prepended; SafeText would otherwise strip the \x1b
// byte itself and defeat the styling.
const (
	ansiResetSGR   = "\x1b[0m"
	ansiGreenFgSGR = "\x1b[32m" // success / info toast foreground
	ansiRedFgSGR   = "\x1b[31m" // error toast foreground
)

// ToastSource is the narrow accessor RenderStatusLine needs from the
// toast helper. *ui.ToastHelper satisfies it structurally; tests may
// inject a fake. nil disables the toast multiplex (status bar reverts
// to its default-line behaviour).
type ToastSource interface {
	Current() string
	CurrentLevel() ui.ToastLevel
}

// StatusRenderDeps bundles every collaborator RenderStatusLine needs.
// Pulled into its own struct so the orchestrator can construct it once
// at wireWithDriver time and reuse the value for every render pass.
type StatusRenderDeps struct {
	Driver     types.GuiDriver
	Tree       *gui.ContextTree
	KbRuntime  *keys.Runtime
	ActiveConn func() *models.Connection
	Tr         *i18n.TranslationSet
	// Toast surfaces a transient message that, while non-empty,
	// overrides the default status line for its TTL window. Nil falls
	// back to default-line rendering on every pass.
	Toast ToastSource
	// BusyCount returns the live OnWorker in-flight counter for the
	// status-bar spinner segment. Nil → no spinner
	// rendered, which is the correct fallback for partial test wiring.
	BusyCount func() int64
	// SpinnerFrame returns the wall-clock frame index that selects the
	// spinner glyph (U8). Advanced by the periodic re-render ticker while
	// busy>0 so a single worker still animates. Nil → frame 0 (the glyph
	// stays static, the pre-U8 single-worker behaviour).
	SpinnerFrame func() int64
	// TxStatus returns the active transaction's lifecycle status and
	// savepoint names. Nil → no transaction indicator rendered (bootstrap
	// safety / no session connected yet).
	TxStatus func() (models.TxStatus, []string)
	// SessionSettings returns the live session settings snapshot
	// (search_path, role, etc.) for display in the status bar. Nil →
	// no settings section rendered (bootstrap safety / no session).
	SessionSettings func() map[string]string
	// SearchStatus reports the active-search state for the focused
	// result tab's grid. It MUST read the live active
	// grid at call time (every frame) — not a captured pointer — so a
	// tab switch reflects the new tab's count and clears when focus
	// leaves a result tab. Returns active=false when focus is not a
	// result tab, no tab is active, or no search is live. Nil → no
	// search segment rendered (bootstrap safety / partial test wiring).
	SearchStatus func() (query string, cur, total int, active bool)
	// PendingCount returns the total staged-edit count aggregated across
	// every per-(connID, baseTable) set in the registry. Drives the
	// "[N pending]" status-bar indicator. Nil or 0 → no segment rendered.
	PendingCount func() int
}

// pendingIndicatorBudget is the width handed to the pending indicator.
// Large enough to always render the expanded "[N pending]" form; the
// status bar handles overall line wrapping, so the indicator does not
// need to self-collapse here.
const pendingIndicatorBudget = 1 << 20

// RenderStatusLine resolves the focused context's mode label, builds the
// status line via status.BuildStatusLine, and writes it to the
// AppStatus view through the driver.
//
// Toast multiplex: when d.Toast is non-nil AND d.Toast.Current() is
// non-empty (i.e. an unexpired toast exists), the toast message takes
// over the AppStatusViewName cells for the TTL window. The raw message
// is run through config.SafeText to strip control bytes (the toast may
// surface a runtime error string that isn't config-sourced), then
// wrapped with an ANSI SGR pair keyed to the toast level so success
// (green) and error (red) toasts are visually distinct at the cell
// level. When the toast expires (Current() returns ""), the next pass
// reverts to default status with no artifact.
//
// The options slot is populated by CollectOptionsForScope using the
// focused (mode, scope) pair from the focus tree plus the live
// TrieSet snapshot held by the Matcher; an empty result is rendered
// as no options (BuildStatusLine still appends the "?: more" hint).
//
// Skips silently when (a) the driver is nil, (b) the KbRuntime or its
// ModeStore is nil (defensive bootstrap-order guard), or (c) the
// focus tree is nil/empty. Any driver SetContent
// error is swallowed — the status bar is non-critical UI. The view is
// materialised by RunLayout's Tier-4 status pass each frame before this
// function is invoked, so SetContent normally finds its target buffer;
// the error-swallow is purely defense-in-depth for bootstrap races.
func RenderStatusLine(d StatusRenderDeps) {
	if d.Driver == nil {
		return
	}

	// Toast multiplex — checked first so a toast can paint even before
	// the keybinding runtime / focus tree are fully wired (e.g. on
	// reload failures very early in bootstrap).
	if d.Toast != nil {
		if msg := d.Toast.Current(); msg != "" {
			sanitized := config.SafeText(msg)
			content := styleToastForLevel(sanitized, d.Toast.CurrentLevel())
			_ = d.Driver.SetContent(AppStatusViewName, content)
			return
		}
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
		// Always show the mode label, regardless of whether
		// the focused context is editable. The status bar's mode banner
		// is part of the always-on baseline (QA 1.1 / 3.1 / 5.1) so the
		// user can see at a glance which mode keystrokes will dispatch
		// against. Passing forceShowNormal=true keeps the "-- NORMAL --"
		// label visible on side rails too.
		label = status.LabelForMode(mode, d.Tr, true)

		var trieSet *keys.TrieSet
		if d.KbRuntime.Matcher != nil {
			trieSet = d.KbRuntime.Matcher.TrieSet()
		}

		type optionsBarFilterer interface {
			OptionsBarFilter() func(string) bool
		}
		var actionFilter func(string) bool
		if f, ok := focused.(optionsBarFilterer); ok {
			actionFilter = f.OptionsBarFilter()
		}

		options = CollectOptionsForScope(trieSet, mode, key, d.Tr, actionFilter)
	}

	var conn *models.Connection
	if d.ActiveConn != nil {
		conn = d.ActiveConn()
	}

	var busy int64
	if d.BusyCount != nil {
		busy = d.BusyCount()
	}
	var frame int64
	if d.SpinnerFrame != nil {
		frame = d.SpinnerFrame()
	}
	var txSt models.TxStatus
	var txSp []string
	if d.TxStatus != nil {
		txSt, txSp = d.TxStatus()
	}
	var sessSettings map[string]string
	if d.SessionSettings != nil {
		sessSettings = d.SessionSettings()
	}
	// Active-search segment: read the focused result tab's grid live each
	// frame (provider must not capture a *grid.View) so a tab switch or
	// search clear is reflected on the next pass. Appended as a status
	// option so it sits alongside the other line-2 sections; absent when
	// the provider reports active=false.
	if d.SearchStatus != nil {
		if seg := status.SearchIndicator(d.SearchStatus()); seg != "" {
			options = append(options, seg)
		}
	}
	// Pending-edit indicator: aggregate count across every table so a
	// staged edit on any tab is visible from anywhere, not just the tab
	// it was made on.
	if d.PendingCount != nil {
		if seg := status.BuildPendingIndicatorCount(d.PendingCount(), conn, pendingIndicatorBudget); seg != "" {
			options = append(options, seg)
		}
	}
	line := status.BuildStatusLine(label, conn, options, d.Tr, busy, frame, txSt, txSp, sessSettings)
	_ = d.Driver.SetContent(AppStatusViewName, line)
}

// styleToastForLevel wraps msg with the ANSI SGR pair appropriate to
// level. msg is assumed to already be SafeText-sanitised so it carries
// no control bytes other than the ones this wrapper adds.
func styleToastForLevel(msg string, level ui.ToastLevel) string {
	switch level {
	case ui.ToastError:
		return ansiRedFgSGR + msg + ansiResetSGR
	default:
		return ansiGreenFgSGR + msg + ansiResetSGR
	}
}
