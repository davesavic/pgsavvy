package commands

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrDuplicateAction is returned by Registry.Register when a Command
// with the same ID is already registered. The first registration wins;
// the second is rejected without mutating the Registry.
var ErrDuplicateAction = errors.New("commands: duplicate action ID")

// ErrInvalidActionID is returned by Registry.Register when the supplied
// Command has an empty ID. Empty IDs are unusable (config validation
// would never match them) and almost always indicate a typo at the
// call site.
var ErrInvalidActionID = errors.New("commands: invalid action ID")

// ErrNilCommand is returned by Registry.Register when the supplied
// *Command is nil. Distinct from ErrInvalidActionID so callers can
// diagnose programming errors precisely.
var ErrNilCommand = errors.New("commands: nil command")

// ErrNilHandler is returned by Registry.Register when the supplied
// Command has no Handler. Every Command must dispatch to something
// (including NopSentinel for explicit unbinds).
var ErrNilHandler = errors.New("commands: nil handler")

// Registry is the in-process table of all named actions.
//
// The lifecycle is bootstrap-then-read: every Register call happens
// during startup (and during `:reload`), and Get/Has/All/Len are
// called concurrently afterwards. The RWMutex permits many concurrent
// readers and serialises writers, so post-bootstrap reads are
// effectively lock-free contention-wise.
type Registry struct {
	mu    sync.RWMutex
	items map[string]*Command
}

// NewRegistry constructs an empty Registry ready for Register calls.
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]*Command)}
}

// Register adds cmd to the Registry. Returns:
//
//   - ErrNilCommand        if cmd is nil
//   - ErrInvalidActionID   if cmd.ID == ""
//   - ErrNilHandler        if cmd.Handler == nil
//   - ErrDuplicateAction   if cmd.ID is already registered
//     (wrapped via fmt.Errorf so callers can Is-match the sentinel
//     and still see the offending ID in the message)
//
// On any error the Registry is unchanged.
func (r *Registry) Register(cmd *Command) error {
	if cmd == nil {
		return ErrNilCommand
	}
	if cmd.ID == "" {
		return ErrInvalidActionID
	}
	if cmd.Handler == nil {
		return ErrNilHandler
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.items[cmd.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateAction, cmd.ID)
	}
	r.items[cmd.ID] = cmd
	return nil
}

// Get returns the Command registered for id, or (nil, false) if no
// such ID exists. Never panics, even with the zero-value Registry —
// but always construct via NewRegistry to keep the map non-nil.
func (r *Registry) Get(id string) (*Command, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cmd, ok := r.items[id]
	return cmd, ok
}

// Has reports whether id is registered.
func (r *Registry) Has(id string) bool {
	_, ok := r.Get(id)
	return ok
}

// Len returns the number of registered commands.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

// All returns every registered command sorted by ID. The returned
// slice is a fresh copy; mutating it does not affect the Registry.
// The Command pointers inside are the live registry entries — do not
// mutate their fields.
func (r *Registry) All() []*Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*Command, 0, len(r.items))
	for _, cmd := range r.items {
		out = append(out, cmd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
