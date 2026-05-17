package drivers

import (
	"fmt"
	"sort"
	"sync"
)

var (
	registryMu sync.RWMutex
	registry   = make(map[string]Factory)
)

// Register adds factory under name. It panics on an empty name, a nil
// factory, or a duplicate name — registration is a programmer-controlled
// process-startup step (epic dbsavvy-921 D9), so misuse is a bug and not a
// runtime error.
func Register(name string, factory Factory) {
	if name == "" {
		panic("drivers: Register called with empty name")
	}
	if factory == nil {
		panic(fmt.Sprintf("drivers: Register called with nil factory for %q", name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("drivers: driver %q already registered", name))
	}
	registry[name] = factory
}

// Get returns the factory registered under name. If no driver is registered
// the error wraps ErrUnknownDriver so callers can use errors.Is.
func Get(name string) (Factory, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, name)
	}
	return f, nil
}

// Names returns an alphabetically sorted snapshot of registered driver names.
// The returned slice is safe to mutate; subsequent registrations do not
// affect it.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// resetRegistry clears the global registry. Test-only.
func resetRegistry() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = make(map[string]Factory)
}
