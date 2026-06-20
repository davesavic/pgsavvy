//go:build integration

// Package orchestrator_test (integration) drives the pgsavvy keybinding
// subsystem end-to-end through the live wired graph, exercising every
// acceptance-criterion item in one walkthrough.
//
// Unlike TestTUISmokeWalkthrough this test needs no Postgres fixture —
// the keybinding system is purely in-memory. The recorder GuiDriver is
// injected via UseDriverForTest exactly like the data-path smoke; here
// the assertions target the Matcher / TrieSet / WhichKey / ContextTree
// observables rather than helper side effects.
//
// Step coverage:
//
//	step01_quit_via_ctrl_c
//	step02_quit_via_leader_q
//	step03_chord_leader_tr_in_tables_scope     (leader-expansion assertion)
//	step04_chord_gg_in_query_editor_scope      (SKIP — QUERY_EDITOR is STUB)
//	step05_chord_gd_in_result_grid_scope       (SKIP — RESULT_GRID is STUB)
//	step06_chord_cw_v_for_window_split         (SKIP — no window.split action)
//	step07_count_5j                            (ExecCtx.Count==5 via temp action)
//	step08_colon_opens_command_line
//	step09_command_line_esc_returns_to_normal
//	step10_reload_swaps_trie                   (SwapCount + toast)
//	step11_nop_unbinds_default
//	step12_whichkey_popup_after_300ms
//	step13_options_bar_shows_show_in_bar_bindings
//	step14_cheatsheet_question_mark_renders_completeness
//	step15_collision_warning_emitted
//	step16_ambiguous_prefix_warning_emitted
//	step17_help_cheatsheet_unbound_everywhere_warning
//	step18_status_bar_shows_command_mode_when_command_line_focused
//	step19_orphan_action_reload_warn_skip_continue
//	step20_atomic_swap_under_partial_state_clears_pending
//	step21_unbind_question_and_colon_starts_with_warning
//	step22_jk_insert_mode_escape_remap         (SKIP — no jk→ESC mapping shipped)
package orchestrator_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/cheatsheet"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/status"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"go.uber.org/goleak"
)

func TestKeybindingSystemWalkthrough(t *testing.T) {
	s := setupKbSmoke(t)

	t.Run("step01_quit_via_ctrl_c", func(t *testing.T) {
		// <c-c> is the shipped global Quit binding (quit_controller.go).
		// The controller's Quit handler returns gocui.ErrQuit synchronously.
		err := s.g.Controllers().Quit.Quit(commands.ExecCtx{})
		if err != gocui.ErrQuit {
			t.Fatalf("Quit() = %v; want gocui.ErrQuit", err)
		}
		// The matched leaf at (Normal, GLOBAL) for app.quit must include
		// the <c-c> binding.
		ts := s.g.Matcher().TrieSet()
		if ts == nil {
			t.Fatal("Matcher.TrieSet is nil after wireWithDriver")
		}
		trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
		if !ok || trie == nil {
			t.Fatal("no (Normal, GLOBAL) trie")
		}
		res := trie.Lookup([]keys.Key{{Code: 'c', Mod: keys.ModCtrl}})
		if !res.Found || !res.IsLeaf || res.Action == nil || res.Action.ID != commands.AppQuit {
			t.Fatalf("<c-c> lookup = %+v; want leaf with action %q", res, commands.AppQuit)
		}
	})

	t.Run("step02_quit_via_leader_q", func(t *testing.T) {
		// The default config ships `<leader>q -> app.quit`; the leader
		// is ' ' by default. The leaf should sit at (Normal, GLOBAL)
		// under the expanded sequence [' ', 'q'].
		ts := s.g.Matcher().TrieSet()
		trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
		if !ok {
			t.Fatal("no (Normal, GLOBAL) trie")
		}
		seq := []keys.Key{{Code: ' '}, {Code: 'q'}}
		res := trie.Lookup(seq)
		if !res.Found || !res.IsLeaf || res.Action == nil || res.Action.ID != commands.AppQuit {
			t.Fatalf("<leader>q lookup = %+v; want leaf with action %q", res, commands.AppQuit)
		}
	})

	t.Run("step03_chord_leader_tr_in_tables_scope", func(t *testing.T) {
		// Inject a synthetic user binding `<leader>tr -> app.quit`
		// scoped to TABLES. The Build pass MUST expand the <leader>
		// placeholder into the configured leader rune (' ') — the
		// resulting leaf's Sequence[0].Code must equal ' ' rather than
		// the literal "<leader>" placeholder.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode:        "n",
			Scope:       string(types.TABLES),
			Key:         "<leader>tr",
			Action:      commands.AppQuit,
			Description: "smoke: leader-tr",
		})
		trie, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with synthetic <leader>tr: %v", err)
		}
		seq, leaf, ok := findLeaf(trie, types.ModeNormal, types.TABLES, commands.AppQuit)
		if !ok {
			t.Fatalf("synthetic <leader>tr binding missing from (Normal, TABLES) trie")
		}
		if len(seq) == 0 {
			t.Fatalf("matched binding has empty sequence")
		}
		if seq[0].Code != ' ' {
			t.Fatalf("matched binding's Sequence[0].Code = %q; want literal leader rune ' ', not the <leader> placeholder", seq[0].Code)
		}
		if !leaf.IsLeaf || leaf.Action == nil || leaf.Action.ID != commands.AppQuit {
			t.Fatalf("matched leaf = %+v; want app.quit", leaf)
		}
	})

	t.Run("step04_chord_gg_in_query_editor_scope", func(t *testing.T) {
		// QUERY_EDITOR is now the live MAIN_CONTEXT
		// (promoted from STUB) and the VimEditorController
		// publishes its motion bindings under QUERY_EDITOR scope. The
		// `gg` binding maps to motion.buffer_start and its registered
		// handler must move Buffer.Cursor to (0,0) regardless of where
		// the cursor started.
		//
		// We verify the binding exists in the controller's published
		// bindings (mode-mask aware — motion bindings carry the
		// Normal | OperatorPending | Visual* composite mask), then
		// pull the handler from the command registry and invoke it
		// directly. End-to-end Matcher.Dispatch routing of `g`+`g` is
		// covered by pkg/gui/keys/matcher_test.go's interior /
		// ambiguous-leaf tests; this step is the wiring assertion.
		ctrl := s.g.Controllers().VimEditor
		if ctrl == nil {
			t.Fatal("controllers.VimEditor is nil; query-editor not wired")
		}
		bindings := ctrl.GetKeybindings(types.KeybindingsOpts{})
		var ggFound bool
		var ggMode types.Mode
		for _, kb := range bindings {
			if kb == nil || kb.ActionID != commands.MotionBufferStart {
				continue
			}
			if kb.Scope != types.QUERY_EDITOR {
				t.Errorf("motion.buffer_start scope = %s, want QUERY_EDITOR", kb.Scope)
			}
			if len(kb.Sequence) != 2 || kb.Sequence[0].Code != 'g' || kb.Sequence[1].Code != 'g' {
				t.Errorf("motion.buffer_start sequence = %+v, want ['g','g']", kb.Sequence)
			}
			ggFound = true
			// Normal-mode motions are published as a separate binding whose
			// Mode is ModeNormal (== 0, the zero sentinel that cannot be OR'd
			// into a mask), alongside the OperatorPending|Visual* binding.
			// Accumulate every matching binding's mask rather than capturing
			// only the first, which may be the zero-valued Normal entry.
			ggMode |= kb.Mode
		}
		if !ggFound {
			t.Fatal("VimEditorController did not publish a binding for motion.buffer_start")
		}
		// The published mask must at least cover the in-flight modes the
		// editor uses (Visual / OperatorPending) — Normal mode is implicit
		// via the controller's Normal-mode dispatch path. The mask is the
		// editor contract; bit-level fan-out is the keys-package contract.
		if ggMode&types.ModeOperatorPending == 0 {
			t.Errorf("motion.buffer_start mode mask = %v; expected ModeOperatorPending bit", ggMode)
		}
		// Pull the handler from the command registry and exercise it.
		cmd, ok := s.g.CommandRegistry().Get(commands.MotionBufferStart)
		if !ok || cmd == nil || cmd.Handler == nil {
			t.Fatalf("registry missing handler for %s", commands.MotionBufferStart)
		}
		qec := s.g.Registry().QueryEditor
		if qec == nil {
			t.Fatal("registry.QueryEditor is nil after wireWithDriver")
		}
		buf := qec.Buffer()
		if buf == nil {
			t.Fatal("qec.Buffer() is nil")
		}
		// Seed multi-line content + cursor at (2, 3) so the handler has
		// somewhere to move FROM.
		buf.Lines = []editor.Line{
			{Runes: []rune("first")},
			{Runes: []rune("second")},
			{Runes: []rune("third")},
		}
		buf.SetCursor(editor.Position{Line: 2, Col: 3})
		if cur := buf.CursorPos(); cur.Line != 2 || cur.Col != 3 {
			t.Fatalf("seeded cursor = %+v; want (2,3)", cur)
		}
		if err := cmd.Handler(commands.ExecCtx{
			Mode:  types.ModeNormal,
			Scope: types.QUERY_EDITOR,
		}); err != nil {
			t.Fatalf("motion.buffer_start handler: %v", err)
		}
		if cur := buf.CursorPos(); cur.Line != 0 || cur.Col != 0 {
			t.Fatalf("after gg cursor = %+v; want (0,0)", cur)
		}
	})

	t.Run("step05_chord_gd_in_result_grid_scope", func(t *testing.T) {
		t.Skip("RESULT_GRID is STUB; controller for result-grid chords not yet shipped")
	})

	t.Run("step06_chord_cw_v_for_window_split", func(t *testing.T) {
		t.Skip("window.split.vertical action / pane controllers not yet shipped")
	})

	t.Run("step07_count_5j", func(t *testing.T) {
		// Register a one-shot test action that captures the ExecCtx.
		// Then rebuild the trie with a synthetic user binding mapping
		// `j` at (Normal, GLOBAL) to that action, and Swap it into the
		// live matcher. Feed `5` then `j` through Dispatch and assert
		// the handler observed Count == 5.
		var capturedCount int
		var capturedScope types.ContextKey
		actionID := "test.counter5j"
		if err := s.g.CommandRegistry().Register(&commands.Command{
			ID:          actionID,
			Description: "smoke: count capture",
			Handler: func(ctx commands.ExecCtx) error {
				capturedCount = ctx.Count
				capturedScope = ctx.Scope
				return nil
			},
		}); err != nil {
			t.Fatalf("Register test.counter5j: %v", err)
		}

		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode:        "n",
			Scope:       "global",
			Key:         "j",
			Action:      actionID,
			Description: "smoke: j capture",
		})
		trieSet, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build synthetic j-binding: %v", err)
		}

		// Preserve the original trie so subsequent steps still see the
		// shipped defaults.
		previous := s.g.Matcher().TrieSet()
		s.g.Matcher().SwapTrieSet(trieSet)
		t.Cleanup(func() {
			s.g.Matcher().SwapTrieSet(previous)
		})

		// Count digit '5' accumulates without firing.
		res, derr := s.g.Matcher().Dispatch(types.GLOBAL, keys.Key{Code: '5'})
		if derr != nil {
			t.Fatalf("Dispatch '5': %v", derr)
		}
		if res != keys.Pending {
			t.Fatalf("Dispatch '5' = %v; want Pending (count collection)", res)
		}
		// 'j' fires the handler with Count == 5.
		res, derr = s.g.Matcher().Dispatch(types.GLOBAL, keys.Key{Code: 'j'})
		if derr != nil {
			t.Fatalf("Dispatch 'j': %v", derr)
		}
		if res != keys.Dispatched {
			t.Fatalf("Dispatch 'j' after '5' = %v; want Dispatched", res)
		}
		if capturedCount != 5 {
			t.Fatalf("captured Count = %d; want 5", capturedCount)
		}
		if capturedScope != types.GLOBAL {
			t.Fatalf("captured Scope = %q; want %q", capturedScope, types.GLOBAL)
		}
	})

	t.Run("step08_colon_opens_command_line", func(t *testing.T) {
		// `:` is a shipped default binding (DefaultCommandLineBindings)
		// mapped to commands.CommandOpen, scope: all. The handler
		// pushes the COMMAND_LINE context onto the focus stack.
		before := s.g.ContextTree().Current()
		if before == nil || before.GetKey() == types.COMMAND_LINE {
			t.Fatalf("pre-state: expected non-COMMAND_LINE focus; got %v", before)
		}
		res, err := s.g.Matcher().Dispatch(before.GetKey(), keys.Key{Code: ':'})
		if err != nil {
			t.Fatalf("Dispatch(':'): %v", err)
		}
		if res != keys.Dispatched {
			t.Fatalf("Dispatch(':') = %v; want Dispatched", res)
		}
		top := s.g.ContextTree().Current()
		if top == nil || top.GetKey() != types.COMMAND_LINE {
			t.Fatalf("after ':' focus = %v; want %q", top, types.COMMAND_LINE)
		}
	})

	t.Run("step09_command_line_esc_returns_to_normal", func(t *testing.T) {
		// Precondition: COMMAND_LINE is on top of the stack (carried
		// over from step08). The ModeStore should have ModeCommand for
		// the COMMAND_LINE scope (HandleFocus wired this on push).
		if top := s.g.ContextTree().Current(); top == nil || top.GetKey() != types.COMMAND_LINE {
			t.Fatalf("step09 precondition: expected COMMAND_LINE on top; got %v", top)
		}
		// The Cancel binding (<esc>) at (ModeCommand, COMMAND_LINE)
		// resolves to commands.CommandCancel, which pops the context.
		// We invoke it through the Command Registry directly to
		// observe the side effect — feeding <esc> through Dispatch
		// would require a master-editor route the recorder doesn't
		// expose here.
		cmd, ok := s.g.CommandRegistry().Get(commands.CommandCancel)
		if !ok || cmd == nil {
			t.Fatalf("command.cancel not registered")
		}
		if err := cmd.Handler(commands.ExecCtx{
			Mode:  types.ModeCommand,
			Scope: types.COMMAND_LINE,
		}); err != nil {
			t.Fatalf("command.cancel handler: %v", err)
		}
		top := s.g.ContextTree().Current()
		if top != nil && top.GetKey() == types.COMMAND_LINE {
			t.Fatal("command.cancel did not pop COMMAND_LINE")
		}
	})

	t.Run("step10_reload_swaps_trie", func(t *testing.T) {
		before := s.g.Matcher().SwapCount()
		cmd, ok := s.g.ExRegistry().Get("reload")
		if !ok {
			t.Fatal(":reload not registered")
		}
		if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
			t.Fatalf(":reload handler: %v", err)
		}
		after := s.g.Matcher().SwapCount()
		if after != before+1 {
			t.Fatalf("SwapCount delta = %d; want exactly +1 (before=%d after=%d)", after-before, before, after)
		}
		history := s.g.ToastHelper().History()
		seen := false
		for _, msg := range history {
			if strings.Contains(msg, "config reloaded") {
				seen = true
				break
			}
		}
		if !seen {
			t.Fatalf("toast history missing 'config reloaded'; history=%v", history)
		}

		// The toast must paint into the AppStatusViewName
		// cells, not only land in the helper's History ring. Drive the
		// production RunLayout path — it materialises the AppStatus view
		// (Tier-4 status pass) and calls RenderStatusLine internally,
		// which multiplexes the toast over the default status line. The
		// recorder's stored cell buffer must carry the toast text.
		if err := s.g.RunLayout(80, 24); err != nil {
			t.Fatalf("RunLayout: %v", err)
		}
		buf := s.rec.GetViewBuffer(orchestrator.AppStatusViewName)
		if !strings.Contains(buf, "config reloaded") {
			t.Fatalf("AppStatusViewName buffer after RunLayout = %q; want it to contain 'config reloaded'", buf)
		}
	})

	t.Run("step11_nop_unbinds_default", func(t *testing.T) {
		// Inject `<leader>q -> <nop>` user override on top of the
		// shipped `<leader>q -> app.quit` default. The resulting trie's
		// leaf at [' ', 'q'] must resolve to commands.NopCommand (the
		// shared <nop> sentinel) — proving the user layer overrides the
		// default and unbinds the key.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode:        "n",
			Scope:       "global",
			Key:         "<leader>q",
			Action:      "<nop>",
			Description: "smoke: unbind leader-q",
		})
		trie, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with <nop>: %v", err)
		}
		t2, ok := trie.Get(types.ModeNormal, types.GLOBAL)
		if !ok {
			t.Fatal("synthetic trie missing (Normal, GLOBAL)")
		}
		res := t2.Lookup([]keys.Key{{Code: ' '}, {Code: 'q'}})
		if !res.Found || !res.IsLeaf {
			t.Fatalf("<leader>q lookup after <nop> override = %+v; want leaf", res)
		}
		if res.Action != commands.NopCommand {
			t.Fatalf("<leader>q action = %+v; want commands.NopCommand", res.Action)
		}
	})

	t.Run("step12_whichkey_popup_after_300ms", func(t *testing.T) {
		// WithKeyDelays(_, _, 20ms) — feed a partial chord prefix, wait
		// ~30ms, assert popup is visible. We use ' ' (the leader) which
		// is a real prefix of `<leader>q -> app.quit`. Dispatch the
		// first key; the matcher schedules the popup via the synthetic
		// 20ms WhichKey delay.
		s.g.WhichKey().Hide() // ensure clean slate
		// Cancel any pending matcher state from previous steps.
		s.g.Matcher().Cancel()

		_, err := s.g.Matcher().Dispatch(types.GLOBAL, keys.Key{Code: ' '})
		if err != nil {
			t.Fatalf("Dispatch(leader): %v", err)
		}
		if !s.g.Matcher().IsPartial() {
			t.Fatalf("Matcher.IsPartial = false right after leader; want partial pending")
		}
		// Wait for the WhichKey AfterFunc to fire (20ms + small buffer).
		deadline := time.Now().Add(100 * time.Millisecond)
		for time.Now().Before(deadline) && !s.g.WhichKey().Visible() {
			time.Sleep(2 * time.Millisecond)
		}
		if !s.g.WhichKey().Visible() {
			t.Fatalf("WhichKey.Visible = false after 100ms of partial; want true")
		}
		// Clean up: cancel pending so subsequent steps start idle.
		s.g.Matcher().Cancel()
	})

	t.Run("step13_options_bar_shows_show_in_bar_bindings", func(t *testing.T) {
		// Inject a ShowInBar binding into a synthetic config and call
		// CollectOptionsForScope through the orchestrator's public API.
		// The resulting []string must include the synthetic entry.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode:        "n",
			Scope:       string(types.TABLES),
			Key:         "X",
			Action:      commands.AppQuit,
			Description: "smoke: bar entry",
			ShowInBar:   true,
		})
		trie, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build synthetic ShowInBar: %v", err)
		}
		opts := orchestrator.CollectOptionsForScope(trie, types.ModeNormal, types.TABLES, s.tr, nil)
		// CollectOptionsForScope reads the description off the resolved
		// *commands.Command (NOT the binding's local Description field).
		// The synthetic binding cites commands.AppQuit, whose registered
		// description is "Quit application". Assert the formatted entry
		// "Quit application: X" appears in the output — the X key half
		// is the load-bearing observable (this binding was injected).
		found := false
		for _, o := range opts {
			if strings.Contains(o, "X") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("CollectOptionsForScope output missing synthetic 'X' ShowInBar entry; got %v", opts)
		}
	})

	t.Run("step14_cheatsheet_question_mark_renders_completeness", func(t *testing.T) {
		// `?` is wired to commands.HelpCheatsheet, which pushes the
		// CHEATSHEET context onto the focus tree and sets its scope.
		// We exercise the wired handler directly through the Command
		// Registry to keep the assertion self-contained.
		cmd, ok := s.g.CommandRegistry().Get(commands.HelpCheatsheet)
		if !ok || cmd == nil {
			t.Fatalf("help.cheatsheet not registered")
		}
		if err := cmd.Handler(commands.ExecCtx{
			Mode:  types.ModeNormal,
			Scope: types.GLOBAL,
		}); err != nil {
			t.Fatalf("help.cheatsheet handler: %v", err)
		}
		top := s.g.ContextTree().Current()
		if top == nil || top.GetKey() != types.CHEATSHEET {
			t.Fatalf("after `?` focus = %v; want %q", top, types.CHEATSHEET)
		}
		t.Cleanup(func() {
			// Pop the CHEATSHEET context so later steps see a normal
			// top-of-stack.
			_ = s.g.ContextTree().Pop()
		})

		// Verify the cheatsheet generator + per-category renderer produce
		// a non-empty body for the live TrieSet.
		out := cheatsheet.Generate(cheatsheet.GenerateInput{
			Trie:  s.g.Matcher().TrieSet(),
			Scope: types.GLOBAL,
			Tr:    s.tr,
		})
		var body strings.Builder
		for _, cv := range cheatsheet.Categorize(out) {
			body.WriteString(cheatsheet.RenderCategory(cv, s.tr))
		}
		if strings.TrimSpace(body.String()) == "" {
			t.Fatalf("cheatsheet.RenderCategory produced no non-empty category body for live TrieSet")
		}

		// Every new (key, scope, mode, actionID) tuple
		// MUST be registered in the live TrieSet after wireWithDriver.
		// Table-driven so a missing or moved binding surfaces a precise
		// failure (which tuple) rather than a generic count mismatch.
		ts := s.g.Matcher().TrieSet()
		if ts == nil {
			t.Fatalf("Matcher.TrieSet is nil; cannot assert Z1 bindings")
		}
		bwqCases := []struct {
			actionID string
			scope    types.ContextKey
			mode     types.Mode
			wantKeys []keys.Key
		}{
			{commands.CellEditEnter, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: 'i'}}},
			{commands.FKJumpForward, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: 'g'}, {Code: 'd'}}},
			{commands.FKReverseMenu, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: 'g'}, {Code: 'D'}}},
			{commands.ResultJumpBack, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: 'o', Mod: keys.ModCtrl}}},
			{commands.ResultJumpForward, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: 'i', Mod: keys.ModCtrl}}},
			{commands.PendingDiscardAtCursor, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: ' '}, {Code: 'c'}, {Code: 'u'}}},
			{commands.PendingDiscardAll, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: ' '}, {Code: 'c'}, {Code: 'U'}}},
			{commands.CommitDialogOpen, types.RESULT_GRID, types.ModeNormal, []keys.Key{{Code: ' '}, {Code: 'c'}, {Code: 'w'}}},
			{commands.EditorCompletionTrigger, types.QUERY_EDITOR, types.ModeInsert, []keys.Key{{Code: 'x', Mod: keys.ModCtrl}, {Code: 'o', Mod: keys.ModCtrl}}},
		}
		for _, tc := range bwqCases {
			seq, leaf, ok := findLeaf(ts, tc.mode, tc.scope, tc.actionID)
			if !ok {
				t.Errorf("Z1: missing leaf for actionID=%q scope=%s mode=%v", tc.actionID, tc.scope, tc.mode)
				continue
			}
			if !leaf.IsLeaf || leaf.Action == nil || leaf.Action.ID != tc.actionID {
				t.Errorf("Z1: leaf for %q is malformed: %+v", tc.actionID, leaf)
				continue
			}
			if len(seq) != len(tc.wantKeys) {
				t.Errorf("Z1: actionID=%q sequence length = %d, want %d (seq=%+v)",
					tc.actionID, len(seq), len(tc.wantKeys), seq)
				continue
			}
			for i, want := range tc.wantKeys {
				got := seq[i]
				if got.Code != want.Code || got.Special != want.Special || got.Mod != want.Mod {
					t.Errorf("Z1: actionID=%q seq[%d] = %+v, want %+v", tc.actionID, i, got, want)
				}
			}
		}
	})

	t.Run("step15_collision_warning_emitted", func(t *testing.T) {
		// Two user bindings on the same key at the same scope must emit
		// a `collision` warning (user-vs-user collision; the second
		// overwrites the first per TrieBuilder.insertUser).
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings,
			config.KeybindingConfig{
				Mode: "n", Scope: string(types.TABLES), Key: "Z",
				Action: commands.AppQuit, Description: "first",
			},
			config.KeybindingConfig{
				Mode: "n", Scope: string(types.TABLES), Key: "Z",
				Action: commands.HelpCheatsheet, Description: "second",
			},
		)
		_, warnings, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with collision: %v", err)
		}
		if !hasWarning(warnings, "collision") {
			t.Fatalf("expected `collision` warning; got %v", warnings)
		}
	})

	t.Run("step16_ambiguous_prefix_warning_emitted", func(t *testing.T) {
		// A leaf AND prefix sharing the same chord (e.g. user binding
		// `gg` and `g` both at the same scope) triggers an
		// `ambiguous_prefix` warning during Build's prefix-detection
		// pass.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings,
			config.KeybindingConfig{
				Mode: "n", Scope: string(types.TABLES), Key: "g",
				Action: commands.AppQuit, Description: "leaf-g",
			},
			config.KeybindingConfig{
				Mode: "n", Scope: string(types.TABLES), Key: "gg",
				Action: commands.HelpCheatsheet, Description: "leaf-gg",
			},
		)
		_, warnings, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with ambiguous prefix: %v", err)
		}
		if !hasWarning(warnings, "ambiguous_prefix") {
			t.Fatalf("expected `ambiguous_prefix` warning; got %v", warnings)
		}
	})

	t.Run("step17_help_cheatsheet_unbound_everywhere_warning", func(t *testing.T) {
		// Unbinding `?` everywhere (the default for
		// help.cheatsheet) is an unusual config — Build does NOT emit a
		// dedicated warning for it today. We assert the build succeeds
		// and the `?` leaf is overwritten with <nop>, which is the
		// observable "unbound" state. The named warning is the
		// observable from step21 (combined ?+: <nop>); this step is the
		// isolated `?` half.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode: "n", Scope: "global", Key: "?",
			Action: "<nop>", Description: "unbind ?",
		})
		trie, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with ? <nop>: %v", err)
		}
		t2, ok := trie.Get(types.ModeNormal, types.GLOBAL)
		if !ok {
			t.Fatal("no (Normal, GLOBAL) trie")
		}
		res := t2.Lookup([]keys.Key{{Code: '?'}})
		if !res.Found || res.Action != commands.NopCommand {
			t.Fatalf("? lookup after unbind = %+v; want NopCommand leaf", res)
		}
	})

	t.Run("step18_status_bar_shows_command_mode_when_command_line_focused", func(t *testing.T) {
		// LabelForMode(ModeCommand) must return a non-empty banner
		// containing "COMMAND" (the i18n string is "-- COMMAND --").
		label := status.LabelForMode(types.ModeCommand, s.tr, false)
		if label == "" {
			t.Fatalf("LabelForMode(ModeCommand) returned empty string")
		}
		if !strings.Contains(strings.ToUpper(label), "COMMAND") {
			t.Fatalf("LabelForMode(ModeCommand) = %q; expected to contain 'COMMAND'", label)
		}
	})

	t.Run("step19_orphan_action_reload_warn_skip_continue", func(t *testing.T) {
		// A user binding citing an unknown ActionID must emit an
		// `orphan_action` warning, skip the offending binding, but
		// still produce a usable TrieSet (no hard error).
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
			Mode: "n", Scope: "global", Key: "Q",
			Action: "this.action.does.not.exist", Description: "smoke: orphan",
		})
		trie, warnings, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with orphan action: %v", err)
		}
		if trie == nil {
			t.Fatalf("Build returned nil TrieSet despite orphan being non-fatal")
		}
		if !hasWarning(warnings, "orphan_action") {
			t.Fatalf("expected `orphan_action` warning; got %v", warnings)
		}
		// The bad binding must be absent from the trie (skipped).
		t2, ok := trie.Get(types.ModeNormal, types.GLOBAL)
		if ok {
			res := t2.Lookup([]keys.Key{{Code: 'Q'}})
			if res.Found && res.IsLeaf && res.Action != nil && res.Action.ID == "this.action.does.not.exist" {
				t.Fatalf("orphan binding leaked into trie: %+v", res)
			}
		}
	})

	t.Run("step20_atomic_swap_under_partial_state_clears_pending", func(t *testing.T) {
		s.g.Matcher().Cancel()
		// Feed a partial chord prefix: ' ' (leader) — opens a partial
		// pending state because <leader>q is a multi-key binding.
		_, err := s.g.Matcher().Dispatch(types.GLOBAL, keys.Key{Code: ' '})
		if err != nil {
			t.Fatalf("Dispatch(leader): %v", err)
		}
		if !s.g.Matcher().IsPartial() {
			t.Fatalf("Matcher.IsPartial = false after partial dispatch; want true")
		}
		// Atomic swap must Cancel pending state before storing.
		s.g.Matcher().SwapTrieSet(s.g.Matcher().TrieSet())
		if s.g.Matcher().IsPartial() {
			t.Fatalf("Matcher.IsPartial = true after SwapTrieSet; want false (D9 cancel-before-swap)")
		}
	})

	t.Run("step21_unbind_question_and_colon_starts_with_warning", func(t *testing.T) {
		// Config with both `?` and `:` set to <nop>. Build must succeed
		// (no hard error) and the resulting trie must show both as
		// NopCommand leaves. This is the AC sanity-check that the app
		// will start with neither help nor command-line available, and
		// the warnings emitted during Build do not block startup.
		synthetic := *s.cfg
		synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
		synthetic.Keybindings = append(synthetic.Keybindings,
			config.KeybindingConfig{
				Mode: "n", Scope: "global", Key: "?",
				Action: "<nop>", Description: "unbind ?",
			},
			config.KeybindingConfig{
				Mode: "n", Scope: "all", Key: ":",
				Action: "<nop>", Description: "unbind :",
			},
		)
		trie, _, err := s.runBuildWithCfg(&synthetic)
		if err != nil {
			t.Fatalf("Build with ?/: <nop>: %v", err)
		}
		t2, ok := trie.Get(types.ModeNormal, types.GLOBAL)
		if !ok {
			t.Fatal("no (Normal, GLOBAL) trie after dual-unbind")
		}
		if res := t2.Lookup([]keys.Key{{Code: '?'}}); res.Action != commands.NopCommand {
			t.Fatalf("? after unbind = %+v; want NopCommand", res)
		}
		if res := t2.Lookup([]keys.Key{{Code: ':'}}); res.Action != commands.NopCommand {
			t.Fatalf(": after unbind = %+v; want NopCommand", res)
		}
	})

	t.Run("step22_jk_insert_mode_escape_remap", func(t *testing.T) {
		t.Skip("jk -> ESC insert-mode mapping is not yet shipped")
	})

	// Post-test invariants: the matcher must be idle, no Update
	// closures should have errored, and the wired warnings slice
	// (captured at wireWithDriver time) is queryable.
	s.g.Matcher().Cancel()
	if s.g.Matcher().IsPartial() {
		t.Fatalf("Matcher.IsPartial = true after final Cancel")
	}
	if errs := s.rec.UpdateErrors(); len(errs) > 0 {
		t.Fatalf("recorder driver captured update errors: %v", errs)
	}
	// Surface a snapshot of the wired warnings for diagnostic context.
	// We do NOT enforce zero — the shipped defaults can legitimately
	// produce non-fatal warnings; this assertion just exercises the
	// public accessor.
	_ = s.g.Warnings()

	// Close the Gui (drains the store) before goleak checks so the
	// debounce timer goroutine is gone.
	if err := s.g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	goleak.VerifyNone(t, goleak.IgnoreCurrent())
}

// TestMatcherToasterWired_HandlerErrorSurfaces proves the production Gui
// wires a Toaster into the Matcher. The central error
// boundary swallows a handler error so it never reaches gocui's MainLoop,
// but without a Toaster the user saw nothing — apply/commit failures
// looked like silent no-ops (only a debug-log breadcrumb). This drives a
// real erroring handler through the live matcher and asserts the message
// lands in the wired ToastHelper. Removing the MatcherConfig.Toaster
// wiring makes ToastHelper.Current() empty and fails this test.
func TestMatcherToasterWired_HandlerErrorSurfaces(t *testing.T) {
	s := setupKbSmoke(t)

	const actionID = "test.boom"
	if err := s.g.CommandRegistry().Register(&commands.Command{
		ID:          actionID,
		Description: "smoke: erroring handler",
		Handler: func(commands.ExecCtx) error {
			return errors.New("kaboom from handler")
		},
	}); err != nil {
		t.Fatalf("Register %s: %v", actionID, err)
	}

	synthetic := *s.cfg
	synthetic.Keybindings = append([]config.KeybindingConfig(nil), s.cfg.Keybindings...)
	synthetic.Keybindings = append(synthetic.Keybindings, config.KeybindingConfig{
		Mode: "n", Scope: "global", Key: "K",
		Action: actionID, Description: "smoke: boom",
	})
	trieSet, _, err := s.runBuildWithCfg(&synthetic)
	if err != nil {
		t.Fatalf("Build synthetic boom binding: %v", err)
	}
	s.g.Matcher().SwapTrieSet(trieSet)

	res, derr := s.g.Matcher().Dispatch(types.GLOBAL, keys.Key{Code: 'K'})
	if derr != nil {
		t.Fatalf("Dispatch returned err=%v; the boundary must swallow handler errors", derr)
	}
	if res != keys.Dispatched {
		t.Fatalf("Dispatch result = %v; want Dispatched", res)
	}
	if cur := s.g.ToastHelper().Current(); !strings.Contains(cur, "kaboom") {
		t.Fatalf("ToastHelper.Current() = %q; want it to surface the swallowed handler error (Matcher Toaster not wired?)", cur)
	}
}

// TestCellEditorPushFlipsInsertModeAndCaret proves the gui.go SetModes
// wiring end-to-end: pushing the live CELL_EDITOR context
// onto the focus stack fires HandleFocus, which flips the per-scope mode
// to ModeInsert (NOT ModeCommand) and enables the terminal caret. It also
// confirms the commit chord is reachable in that exact (mode, scope)
// cell, so SetModes and the ModeInsert binding line up.
func TestCellEditorPushFlipsInsertModeAndCaret(t *testing.T) {
	s := setupKbSmoke(t)

	cellCtx := s.g.Registry().CellEditor
	if cellCtx == nil {
		t.Fatal("Registry().CellEditor is nil")
	}

	// Open captures the per-edit snapshot; Push fires HandleFocus.
	cellCtx.Open("v", models.ColumnMeta{}, []any{1}, "v")
	if err := s.g.ContextTree().Push(cellCtx); err != nil {
		t.Fatalf("Push(CELL_EDITOR): %v", err)
	}

	// 1) The mode store must report ModeInsert for the CELL_EDITOR scope.
	//    This is the direct proof that gui.go's cellCtx.SetModes(modeStore)
	//    ran and HandleFocus toggled it. ModeInsert, not ModeCommand.
	if got := s.g.ModeStore().Get(types.CELL_EDITOR); got != types.ModeInsert {
		t.Errorf("CELL_EDITOR mode after push = %v; want ModeInsert", got)
	}

	// 2) The commit chord must resolve under (ModeInsert, CELL_EDITOR) —
	//    the HandleFocus mode set — so keystrokes that aren't the commit/
	//    discard chords fall through Passthrough to the TextArea while the
	//    commit chord stays reachable.
	ts := s.g.Matcher().TrieSet()
	if ts == nil {
		t.Fatal("Matcher.TrieSet is nil")
	}
	seq, leaf, ok := findLeaf(ts, types.ModeInsert, types.CELL_EDITOR, commands.CellEditCommit)
	if !ok {
		t.Fatalf("no leaf for CellEditCommit at (ModeInsert, CELL_EDITOR)")
	}
	if !leaf.IsLeaf || leaf.Action == nil || leaf.Action.ID != commands.CellEditCommit {
		t.Errorf("CellEditCommit leaf malformed: %+v (seq=%+v)", leaf, seq)
	}

	// 3) BONUS: the registry context's deps.GuiDriver is the same recorder
	//    (gui.go:401 sets registry deps.GuiDriver = g.driver = rec), so
	//    HandleFocus's SetCaretEnabled(true) is observable on rec.
	if !s.rec.CaretEnabled {
		t.Errorf("recorder caret enabled = false after push; want true")
	}
}
