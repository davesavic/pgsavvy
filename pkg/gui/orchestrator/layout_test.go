package orchestrator_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
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
		string(types.CONNECTIONS),
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
	for _, name := range []string{
		string(types.QUERY_EDITOR),
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

func TestRunLayoutCreatesSideRails(t *testing.T) {
	g, rec := buildTestGui(t)
	if err := g.RunLayout(120, 40); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	for _, name := range []string{
		string(types.CONNECTIONS),
		string(types.SCHEMAS),
		string(types.TABLES),
		string(types.COLUMNS),
		string(types.INDEXES),
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
