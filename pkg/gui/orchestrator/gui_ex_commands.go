package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// toaster surfaces a transient message via the live toast helper.
// nil-safe: a no-op until g.toastHelp is constructed in wireWithDriver.
func (g *Gui) toaster(msg string) {
	if g.toastHelp != nil {
		g.toastHelp.Show(msg, 3*time.Second)
	}
}

// caret toggles the terminal caret (insert-mode cursor) via the driver.
func (g *Gui) caret(enabled bool) {
	if g.driver != nil {
		g.driver.SetCaretEnabled(enabled)
	}
}

// kindOf classifies a ContextKey by walking the registry; used by
// Build to expand `scope: all` and by :reload.
func (g *Gui) kindOf(k types.ContextKey) types.ContextKind {
	for _, ctx := range g.registry.Flatten() {
		if ctx != nil && ctx.GetKey() == k {
			return ctx.GetKind()
		}
	}
	return types.GLOBAL_CONTEXT
}

// recognisedSettings are the settings whose successful SET updates the
// SettingsSnapshot / AppState. Read by both :set and :reset.
//
//	search_path, role, time zone / timezone, application_name, statement_timeout.
var recognisedSettings = map[string]bool{
	"search_path":       true,
	"role":              true,
	"time":              true, // SET TIME ZONE — "time" is args[0], "zone" is args[1]
	"timezone":          true,
	"application_name":  true,
	"statement_timeout": true,
}

// registerReloadEx builds the :reload ex-command and registers it.
//
// LoadUserConfig reads config.yml from disk via config.LoadUserConfig
// using the UserConfigPath plumbed through Deps. ValidateUserConfig
// runs against the live command registry and context registry before
// Build. After a successful Build, StoreConfig atomically swaps the
// Common.UserConfig pointer and ApplyTheme reconciles the theme
// colours with theme.ApplyUserConfig.
//
// defaults and svc are wireWithDriver locals (built from
// controllers.AllDefaultBindings + keys.NewKeybindingService) so they are
// passed in. g.keybindingSystem.matcher == matcher (assigned earlier in wireWithDriver),
// so the live matcher is referenced via g.keybindingSystem.matcher.
func (g *Gui) registerReloadEx(defaults []*types.ChordBinding, svc *keys.KeybindingService) error {
	reloadDeps := keys.ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			return config.LoadUserConfig(g.deps.Common.Fs, []string{g.deps.UserConfigPath})
		},
		Defaults: defaults,
		Registry: g.keybindingSystem.cmdRegistry,
		KindOf:   g.kindOf,
		Service:  svc,
		Matcher:  g.keybindingSystem.matcher,
		Toaster:  g.toaster,
		Log:      g.deps.Common.Logger(),
		ValidateDeps: config.ValidationDeps{
			ActionExists: func(id string) bool { return g.keybindingSystem.cmdRegistry.Has(id) },
			ScopeExists:  g.scopeExistsPredicate(),
		},
		StoreConfig: func(cfg *config.UserConfig) {
			g.deps.Common.UserConfig.Store(cfg)
		},
		ApplyTheme: func(tc *config.ThemeConfig) {
			warns := theme.ApplyUserConfig(tc)
			for _, w := range warns {
				g.toaster("theme warning: " + w)
			}
		},
	}
	return g.keybindingSystem.exRegistry.Register(keys.ReloadCommand(reloadDeps))
}

// handleQuitEx is the :q / :quit handler. Return gocui.ErrQuit so the
// submit dispatcher can propagate it up through the gocui main loop.
// CommandSubmitCommand recognises ErrQuit specifically and skips its
// default toast-and-swallow path.
//
// `:q` consults the PendingDiscardHelper before returning
// ErrQuit. When the PendingEditSet is non-empty the guard surfaces an
// instructional toast (`:w` / `:q!` / `<leader>cU`) and the quit is
// aborted (return nil so submit doesn't propagate ErrQuit). `:q!` bypasses
// the guard; `:w` opens the commit dialog.
func (g *Gui) handleQuitEx(_ []string, _ commands.ExecCtx) error {
	// Delegate to the AppQuit command handler so :q, <leader>q,
	// and <c-c> share the same guard chain (pending-edit +
	// open-tx checks). The handler is registered by
	// QuitController.RegisterActions.
	if g.keybindingSystem.cmdRegistry != nil {
		if cmd, ok := g.keybindingSystem.cmdRegistry.Get(commands.AppQuit); ok && cmd != nil && cmd.Handler != nil {
			return cmd.Handler(commands.ExecCtx{})
		}
	}
	return gocui.ErrQuit
}

// handleForceQuitEx is the :q! handler. It bypasses the pending-edit guard.
func (g *Gui) handleForceQuitEx(_ []string, _ commands.ExecCtx) error {
	return gocui.ErrQuit
}

// handleWriteEx is the :w handler; it opens the commit dialog.
func (g *Gui) handleWriteEx(_ []string, _ commands.ExecCtx) error {
	if g.keybindingSystem.cmdRegistry == nil {
		return nil
	}
	cmd, ok := g.keybindingSystem.cmdRegistry.Get(commands.CommitDialogOpen)
	if !ok || cmd == nil || cmd.Handler == nil {
		return nil
	}
	return cmd.Handler(commands.ExecCtx{})
}

// handleSetEx is the :set handler. It executes SET on the live SQL
// session, updates SettingsSnapshot, persists to
// AppState.LastSessionSettings, and (for search_path) refreshes the schema
// rail. Unrecognised settings pass through to normal SQL execution.
func (g *Gui) handleSetEx(args []string, _ commands.ExecCtx) error {
	if len(args) == 0 {
		g.toaster("SET requires a setting name")
		return nil
	}
	sess := g.queryState.activeSQLSession
	if sess == nil {
		g.toaster("no active session")
		return nil
	}

	// Reconstruct the full SQL from the tokens. The ex-line
	// already split on whitespace so we rejoin.
	fullSQL := "SET " + strings.Join(args, " ")

	// Determine the canonical setting key and the value portion.
	settingKey := strings.ToLower(args[0])
	var settingValue string

	// Handle "SET TIME ZONE ..." — two-word setting name.
	if settingKey == "time" && len(args) >= 2 && strings.EqualFold(args[1], "zone") {
		settingKey = "timezone"
		// Value is everything after "TIME ZONE" (skip the TO/= if present).
		valStart := 2
		if len(args) > 3 && (strings.EqualFold(args[2], "to") || args[2] == "=") {
			valStart = 3
		}
		if valStart < len(args) {
			settingValue = strings.Join(args[valStart:], " ")
		}
	} else if len(args) >= 2 {
		// Normal form: SET <key> TO <value> or SET <key> = <value>.
		valStart := 1
		if len(args) > 2 && (strings.EqualFold(args[1], "to") || args[1] == "=") {
			valStart = 2
		}
		settingValue = strings.Join(args[valStart:], " ")
	}

	isRecognised := recognisedSettings[settingKey]

	// AD-7: execute on worker, never block UI thread.
	g.OnWorker(func(_ gocui.Task) error {
		_, err := sess.Execute(context.Background(), models.Query{SQL: fullSQL})
		if err != nil {
			g.toaster(fmt.Sprintf("SET failed: %s", err))
			return nil
		}

		if isRecognised {
			g.persistTrackedSetting(context.Background(), settingKey, settingValue)
		}

		g.toaster(fmt.Sprintf("OK: %s", fullSQL))
		return nil
	})
	return nil
}

// handleResetEx is the :reset handler. It executes RESET on the live SQL
// session and reverts the tracked setting to the server default.
func (g *Gui) handleResetEx(args []string, _ commands.ExecCtx) error {
	if len(args) == 0 {
		g.toaster("RESET requires a setting name")
		return nil
	}
	sess := g.queryState.activeSQLSession
	if sess == nil {
		g.toaster("no active session")
		return nil
	}

	fullSQL := "RESET " + strings.Join(args, " ")
	settingKey := strings.ToLower(args[0])

	g.OnWorker(func(_ gocui.Task) error {
		_, err := sess.Execute(context.Background(), models.Query{SQL: fullSQL})
		if err != nil {
			g.toaster(fmt.Sprintf("RESET failed: %s", err))
			return nil
		}

		// Delete the key from the snapshot so it reverts to the
		// server default.
		if recognisedSettings[settingKey] {
			sess.SettingsSnapshot().Delete(settingKey)

			connID := g.connectionState.activeConnID
			if connID != "" && g.deps.Store != nil {
				g.deps.Store.MutateAndSave(func(a *common.AppState) {
					if a.LastSessionSettings == nil {
						return
					}
					inner := a.LastSessionSettings[connID]
					if inner == nil {
						return
					}
					delete(inner, settingKey)
				})
			}

			if (settingKey == "role" || settingKey == "search_path") && g.refreshHelper != nil {
				_ = g.refreshHelper.RefreshSchemas(context.Background())
			}
		}

		g.toaster(fmt.Sprintf("OK: %s", fullSQL))
		return nil
	})
	return nil
}

// handleCrossDBEx is the :c handler; cross-database attach is unsupported.
func (g *Gui) handleCrossDBEx(_ []string, _ commands.ExecCtx) error {
	g.toaster("cross-database attach not supported — create a separate connection profile")
	return nil
}

// handleShowTipEx is the :tip handler. It re-displays the first-run welcome
// tip on demand (e.g. to re-check it after it's been dismissed), independent
// of the startup gate (the seen-stamp / zero-connections predicate only gates
// the automatic boot-time push). The command line is popped first so
// dismissing the tip returns to the underlying context instead of a stranded
// command line. No-op if the tip is already on top.
func (g *Gui) handleShowTipEx(_ []string, _ commands.ExecCtx) error {
	if g.registry == nil || g.registry.FirstRunTip == nil || g.tree == nil {
		return nil
	}
	_ = g.tree.PopIfTop(types.COMMAND_LINE)
	if top := g.tree.Current(); top != nil && top.GetKey() == types.FIRST_RUN_TIP {
		return nil
	}
	return g.tree.Push(g.registry.FirstRunTip)
}

// handleSettingsEx is the :settings handler. It pushes the SETTINGS modal
// onto the focus stack, popping the command line first so dismissing the
// modal returns to the underlying context. No-op when already on top.
func (g *Gui) handleSettingsEx(_ []string, _ commands.ExecCtx) error {
	if g.registry == nil || g.registry.Settings == nil || g.tree == nil {
		return nil
	}
	_ = g.tree.PopIfTop(types.COMMAND_LINE)
	return g.tree.Push(g.registry.Settings)
}
