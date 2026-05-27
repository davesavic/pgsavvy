package keys

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
)

// ExCommand / ExRegistry hold the colon-prefixed ex-commands the user
// types into the COMMAND_LINE buffer (`:reload`, `:q`, etc.).
//
// The namespace is intentionally SEPARATE from commands.Registry: chord
// keystrokes dispatch ActionIDs (`app.quit`, `command.open`, …) while ex
// commands dispatch keywords (`reload`, …). Keeping the two tables
// disjoint means a user cannot accidentally type `:command.open` and
// trigger the chord-dispatched open path, and vice-versa.
//
// dlp.7 ships the registry itself + the `:reload` ex-command. Bootstrap
// wiring (closure binding LoadUserConfig to the on-disk path, mounting
// the registry into the gui) lands in dlp.8c.

// ErrDuplicateExCommand is returned by ExRegistry.Register when a
// command with the same name is already registered.
var ErrDuplicateExCommand = errors.New("keys: duplicate ex-command name")

// ErrInvalidExCommandName is returned when an empty name is registered.
var ErrInvalidExCommandName = errors.New("keys: invalid ex-command name")

// ErrNilExCommandHandler is returned when Handler is nil.
var ErrNilExCommandHandler = errors.New("keys: nil ex-command handler")

// ExCommand is one named ex-command. Handler receives the tokenized
// argument slice (excluding the command name itself) plus a
// commands.ExecCtx carrying the dispatching mode/scope/count/register.
type ExCommand struct {
	Name        string
	Description string
	Handler     func(args []string, exec commands.ExecCtx) error
}

// ExRegistry holds the in-process table of ex-commands. Safe for
// concurrent Register / Get calls; lifecycle is bootstrap-then-read
// (one writer at startup, many readers afterwards).
type ExRegistry struct {
	mu sync.RWMutex
	m  map[string]ExCommand
}

// NewExRegistry constructs an empty ExRegistry.
func NewExRegistry() *ExRegistry { return &ExRegistry{m: map[string]ExCommand{}} }

// Register adds cmd to the registry. Names are stored in lowercase so
// dispatch is case-insensitive (`:SET` and `:set` match the same
// handler). Returns ErrInvalidExCommandName on empty name,
// ErrNilExCommandHandler on nil handler, or ErrDuplicateExCommand
// (wrapped) if the normalised name is already taken.
func (r *ExRegistry) Register(cmd ExCommand) error {
	if cmd.Name == "" {
		return ErrInvalidExCommandName
	}
	if cmd.Handler == nil {
		return ErrNilExCommandHandler
	}
	key := strings.ToLower(cmd.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[key]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateExCommand, cmd.Name)
	}
	r.m[key] = cmd
	return nil
}

// Get returns the ExCommand registered for name, or (zero, false).
// Lookup is case-insensitive.
func (r *ExRegistry) Get(name string) (ExCommand, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.m[strings.ToLower(name)]
	return c, ok
}

// Has reports whether name is registered.
func (r *ExRegistry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List returns every registered name sorted lexicographically. Used by
// the cheatsheet generator and tests.
func (r *ExRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
