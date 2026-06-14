package keys

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
)

// ToastFunc is the minimal toast surface :reload uses. The orchestrator
// binds it to ui.ToastHelper; tests substitute a recording
// spy. nil is treated as "no toast".
type ToastFunc func(message string)

// MatcherSwapper is the minimal subset of *Matcher that :reload calls.
// *Matcher satisfies this structurally; tests use a fake to assert that
// SwapTrieSet is (or is not) invoked.
type MatcherSwapper interface {
	SwapTrieSet(t *TrieSet)
}

// BuildService is the minimal subset of *KeybindingService that :reload
// calls. *KeybindingService satisfies it structurally; tests substitute
// a stub that returns canned (trie, warnings, err) tuples or panics.
type BuildService interface {
	Build(
		defaults []*ChordBinding,
		cfg *config.UserConfig,
		registry *commands.Registry,
		kindOf ContextKindLookup,
	) (*TrieSet, []Warning, error)
}

// ReloadDeps groups the dependencies the :reload ex-command needs.
// Bootstrap supplies concrete closures + the live service /
// matcher / registry / defaults; tests inject fakes.
type ReloadDeps struct {
	// LoadUserConfig reloads the user config from its canonical path.
	// The closure is bound at bootstrap to the fs+path tuple so the
	// ex-command needs no path arg. Extra `:reload <tokens>` are
	// dropped before reaching the closure (security: prevents
	// `:reload /etc/passwd` from loading arbitrary files).
	LoadUserConfig func() (*config.UserConfig, error)

	// Defaults are the controller-published shipped bindings (D8).
	// Bootstrap populates this from AllDefaultBindings().
	Defaults []*ChordBinding

	// Registry is the commands.Registry used to resolve action IDs.
	Registry *commands.Registry

	// KindOf classifies a ContextKey for `scope: all` expansion.
	KindOf ContextKindLookup

	// Service runs Build. Interface (not *KeybindingService) so tests
	// can substitute a panicking / failing fake.
	Service BuildService

	// Matcher receives the new TrieSet via SwapTrieSet.
	// Interface (not *Matcher) so tests can substitute a recording spy.
	Matcher MatcherSwapper

	// Toaster surfaces success / warning / error messages to the user.
	// Optional — nil means no toast.
	Toaster ToastFunc

	// Log surfaces warnings (orphan_action, etc.) per Build. Optional.
	Log DebugLogger
}

// ReloadCommand returns the `:reload` ex-command bound to deps.
//
// Concurrency: handler invocations serialise behind an internal mutex.
// If multiple :reload submissions queue up while one is in flight, only
// the LAST submitted invocation does work — every earlier queued
// invocation emits "reload superseded" via the Toaster and returns. The
// generation counter (atomic.Uint64) plus the runMu mutex implement
// the "latest submitted wins" semantics from the AC.
//
// Build panics are recovered → error toast → old trie is preserved
// (SwapTrieSet is NOT called). LoadUserConfig errors short-circuit
// before Build runs.
//
// Extra args (`:reload foo bar`) are silently dropped — the bootstrap-
// bound LoadUserConfig closure takes no arguments, so arbitrary file
// paths cannot leak through.
func ReloadCommand(deps ReloadDeps) ExCommand {
	var (
		runMu     sync.Mutex
		latestGen atomic.Uint64
	)
	handler := func(_ []string, _ commands.ExecCtx) error {
		// Bump the generation counter then wait for the per-handler
		// serialisation mutex. If a NEWER :reload has been queued
		// behind us by the time we acquire runMu, skip our work — the
		// newer invocation will reload with fresher inputs.
		myGen := latestGen.Add(1)
		runMu.Lock()
		defer runMu.Unlock()
		if latestGen.Load() != myGen {
			if deps.Toaster != nil {
				deps.Toaster("reload superseded")
			}
			return nil
		}

		// Step 1: load the user config from disk.
		cfg, err := deps.LoadUserConfig()
		if err != nil {
			if deps.Toaster != nil {
				deps.Toaster("reload failed: " + err.Error())
			}
			return nil
		}

		// Step 2: run Build under a panic guard. A panicking Build
		// must NOT crash the dispatch goroutine; the old trie stays.
		var (
			newTrie  *TrieSet
			warnings []Warning
			buildErr error
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					buildErr = fmt.Errorf("build panic: %v", r)
				}
			}()
			newTrie, warnings, buildErr = deps.Service.Build(
				deps.Defaults, cfg, deps.Registry, deps.KindOf,
			)
		}()
		if buildErr != nil {
			if deps.Toaster != nil {
				deps.Toaster("reload failed: " + buildErr.Error())
			}
			return nil
		}

		// Step 3: log warnings, swap, toast success.
		if deps.Log != nil {
			for _, w := range warnings {
				deps.Log.Debug(fmt.Sprintf(
					"reload: %s warning [%s] %s (origin=%s)",
					w.Level, w.Code, w.Message, w.Origin,
				))
			}
		}
		deps.Matcher.SwapTrieSet(newTrie)
		if deps.Toaster != nil {
			if len(warnings) > 0 {
				deps.Toaster(fmt.Sprintf("config reloaded (%d warning(s))", len(warnings)))
			} else {
				deps.Toaster("config reloaded")
			}
		}
		return nil
	}

	return ExCommand{
		Name:        "reload",
		Description: "reload user config",
		Handler:     handler,
	}
}
