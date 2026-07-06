package orchestrator_test

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

// TestSettingsExCommandRegistered verifies the :settings ex-command is
// registered after wireWithDriver.
func TestSettingsExCommandRegistered(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	exReg := g.ExRegistry()
	if exReg == nil {
		t.Fatal("ExRegistry is nil after wireWithDriver")
	}

	cmd, ok := exReg.Get("settings")
	if !ok {
		t.Fatal(":settings ex-command not registered")
	}
	if cmd.Handler == nil {
		t.Fatal(":settings handler is nil")
	}
}

// TestSettingsOpenBindingGLOBAL verifies <leader>os is a GLOBAL-scoped
// binding that maps to settings.open.
func TestSettingsOpenBindingGLOBAL(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	ts := g.Matcher().TrieSet()
	if ts == nil {
		t.Fatal("TrieSet is nil")
	}

	trie, ok := ts.Get(types.ModeNormal, types.GLOBAL)
	if !ok || trie == nil {
		t.Fatal("no (Normal, GLOBAL) trie")
	}

	seq := []keys.Key{{Code: ' '}, {Code: 'o'}, {Code: 's'}}
	res := trie.Lookup(seq)
	if !res.Found || !res.IsLeaf || res.Action == nil || res.Action.ID != commands.SettingsOpen {
		t.Fatalf("<leader>os lookup = %+v; want leaf with action %q", res, commands.SettingsOpen)
	}
}

// TestSettingsExCommandPushContext verifies that executing the :settings
// ex-command pushes SETTINGS onto the context tree.
func TestSettingsExCommandPushContext(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	exReg := g.ExRegistry()
	ctxTree := g.ContextTree()
	registry := g.Registry()

	if ctxTree == nil || registry == nil || registry.Settings == nil {
		t.Skip("ContextTree / Settings registry not available")
	}

	cmd, ok := exReg.Get("settings")
	if !ok {
		t.Fatal(":settings ex-command not registered")
	}

	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf(":settings handler: %v", err)
	}

	stack := ctxTree.Stack()
	found := false
	for _, c := range stack {
		if c.GetKey() == types.SETTINGS {
			found = true
			break
		}
	}
	if !found {
		t.Error("SETTINGS not on context stack after :settings")
	}
}

// TestSettingsRegistryExists verifies SettingsContext is wired into the registry.
func TestSettingsRegistryExists(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	reg := g.Registry()
	if reg == nil {
		t.Fatal("Registry is nil")
	}
	if reg.Settings == nil {
		t.Fatal("SettingsContext not registered")
	}
}

// TestSettingsControllerAttached verifies that the SettingsController
// is created and wired by buildTestGui.
func TestSettingsControllerAttached(t *testing.T) {
	g, _ := buildTestGui(t)
	t.Cleanup(func() { _ = g.Close() })

	ctls := g.Controllers()
	if ctls == nil {
		t.Fatal("Controllers is nil")
	}
	if ctls.Settings == nil {
		t.Fatal("SettingsController not attached")
	}
}
