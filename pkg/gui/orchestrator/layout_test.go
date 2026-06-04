package orchestrator_test

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/editor"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestRunLayoutGatesLimitOverlay(t *testing.T) {
	g, rec := buildTestGui(t)
	// Below threshold → only the LIMIT view is laid out.
	if err := g.RunLayout(5, 5); err != nil {
		t.Fatalf("RunLayout small: %v", err)
	}
	if !rec.HasSetView(string(types.LIMIT)) {
		t.Fatal("expected LIMIT view to be created at 5x5")
	}
	for _, name := range []string{
		string(types.SCHEMAS),
		string(types.TABLES),
	} {
		if rec.HasSetView(name) {
			t.Errorf("did not expect SetView(%q) on tiny terminal", name)
		}
	}
}

func TestRunLayoutSkipsStubContexts(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout large: %v", err)
	}
	// QUERY_EDITOR is no longer in this list — promoted to a live
	// MAIN_CONTEXT (dbsavvy-wwd.1) and tiled into dims["main"] every
	// frame by Tier 1.4 (dbsavvy-9p3). See
	// TestRunLayoutCreatesQueryEditorMainPane.
	for _, name := range []string{
		string(types.TABLE_DATA_EDITOR),
		string(types.RESULT_GRID),
		string(types.PLAN),
		string(types.WHICH_KEY),
		string(types.HISTORY),
	} {
		if rec.HasSetView(name) {
			t.Errorf("stub context %q must not be laid out", name)
		}
	}
}

// TestRunLayoutCreatesQueryEditorMainPane (dbsavvy-9p3): the QUERY_EDITOR
// is a live MAIN_CONTEXT and must be SetView'd into dims["main"] every
// frame, regardless of focus-stack membership — focus only governs
// FrameColor and SetCurrentView, not whether the pane exists.
// Pop the CONNECTION_MANAGER first to simulate post-connect state;
// the modal suppresses the query editor while active.
func TestRunLayoutCreatesQueryEditorMainPane(t *testing.T) {
	g, rec := buildTestGui(t)
	_ = g.ContextTree().Push(g.Registry().Schemas)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if !rec.HasSetView(string(types.QUERY_EDITOR)) {
		t.Fatal("QUERY_EDITOR SetView not invoked; main pane would be invisible")
	}
}

// TestRunLayoutEnablesCaretOnQueryEditorFocus regresses the "cursor
// invisible in query panel" bug. gocui's flush only calls
// Screen.ShowCursor when g.Cursor (toggled via SetCaretEnabled) is true,
// so even though syncViewToBuffer / Tier 1.4 position the view cursor,
// no caret is drawn unless the layout enables it when QUERY_EDITOR is
// focused. PROMPT and COMMAND_LINE manage their own caret via helpers
// (they're TEMPORARY_POPUPs on top of the tiled stack); only the tiled
// QUERY_EDITOR needs the layout-level toggle.
func TestRunLayoutEnablesCaretOnQueryEditorFocus(t *testing.T) {
	g, rec := buildTestGui(t)
	qec := g.Registry().QueryEditor
	if qec == nil {
		t.Fatal("registry.QueryEditor is nil")
	}
	if err := g.ContextTree().Push(qec); err != nil {
		t.Fatalf("Push(queryEditor): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if !rec.CaretEnabled {
		t.Fatalf("CaretEnabled = false after focusing QUERY_EDITOR; caret would not render. log=%v", rec.AllCaretEnabledLog())
	}
}

// TestRunLayoutDisablesCaretOnSideRailFocus locks the inverse: when a
// SIDE_CONTEXT (here CONNECTIONS, the bootstrap top) is focused, the
// layout must keep the gocui caret off so a stale enabled state from a
// prior QUERY_EDITOR frame doesn't bleed a cursor onto the rail list.
func TestRunLayoutDisablesCaretOnSideRailFocus(t *testing.T) {
	g, rec := buildTestGui(t)
	// dbsavvy-56u.2: pop the first-run tip pushed at bootstrap so the
	// assertion measures CONNECTIONS focus, not the popup on top of it.
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop first-run tip: %v", err)
	}
	// Pre-stain the caret state so the assertion catches "layout never
	// touched it" as a failure too.
	rec.SetCaretEnabled(true)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if rec.CaretEnabled {
		t.Fatalf("CaretEnabled = true with CONNECTIONS focused; layout must clear caret on side-rail focus. log=%v", rec.AllCaretEnabledLog())
	}
}

// TestRunLayoutDisablesCaretOnConfirmationPopup regresses the "cursor
// at the top of the confirmation dialog" bug (dbsavvy-u6p7). The confirm
// popup is a non-editable TEMPORARY_POPUP that opens over the focused
// QUERY_EDITOR, which leaves g.Cursor enabled from the prior frame.
// Unless the layout actively clears the caret when a non-editable popup
// is on top, gocui draws a stale terminal cursor at the popup's (0,0).
func TestRunLayoutDisablesCaretOnConfirmationPopup(t *testing.T) {
	g, rec := buildTestGui(t)
	confirm := g.Registry().Confirmation
	if confirm == nil {
		t.Fatal("registry.Confirmation is nil")
	}
	if err := g.ContextTree().Push(confirm); err != nil {
		t.Fatalf("Push(confirmation): %v", err)
	}
	// Pre-stain the caret as if the QUERY_EDITOR beneath had enabled it.
	rec.SetCaretEnabled(true)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if rec.CaretEnabled {
		t.Fatalf("CaretEnabled = true with CONFIRMATION on top; layout must clear the caret for a non-editable popup. log=%v", rec.AllCaretEnabledLog())
	}
}

func TestRunLayoutCreatesSideRails(t *testing.T) {
	g, rec := buildTestGui(t)
	// Pop CONNECTION_MANAGER first so the layout pass paints the side rails.
	_ = g.ContextTree().Push(g.Registry().Schemas)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.SCHEMAS),
		string(types.TABLES),
	} {
		if !rec.HasSetView(name) {
			t.Errorf("side rail %q not laid out at 120x40", name)
		}
	}
}

// TestRunLayoutNoQueuedWriteErrors regresses the startup crash where
// LimitContext.HandleRender ran in the normal-size flatten pass and
// queued a Write to the "limit" view, which the layout never created.
// Real gocui surfaces that ErrUnknownView out of the MainLoop and kills
// the TUI on the first frame; the RecorderGuiDriver previously dropped
// the error silently, so this assertion locks the behaviour in.
func TestRunLayoutNoQueuedWriteErrors(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if errs := rec.UpdateErrors(); len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("queued Update closure returned error: %v", e)
		}
	}
}

// TestRunLayoutOmitsOffStackPopups asserts the per-Kind dispatch
// contract: popup contexts (MENU/CONFIRMATION/PROMPT/SUGGESTIONS/
// COMMAND_LINE/CHEATSHEET) must NOT have SetView called when they are
// absent from the focus stack — otherwise empty popup rectangles
// occlude the screen under gocui.SupportOverlaps=false.
func TestRunLayoutOmitsOffStackPopups(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.MENU),
		string(types.CONFIRMATION),
		string(types.PROMPT),
		string(types.SUGGESTIONS),
		string(types.COMMAND_LINE),
		string(types.CHEATSHEET),
	} {
		if rec.HasSetView(name) {
			t.Errorf("popup %q must not be laid out when off the focus stack", name)
		}
	}
}

// lastSetView returns the most recent SetView call for name (dbsavvy-etp.2
// helper). The Tier-3 popup loop SetViews each frame, so the last call
// reflects the rect actually applied.
func lastSetView(rec *testfake.RecorderGuiDriver, name string) (testfake.SetViewCall, bool) {
	var got testfake.SetViewCall
	found := false
	for _, c := range rec.AllSetViewCalls() {
		if c.Name == name {
			got, found = c, true
		}
	}
	return got, found
}

// TestRunLayoutSuggestionsAnchoredBelowCursor (dbsavvy-etp.2): when the
// SUGGESTIONS popup is on the focus stack and visible, RunLayout sizes its
// view to the cell directly below the cursor — derived from the live
// QUERY_EDITOR view Dimensions()+Origin() and the SuggestionsContext
// anchor — not screen-center.
func TestRunLayoutSuggestionsAnchoredBelowCursor(t *testing.T) {
	g, rec := buildTestGui(t)
	// Install a real editor view so ViewByName returns a handle with a
	// known origin (5,3)-(105,33), scrolled oy=4.
	ev := gocui.NewView(string(types.QUERY_EDITOR), 5, 3, 105, 33, gocui.OutputNormal)
	ev.SetOrigin(0, 4)
	rec.SetRealView(string(types.QUERY_EDITOR), ev)

	sugg := g.Registry().Suggestions
	if sugg == nil {
		t.Fatal("registry.Suggestions is nil")
	}
	sugg.Show([]editor.Suggestion{{Display: "users"}, {Display: "user_roles"}}, editor.Position{Line: 9, Col: 7})
	if err := g.ContextTree().Push(sugg); err != nil {
		t.Fatalf("Push(suggestions): %v", err)
	}

	if err := g.RunLayout(200, 60); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	call, ok := lastSetView(rec, string(types.SUGGESTIONS))
	if !ok {
		t.Fatal("SUGGESTIONS SetView not invoked while visible on stack")
	}
	wantY0 := 3 + 1 + (9 - 4) + 1 // vy0 + 1 (frame) + (Line-oy) + 1 (below cursor)
	wantX0 := 5 + 1 + (7 - 0)     // vx0 + 1 (frame) + (Col-ox)
	if call.Y0 != wantY0 {
		t.Errorf("SUGGESTIONS Y0 = %d, want %d (row below cursor)", call.Y0, wantY0)
	}
	if call.X0 != wantX0 {
		t.Errorf("SUGGESTIONS X0 = %d, want %d (below-cursor column)", call.X0, wantX0)
	}
	// Not screen-centered (center of a 200-wide canvas would be ~100).
	if call.X0 > 60 {
		t.Errorf("SUGGESTIONS X0 = %d looks screen-centered, want anchored", call.X0)
	}
}

// TestRunLayoutSuggestionsFallsBackToCenteredWithoutEditorView
// (dbsavvy-etp.2): when the QUERY_EDITOR view handle is unavailable
// (ViewByName -> nil, the default recorder behavior), the SUGGESTIONS
// popup degrades to a centered rect rather than landing at (0,0).
func TestRunLayoutSuggestionsFallsBackToCenteredWithoutEditorView(t *testing.T) {
	g, rec := buildTestGui(t)
	sugg := g.Registry().Suggestions
	sugg.Show([]editor.Suggestion{{Display: "users"}}, editor.Position{Line: 2, Col: 3})
	if err := g.ContextTree().Push(sugg); err != nil {
		t.Fatalf("Push(suggestions): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	call, ok := lastSetView(rec, string(types.SUGGESTIONS))
	if !ok {
		t.Fatal("SUGGESTIONS SetView not invoked")
	}
	// Centered fallback: not pinned to the top-left origin.
	if call.X0 < 5 || call.Y0 < 5 {
		t.Errorf("SUGGESTIONS fallback rect (X0=%d,Y0=%d) not centered", call.X0, call.Y0)
	}
}

// TestRunLayoutRendersVisibleSuggestionsOffStack regresses the
// dbsavvy-2fo integration gap. The frozen design (dbsavvy-etp Scope IN
// #1) keeps the QUERY_EDITOR focused and NEVER pushes SUGGESTIONS onto
// the focus stack, yet the only popup-render path was the focus-stack
// Tier-3 loop. Result: in the real TUI the completion popup never
// appeared (Show() flipped an internal visible bool the orchestrator
// never consulted). RunLayout must SetView the SUGGESTIONS popup
// whenever the context IsVisible(), independent of focus-stack
// membership — exactly as it does for WHICH_KEY.
func TestRunLayoutRendersVisibleSuggestionsOffStack(t *testing.T) {
	g, rec := buildTestGui(t)
	// Real editor view so the anchored rect resolves off the live origin.
	ev := gocui.NewView(string(types.QUERY_EDITOR), 5, 3, 105, 33, gocui.OutputNormal)
	ev.SetOrigin(0, 4)
	rec.SetRealView(string(types.QUERY_EDITOR), ev)

	sugg := g.Registry().Suggestions
	if sugg == nil {
		t.Fatal("registry.Suggestions is nil")
	}
	sugg.Show([]editor.Suggestion{{Display: "users"}, {Display: "user_roles"}}, editor.Position{Line: 9, Col: 7})
	// Deliberately NOT pushed onto the focus stack — the editor keeps
	// focus per the frozen "no focus-stack push" decision. This is the
	// production state the .2 test masked by pushing.

	if err := g.RunLayout(200, 60); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if _, ok := lastSetView(rec, string(types.SUGGESTIONS)); !ok {
		t.Fatal("SUGGESTIONS SetView not invoked while visible off-stack; the popup is invisible in the real TUI")
	}
}

// TestRunLayoutOmitsInvisibleSuggestionsOffStack locks the inverse: an
// off-stack SUGGESTIONS context that is NOT visible must not be laid out
// (no empty popup rect punching a hole under SupportOverlaps=false).
func TestRunLayoutOmitsInvisibleSuggestionsOffStack(t *testing.T) {
	g, rec := buildTestGui(t)
	// Suggestions context exists but Show() was never called → invisible.
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if rec.HasSetView(string(types.SUGGESTIONS)) {
		t.Error("SUGGESTIONS must not be laid out when invisible and off-stack")
	}
}

// TestRunLayoutCheatsheetFocusedAfterPush is the lc2 / Bug C regression
// probe. Pushing the CHEATSHEET context onto the focus stack and
// running a Layout pass must yield a SetCurrentView("cheatsheet") call
// so the gocui runtime routes Esc to the binding registered for that
// view at gui.go:339. Passing this assertion means the Layout's focus
// handoff is correct; if a real-terminal Esc still fails to dismiss
// the cheatsheet, the bug is downstream of the binding registration
// (e.g., dispatch / modifier mismatch — see Bug A's modifier fix).
func TestRunLayoutCheatsheetFocusedAfterPush(t *testing.T) {
	g, rec := buildTestGui(t)
	cs := g.Registry().Cheatsheet
	if cs == nil {
		t.Fatal("registry.Cheatsheet is nil")
	}
	if err := g.ContextTree().Push(cs); err != nil {
		t.Fatalf("Push(cheatsheet): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if !rec.HasSetView(string(types.CHEATSHEET)) {
		t.Fatal("CHEATSHEET SetView not invoked after Push")
	}
	found := false
	for _, vn := range rec.SetCurrentViewLog {
		if vn == string(types.CHEATSHEET) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SetCurrentView(%q) never invoked; SetCurrentViewLog = %v", types.CHEATSHEET, rec.SetCurrentViewLog)
	}
}

// TestRunLayoutWhichKeyOverlayFillsRectBody (dbsavvy-tro.11): with the
// WHICH_KEY notifier visible and only a couple of binding rows wired,
// the SetContent payload written into the WHICH_KEY view must span the
// popup's interior height — no fewer lines than the body-row spec.
// Without the padding, a sparse binding set produces 2-3 newlines and
// the popup rect's remaining rows hold whatever cells the underlying
// (stub-rect / extras / status) views happened to leave behind:
// "bleed-through" from the user's perspective.
//
// Asserts the orchestrator-level invariant: popup body row count >=
// (whichKeyMaxRows - 2) (the popup's interior height after subtracting
// the gocui frame). Also asserts the original binding rows survive at
// the top of the payload so a trivial "always emit empty" reward-hack
// in formatWhichKeyRows wouldn't pass.
func TestRunLayoutWhichKeyOverlayFillsRectBody(t *testing.T) {
	g, rec := buildTestGui(t)

	// Wire a deterministic 2-row resolver so HandleRender produces a
	// concrete payload instead of no-opping on a nil rows callback.
	wk := g.Registry().WhichKey
	if wk == nil {
		t.Fatal("registry.WhichKey is nil")
	}
	wk.SetRows(func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow {
		return []types.ChildRow{
			{Key: types.ChordKey{Code: 'a'}, Label: "alpha", IsLeaf: true},
			{Key: types.ChordKey{Code: 'b'}, Label: "bravo", IsLeaf: true},
		}
	})

	// Flip the notifier visible. zero-delay ShowAfter makes Visible()
	// return true synchronously.
	g.WhichKey().ShowAfter(0, types.GLOBAL, nil)

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if !rec.HasSetView(string(types.WHICH_KEY)) {
		t.Fatal("WHICH_KEY SetView not invoked while notifier reports visible")
	}

	body := rec.GetViewBuffer(string(types.WHICH_KEY))
	if body == "" {
		t.Fatalf("WHICH_KEY view buffer is empty; HandleRender did not write content")
	}
	lines := strings.Split(body, "\n")
	// Popup rect height is whichKeyMaxRows+1 cells (inclusive). The gocui
	// frame consumes 2 of those rows, leaving the interior height that
	// the body must cover.
	const wantMinLines = 10 // matches context.whichKeyBodyRows
	if len(lines) < wantMinLines {
		t.Errorf("popup body has %d lines, want >= %d (padding missing — bleed-through fix regressed); body=%q",
			len(lines), wantMinLines, body)
	}
	// The first two lines must be the real binding rows — guards
	// against a reward-hack where formatWhichKeyRows emits only blanks.
	if !strings.Contains(lines[0], "alpha") {
		t.Errorf("lines[0] = %q; expected 'alpha' label", lines[0])
	}
	if len(lines) > 1 && !strings.Contains(lines[1], "bravo") {
		t.Errorf("lines[1] = %q; expected 'bravo' label", lines[1])
	}
}

// TestRunLayoutWhichKeyOverlayExpandsToFitRows (dbsavvy-y5t): the
// which-key popup height must grow to fit every wired binding row
// (clamped to the screen), not a fixed 12-row rect that clipped
// everything past ~10 children with no way to scroll. Here 15 rows are
// wired; the popup's interior height (rect height minus the 2-row gocui
// frame) must be >= 15 so no binding is clipped off-screen.
func TestRunLayoutWhichKeyOverlayExpandsToFitRows(t *testing.T) {
	g, rec := buildTestGui(t)

	const rowCount = 15
	wk := g.Registry().WhichKey
	if wk == nil {
		t.Fatal("registry.WhichKey is nil")
	}
	wk.SetRows(func(_ types.ContextKey, _ []types.ChordKey) []types.ChildRow {
		rows := make([]types.ChildRow, rowCount)
		for i := range rows {
			rows[i] = types.ChildRow{Key: types.ChordKey{Code: rune('a' + i)}, Label: "row", IsLeaf: true}
		}
		return rows
	})
	g.WhichKey().ShowAfter(0, types.GLOBAL, nil)

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	h := -1
	for _, c := range rec.AllSetViewCalls() {
		if c.Name == string(types.WHICH_KEY) {
			h = c.Y1 - c.Y0
		}
	}
	if h < 0 {
		t.Fatal("WHICH_KEY SetView not invoked while notifier reports visible")
	}
	// Interior = rect height - 2 (top + bottom gocui frame).
	if h-2 < rowCount {
		t.Errorf("WHICH_KEY rect height = %d (interior %d); want interior >= %d so all %d bindings are visible without scrolling",
			h, h-2, rowCount, rowCount)
	}
}

// TestWhichKeyRowsResolverWiredAtBoot (dbsavvy-tro.4): the orchestrator
// MUST install a non-nil WhichKeyRows closure on the WhichKeyContext at
// boot so HandleRender can resolve children for the live trie. Without
// the wiring, the popup would render an empty body even when the trie
// has children for the current (scope, prefix). HasRows(GLOBAL, nil)
// must return true because the GLOBAL scope owns top-level chord roots
// (e.g. <leader>, ?) and every wired controller contributes at least
// one binding.
func TestWhichKeyRowsResolverWiredAtBoot(t *testing.T) {
	g, _ := buildTestGui(t)
	wk := g.Registry().WhichKey
	if wk == nil {
		t.Fatal("registry.WhichKey is nil")
	}
	// Empty prefix on GLOBAL scope: should resolve to the top-level
	// chord roots (one ChildRow per root key). If the closure is nil
	// the HasRows guard returns false and we'd fail here.
	if !wk.HasRows(types.GLOBAL, nil) {
		t.Fatal("HasRows(GLOBAL, nil) = false; WhichKeyRows closure not wired or trie empty")
	}
}

// TestRunLayoutWhichKeyOverlayEmptyRowsHidesPopup (dbsavvy-tro.4): when
// the WHICH_KEY notifier flips visible but the wired rows-resolver
// returns no children for the current (scope, prefix), the layout pass
// must dismiss the notifier and DeleteView the popup. Without this
// guard the user would see an empty popup rect hover until the
// notifier's TTL elapsed.
func TestRunLayoutWhichKeyOverlayEmptyRowsHidesPopup(t *testing.T) {
	g, rec := buildTestGui(t)

	// Wire an empty resolver so HasRows returns false even though the
	// notifier reports visible.
	wk := g.Registry().WhichKey
	if wk == nil {
		t.Fatal("registry.WhichKey is nil")
	}
	wk.SetRows(func(scope types.ContextKey, prefix []types.ChordKey) []types.ChildRow {
		return nil
	})

	// Flip the notifier visible with zero-delay so Visible() returns
	// true synchronously.
	g.WhichKey().ShowAfter(0, types.GLOBAL, nil)
	if !g.WhichKey().Visible() {
		t.Fatal("notifier did not flip visible after ShowAfter(0, …)")
	}

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	// Notifier must be hidden by the layout pass.
	if g.WhichKey().Visible() {
		t.Error("notifier still visible after empty-rows layout pass; Hide() not called")
	}
	// View must be DeleteView'd so no empty rect persists.
	found := false
	for _, name := range rec.DeleteViews {
		if name == string(types.WHICH_KEY) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("WHICH_KEY DeleteView not invoked; DeleteViews = %v", rec.DeleteViews)
	}
	// SetContent must NOT have been called on WHICH_KEY (the empty
	// branch returns before HandleRender writes any body).
	if rec.GetViewBuffer(string(types.WHICH_KEY)) != "" {
		t.Errorf("WHICH_KEY buffer non-empty (%q); empty-rows path should not write content",
			rec.GetViewBuffer(string(types.WHICH_KEY)))
	}
}

// TestRunLayoutCreatesPopupOnStack pushes MENU onto the focus stack
// and asserts the Tier-3 popup pass creates the view. After Pop, the
// next RunLayout pass must DeleteView the now-orphan popup.
func TestRunLayoutCreatesPopupOnStack(t *testing.T) {
	g, rec := buildTestGui(t)
	menu := g.Registry().Menu
	if menu == nil {
		t.Fatal("registry.Menu is nil")
	}
	if err := g.ContextTree().Push(menu); err != nil {
		t.Fatalf("Push(menu): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-push: %v", err)
	}
	if !rec.HasSetView(string(types.MENU)) {
		t.Fatal("MENU SetView not invoked after Push")
	}

	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-pop: %v", err)
	}
	found := false
	for _, name := range rec.DeleteViews {
		if name == string(types.MENU) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("MENU DeleteView not invoked after Pop")
	}
}

// TestCommandActionsFlipDriverCaret (dbsavvy-tro.2): the live wiring in
// gui.go assembles CommandLineCommandDeps.CaretToggler = driver.SetCaretEnabled.
// Invoke the registered command.open / command.cancel handlers and
// verify the recorder driver observed (true, false).
func TestCommandActionsFlipDriverCaret(t *testing.T) {
	g, rec := buildTestGui(t)
	openCmd, ok := g.CommandRegistry().Get(commands.CommandOpen)
	if !ok || openCmd == nil {
		t.Fatal("command.open not registered")
	}
	cancelCmd, ok := g.CommandRegistry().Get(commands.CommandCancel)
	if !ok || cancelCmd == nil {
		t.Fatal("command.cancel not registered")
	}
	if err := openCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("open Handler: %v", err)
	}
	if !rec.CaretEnabled {
		t.Errorf("after command.open: CaretEnabled = false, want true")
	}
	if err := cancelCmd.Handler(commands.ExecCtx{}); err != nil {
		t.Fatalf("cancel Handler: %v", err)
	}
	if rec.CaretEnabled {
		t.Errorf("after command.cancel: CaretEnabled = true, want false")
	}
	if got := rec.AllCaretEnabledLog(); len(got) != 2 || got[0] != true || got[1] != false {
		t.Errorf("CaretEnabledLog = %v, want [true false]", got)
	}
}

// TestRunLayoutCommandLineCaretAtPromptColumn (dbsavvy-tro.2): each
// Layout pass while COMMAND_LINE is on the focus stack must call
// driver.SetViewCursor on the command-line view at column = 1 +
// len(buffer) so the caret sits right after the ':' prompt.
//
// Under the RecorderGuiDriver SetView returns view=nil, so the layout
// branch falls back to the context's Buffer() method (which strips the
// leading ':') — assert the cursor X is `1 + len(buffer)` (i.e. 1 for
// an empty buffer).
func TestRunLayoutCommandLineCaretAtPromptColumn(t *testing.T) {
	g, rec := buildTestGui(t)
	cl := g.Registry().CommandLine
	if cl == nil {
		t.Fatal("registry.CommandLine is nil")
	}
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push(CommandLine): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	calls := rec.AllSetViewCursorCalls()
	var found *testfake.SetViewCursorCall
	for i := range calls {
		c := calls[i]
		if c.View == string(types.COMMAND_LINE) {
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatalf("SetViewCursor not called for COMMAND_LINE; calls = %+v", calls)
	}
	// Empty buffer → X = 1 (column after ':'). Y = 0 (single-row strip).
	if found.X != 1 || found.Y != 0 {
		t.Errorf("SetViewCursor(COMMAND_LINE) = (%d, %d), want (1, 0)", found.X, found.Y)
	}
}

// TestRunLayoutCommandLineCaretTracksBufferLength: after the user has
// typed "abc", the next Layout pass must put the caret at X = 4
// (1 for ':' + 3 typed chars). Drives SetBuffer on the test seam since
// the recorder driver returns view=nil.
func TestRunLayoutCommandLineCaretTracksBufferLength(t *testing.T) {
	g, rec := buildTestGui(t)
	cl := g.Registry().CommandLine
	if cl == nil {
		t.Fatal("registry.CommandLine is nil")
	}
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push(CommandLine): %v", err)
	}
	cl.SetBuffer("abc")
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	calls := rec.AllSetViewCursorCalls()
	var last *testfake.SetViewCursorCall
	for i := range calls {
		c := calls[i]
		if c.View == string(types.COMMAND_LINE) {
			last = &c
		}
	}
	if last == nil {
		t.Fatalf("no SetViewCursor(COMMAND_LINE); calls=%+v", calls)
	}
	if last.X != 4 || last.Y != 0 {
		t.Errorf("SetViewCursor(COMMAND_LINE) = (%d, %d), want (4, 0)", last.X, last.Y)
	}
}

// TestRunLayoutCommandLinePromptStyled (dbsavvy-tro.12): each Layout
// pass while COMMAND_LINE is on the focus stack must write a buffer
// content that opens with the PromptFg ANSI SGR escape, then ':', then
// the reset. Without the wrapper, the prompt renders in the terminal's
// default foreground — too dim against the CommandLine background.
func TestRunLayoutCommandLinePromptStyled(t *testing.T) {
	g, rec := buildTestGui(t)
	cl := g.Registry().CommandLine
	if cl == nil {
		t.Fatal("registry.CommandLine is nil")
	}
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push(CommandLine): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	body := rec.GetViewBuffer(string(types.COMMAND_LINE))
	if body == "" {
		t.Fatal("COMMAND_LINE view buffer is empty after RunLayout")
	}
	// PromptFg default is "yellow" → \x1b[33m. The buffer must start
	// with an SGR foreground escape immediately followed by ':'.
	if !strings.HasPrefix(body, "\x1b[") {
		t.Errorf("COMMAND_LINE buffer does not start with ANSI SGR escape; body=%q", body)
	}
	if !strings.Contains(body, "\x1b[33m:\x1b[0m") {
		t.Errorf("COMMAND_LINE buffer missing styled prompt %q; body=%q",
			"\x1b[33m:\x1b[0m", body)
	}
}

// TestRunLayoutCommandLinePromptStyled_BufferAppended asserts the typed
// text follows the styled ':' prompt unchanged. Regression-guards the
// scenario where the SetContent overlay accidentally drops the buffer.
func TestRunLayoutCommandLinePromptStyled_BufferAppended(t *testing.T) {
	g, rec := buildTestGui(t)
	cl := g.Registry().CommandLine
	if cl == nil {
		t.Fatal("registry.CommandLine is nil")
	}
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push(CommandLine): %v", err)
	}
	cl.SetBuffer("quit")
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	body := rec.GetViewBuffer(string(types.COMMAND_LINE))
	wantSuffix := "quit"
	if !strings.HasSuffix(body, wantSuffix) {
		t.Errorf("COMMAND_LINE buffer suffix = %q, want suffix %q; body=%q",
			body, wantSuffix, body)
	}
}

// row0Artifact returns the first SetView call whose declared rectangle
// touches row 0 (Y0 == 0) under a normal-size layout pass. Only LIMIT
// is permitted to span row 0 (and only in the tiny-terminal branch,
// which never executes here). Any other hit indicates a candidate
// source of the "thin blue line at canvas top" artifact described in
// dbsavvy-tro.13: a Frame=true gocui view's top border lands at row 0,
// outside the slot the boxlayout reserved for "options" (which the
// orchestrator deliberately leaves un-SetView'd).
func row0Artifact(calls []testfake.SetViewCall) (testfake.SetViewCall, bool) {
	for _, c := range calls {
		if c.Name == string(types.LIMIT) {
			continue
		}
		if c.Y0 == 0 {
			return c, true
		}
	}
	return testfake.SetViewCall{}, false
}

// TestRunLayoutRow0_NoArtifact_FirstFrame (dbsavvy-tro.13, hypothesis 4):
// the very first RunLayout pass after wireWithDriver must not create
// any view whose declared rectangle touches row 0. The orchestrator
// reserves row 0 for the "options" boxlayout slot but never calls
// SetView on it; any other view at Y0=0 would paint a frame border at
// the canvas top — the suspected source of the intermittent blue line.
//
// Locks in: hypothesis 1 (Tier-1 rail at Y0=0+Frame=true) has no
// matching code path; hypothesis 2 (bottomRightRect off-by-one) does
// not fire on a 120x40 first frame; hypothesis 3 (transient first-frame
// border) does not occur because no popup is on the stack.
func TestRunLayoutRow0_NoArtifact_FirstFrame(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	if c, hit := row0Artifact(rec.AllSetViewCalls()); hit {
		t.Errorf("first frame: view %q SetView'd with Y0=0 (rect=%+v) — blue-line candidate", c.Name, c)
	}
}

// TestRunLayoutRow0_NoArtifact_AfterPopupCycle (dbsavvy-tro.13): a
// popup show → hide cycle must not leave any view with Y0=0 in the
// final SetView log slice. Walks the scenario from the walkthrough:
// push MENU, run a frame, pop, run another frame. After the final
// frame, no popup view should occupy row 0; with dbsavvy-b1a
// Screen.Clear() in place, no stale row-0 cells survive either, so
// the only way row 0 could now show paint is via a SetView call we
// missed — which this assertion fails on.
func TestRunLayoutRow0_NoArtifact_AfterPopupCycle(t *testing.T) {
	g, rec := buildTestGui(t)
	menu := g.Registry().Menu
	if menu == nil {
		t.Fatal("registry.Menu is nil")
	}
	if err := g.ContextTree().Push(menu); err != nil {
		t.Fatalf("Push(menu): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-push: %v", err)
	}
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout post-pop: %v", err)
	}
	if c, hit := row0Artifact(rec.AllSetViewCalls()); hit {
		t.Errorf("post popup-cycle: view %q SetView'd with Y0=0 (rect=%+v) — blue-line candidate", c.Name, c)
	}
}

// TestRunLayoutRow0_NoArtifact_AfterResize (dbsavvy-tro.13): popup
// show → resize → popup hide. The resize transition is the canonical
// reproducer for stale back-buffer paint (different SetView rect on
// the second frame leaves the old border in the cell grid). With
// Screen.Clear() the back-buffer is wiped each frame, and no SetView
// in normal-size layout creates a Y0=0 rectangle, so row 0 stays clean
// across the transition.
func TestRunLayoutRow0_NoArtifact_AfterResize(t *testing.T) {
	g, rec := buildTestGui(t)
	menu := g.Registry().Menu
	if menu == nil {
		t.Fatal("registry.Menu is nil")
	}
	if err := g.ContextTree().Push(menu); err != nil {
		t.Fatalf("Push(menu): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout pre-resize: %v", err)
	}
	if err := g.RunLayout(100, 30); err != nil {
		t.Fatalf("RunLayout post-resize: %v", err)
	}
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := g.RunLayout(100, 30); err != nil {
		t.Fatalf("RunLayout post-pop: %v", err)
	}
	if c, hit := row0Artifact(rec.AllSetViewCalls()); hit {
		t.Errorf("post resize+pop: view %q SetView'd with Y0=0 (rect=%+v) — blue-line candidate", c.Name, c)
	}
}

// TestRunLayoutCommandLineCaretResetOnRePush: after Pop, HandleFocusLost
// clears the buffer; re-Push must produce a fresh SetViewCursor at X=1
// even if the previous buffer was longer.
func TestRunLayoutCommandLineCaretResetOnRePush(t *testing.T) {
	g, rec := buildTestGui(t)
	cl := g.Registry().CommandLine
	if cl == nil {
		t.Fatal("registry.CommandLine is nil")
	}
	// First push with a long buffer.
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push#1: %v", err)
	}
	cl.SetBuffer("hello world")
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout#1: %v", err)
	}
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	// Re-push — HandleFocusLost cleared buf to "".
	if err := g.ContextTree().Push(cl); err != nil {
		t.Fatalf("Push#2: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout#2: %v", err)
	}
	// The LAST SetViewCursor call for COMMAND_LINE must be at X=1.
	calls := rec.AllSetViewCursorCalls()
	var last *testfake.SetViewCursorCall
	for i := range calls {
		c := calls[i]
		if c.View == string(types.COMMAND_LINE) {
			last = &c
		}
	}
	if last == nil {
		t.Fatalf("no SetViewCursor(COMMAND_LINE) recorded; calls=%+v", calls)
	}
	if last.X != 1 || last.Y != 0 {
		t.Errorf("re-push SetViewCursor = (%d, %d), want (1, 0) — stale buffer length leaked", last.X, last.Y)
	}
}

// TestRunLayoutStatusBarRectHasVisibleInnerRow (dbsavvy-8tj) regresses
// the QA-1.1 "no status bar" bug. The lazygit gocui fork computes a
// view's writable InnerHeight as Height-2 regardless of Frame and
// writes cells at screen position (x0+x+1, y0+y+1). boxlayout's Size:2
// slot yields Y1-Y0==1 (Height=2 → InnerHeight=0 → invisible), so the
// Tier-4a SetView call must expand the rect by -1/+1 in Y to land the
// two visible rows on the boxlayout-reserved screen rows. Asserts (a)
// the status view IS laid out, and (b) the expanded rectangle has
// Height==4 (gocui InnerHeight==2) with the two inner rows on the
// canvas bottom. Width is asserted >= 3 for symmetry, though boxlayout
// currently always gives status the full canvas width.
func TestRunLayoutStatusBarRectHasVisibleInnerRow(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	var found *testfake.SetViewCall
	for i := range rec.AllSetViewCalls() {
		c := rec.AllSetViewCalls()[i]
		if c.Name == orchestrator.AppStatusViewName {
			found = &c
		}
	}
	if found == nil {
		t.Fatalf("status view never SetView'd; calls=%+v", rec.AllSetViewCalls())
	}

	// gocui (lazygit fork) view.go:540: InnerHeight = max(0, Height-2)
	// where Height = Y1-Y0+1. The Size:2 boxlayout slot (rows 38-39 on a
	// 40-row canvas) is expanded -1/+1 in Y to SetView(37,40) → Height=4
	// → InnerHeight=2. We assert Height==4 so the bar has exactly two
	// visible inner rows.
	if h := found.Y1 - found.Y0 + 1; h != 4 {
		t.Errorf("status SetView rect Height=%d (Y0=%d Y1=%d); want 4 so gocui InnerHeight == 2 (2-row status bar)",
			h, found.Y0, found.Y1)
	}
	if w := found.X1 - found.X0 + 1; w < 3 {
		t.Errorf("status SetView rect Width=%d (X0=%d X1=%d); want >= 3 so gocui InnerWidth >= 1",
			w, found.X0, found.X1)
	}

	// The two visible inner rows (gocui writes at y0+1 and y0+2) must land
	// on the boxlayout-reserved screen rows at the canvas bottom — for a
	// 40-row terminal that's rows 38 and 39. If layout's expansion drifts
	// off the reserved slot, the bar would render over the body or be
	// clipped.
	const wantTopInnerRow, wantBottomInnerRow = 38, 39
	if got := found.Y0 + 1; got != wantTopInnerRow {
		t.Errorf("status top visible inner row = %d (Y0=%d), want %d", got, found.Y0, wantTopInnerRow)
	}
	if got := found.Y0 + 2; got != wantBottomInnerRow {
		t.Errorf("status bottom visible inner row = %d (Y0=%d), want %d (bottom of 40-row canvas)",
			got, found.Y0, wantBottomInnerRow)
	}
}

// TestRunLayoutSeedsCellEditorTextAreaFromInitial (dbsavvy-tzi.2): the
// layout's CELL_EDITOR branch must plumb the live view into the context
// and seed the fresh view's TextArea from Initial() exactly once, so
// Buffer()/ReadAndClearBuffer() read the live TextArea (not the test-mode
// buf). Uses the recorder's opt-in real-view path so there is a real
// *gocui.View with a TextArea to seed.
func TestRunLayoutSeedsCellEditorTextAreaFromInitial(t *testing.T) {
	g, rec := buildTestGui(t)

	cec := g.Registry().CellEditor
	if cec == nil {
		t.Fatal("Registry().CellEditor is nil")
	}
	name := cec.GetViewName()
	rec.EnableRealView(name)

	cec.Open("alice", models.ColumnMeta{}, []any{1}, "alice")
	if err := g.ContextTree().Push(cec); err != nil {
		t.Fatalf("Push(CELL_EDITOR): %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	// Buffer() reads view.TextArea.GetContent() once the view is plumbed —
	// proves the view was both plumbed (SetView) AND seeded from Initial().
	if got := cec.Buffer(); got != "alice" {
		t.Fatalf("Buffer() = %q, want %q (TextArea not plumbed/seeded)", got, "alice")
	}

	// Type an extra char into the LIVE TextArea so it diverges from the
	// test-mode buf ("alice"). ReadAndClearBuffer must read the TextArea
	// ("aliceX"), proving the source-of-truth is the live view, not buf.
	v := rec.RealView(name)
	if v == nil || v.TextArea == nil {
		t.Fatal("RealView returned nil view/TextArea after layout")
	}
	v.TextArea.TypeCharacter("X")
	if got := cec.ReadAndClearBuffer(); got != "aliceX" {
		t.Fatalf("ReadAndClearBuffer() = %q, want %q (read buf instead of live TextArea)", got, "aliceX")
	}
}

// TestRunLayoutCellEditorRePushSeedsNewValue (dbsavvy-tzi.2): popping the
// CELL_EDITOR and running a layout pass tears down the view (the off-stack
// teardown loop DeleteViews it, which evicts the recorder's cached real
// view). Re-opening on a new value and re-pushing must seed a FRESH view
// with the new value — no leftover "alice".
func TestRunLayoutCellEditorRePushSeedsNewValue(t *testing.T) {
	g, rec := buildTestGui(t)

	cec := g.Registry().CellEditor
	if cec == nil {
		t.Fatal("Registry().CellEditor is nil")
	}
	name := cec.GetViewName()
	rec.EnableRealView(name)

	// First edit: "alice".
	cec.Open("alice", models.ColumnMeta{}, []any{1}, "alice")
	if err := g.ContextTree().Push(cec); err != nil {
		t.Fatalf("Push #1: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout #1: %v", err)
	}
	if got := cec.Buffer(); got != "alice" {
		t.Fatalf("Buffer() after first open = %q, want %q", got, "alice")
	}

	// Pop + layout: the off-stack teardown loop DeleteViews CELL_EDITOR,
	// which evicts the recorder's cached real view so the next SetView
	// re-creates a fresh one (returning ErrUnknownView → freshView=true).
	if err := g.ContextTree().Pop(); err != nil {
		t.Fatalf("Pop: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout (teardown): %v", err)
	}
	cec.Close()

	// Second edit: "bob".
	cec.Open("bob", models.ColumnMeta{}, []any{2}, "bob")
	if err := g.ContextTree().Push(cec); err != nil {
		t.Fatalf("Push #2: %v", err)
	}
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout #2: %v", err)
	}
	if got := cec.Buffer(); got != "bob" {
		t.Fatalf("Buffer() after re-open = %q, want %q (leftover state or stale view)", got, "bob")
	}
}

// seedEditorBuffer inserts text into a *editor.Buffer at origin and parks
// the cursor at (0,0). Helper for the seam-2 yank-flash render tests.
func seedEditorBuffer(t *testing.T, buf *editor.Buffer, text string) {
	t.Helper()
	if err := buf.Apply(editor.Edit{
		Kind:  editor.EditKindInsert,
		Range: editor.Range{Start: editor.Position{Line: 0, Col: 0}, End: editor.Position{Line: 0, Col: 0}},
		Text:  text,
	}); err != nil {
		t.Fatalf("seed editor buffer: %v", err)
	}
	buf.SetCursor(editor.Position{Line: 0, Col: 0})
}

// TestRunLayoutPaintsYankFlash exercises SEAM 2 (RunLayout's QUERY_EDITOR
// sync block) end-to-end: a flash set DIRECTLY on the live editor buffer
// (no Task-C trigger) must be read via YankFlashSnapshot and fed through
// editor.ApplyYankFlashOverlay in its OWN nil-check (no live selection),
// then SetContent'd into the main pane. The layout writes to a real
// *gocui.View, which parses the overlay's ANSI into per-cell attributes —
// so the read-back view content holds the flashed glyphs (ANSI stripped);
// the raw \x1b[43m is asserted at the pure-overlay layer in the editor
// package's yank_flash_test.go. This proves the seam reads the snapshot
// in the real layout path and renders without panic.
func TestRunLayoutPaintsYankFlash(t *testing.T) {
	g, rec := buildTestGui(t)
	// Make SetView(QUERY_EDITOR) return a real view so the sync block's
	// `if v != nil` branch (and thus v.SetContent) executes.
	rec.EnableRealView(string(types.QUERY_EDITOR))
	// Pop the CONNECTION_MANAGER modal so the main pane lays out.
	_ = g.ContextTree().Push(g.Registry().Schemas)

	qec := g.Registry().QueryEditor
	if qec == nil {
		t.Fatal("registry.QueryEditor is nil")
	}
	buf := qec.Buffer()
	seedEditorBuffer(t, buf, "SELECT id FROM bar")
	buf.SetYankFlash(editor.Range{
		Start: editor.Position{Line: 0, Col: 0},
		End:   editor.Position{Line: 0, Col: 6},
	})
	if buf.SelectionSnapshot() != nil {
		t.Fatal("precondition: no live selection expected for a normal-mode yank")
	}

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}

	v := rec.RealView(string(types.QUERY_EDITOR))
	if v == nil {
		t.Fatal("QUERY_EDITOR real view not created; seam never ran")
	}
	if got := v.Buffer(); !strings.Contains(got, "SELECT id FROM bar") {
		t.Fatalf("flashed text missing from rendered main pane: %q", got)
	}
}

// TestRunLayoutNoFlashRegression locks SEAM 2's no-flash path: with no
// flash armed, the rendered main-pane content must equal the
// selection-only render of the same buffer (glyph-level, since gocui
// strips ANSI to attributes). Guards against the flash block mutating
// output when no flash is present.
func TestRunLayoutNoFlashRegression(t *testing.T) {
	render := func(arm func(*editor.Buffer)) string {
		g, rec := buildTestGui(t)
		rec.EnableRealView(string(types.QUERY_EDITOR))
		_ = g.ContextTree().Push(g.Registry().Schemas)
		buf := g.Registry().QueryEditor.Buffer()
		seedEditorBuffer(t, buf, "SELECT id FROM bar")
		arm(buf)
		if err := g.RunLayout(120, 40); err != nil {
			t.Fatalf("RunLayout: %v", err)
		}
		v := rec.RealView(string(types.QUERY_EDITOR))
		if v == nil {
			t.Fatal("QUERY_EDITOR real view not created")
		}
		return v.Buffer()
	}

	baseline := render(func(*editor.Buffer) {})
	cleared := render(func(b *editor.Buffer) {
		epoch := b.SetYankFlash(editor.Range{
			Start: editor.Position{Line: 0, Col: 0},
			End:   editor.Position{Line: 0, Col: 6},
		})
		b.ClearYankFlash(epoch)
	})
	if cleared != baseline {
		t.Fatalf("no-flash render diverged:\n  got      %q\n  baseline %q", cleared, baseline)
	}
}

// TestRunLayoutOutOfBoundsFlashNoPanic feeds SEAM 2 a flash range past the
// buffer bounds. ApplyYankFlashOverlay is panic-safe; RunLayout must
// complete without panic.
func TestRunLayoutOutOfBoundsFlashNoPanic(t *testing.T) {
	g, rec := buildTestGui(t)
	rec.EnableRealView(string(types.QUERY_EDITOR))
	_ = g.ContextTree().Push(g.Registry().Schemas)
	buf := g.Registry().QueryEditor.Buffer()
	seedEditorBuffer(t, buf, "abc")
	buf.SetYankFlash(editor.Range{
		Start: editor.Position{Line: 5, Col: 0},
		End:   editor.Position{Line: 6, Col: 99},
	})

	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	v := rec.RealView(string(types.QUERY_EDITOR))
	if v == nil {
		t.Fatal("QUERY_EDITOR real view not created")
	}
	if got := v.Buffer(); !strings.Contains(got, "abc") {
		t.Fatalf("text missing after out-of-bounds flash render: %q", got)
	}
}
