package keys

import (
	"maps"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ModeStore is the canonical per-context modal-state store. It maps a
// types.ContextKey to the types.Mode currently active for that context;
// unknown keys default to types.ModeNormal without inserting an entry.
//
// Concurrency: all internal state lives behind a sync.RWMutex. In
// production all calls happen on the gocui MainLoop, but the mutex keeps
// the store safe for test goroutines (the AC suite races concurrent
// Set/Get calls under -race).
type ModeStore struct {
	mu sync.RWMutex
	m  map[types.ContextKey]types.Mode
}

// NewModeStore returns a ModeStore with a non-nil backing map.
func NewModeStore() *ModeStore {
	return &ModeStore{
		m: make(map[types.ContextKey]types.Mode),
	}
}

// Get returns the Mode recorded for k, or types.ModeNormal if no entry
// exists. Get never inserts an entry for an unknown key.
func (s *ModeStore) Get(k types.ContextKey) types.Mode {
	s.mu.RLock()
	mode, ok := s.m[k]
	s.mu.RUnlock()
	if !ok {
		return types.ModeNormal
	}
	return mode
}

// Set records mode for k. Setting ModeNormal stores the zero sentinel
// rather than deleting the entry — callers that want to drop the entry
// should call Reset.
func (s *ModeStore) Set(k types.ContextKey, mode types.Mode) {
	s.mu.Lock()
	s.m[k] = mode
	s.mu.Unlock()
}

// Reset deletes the entry for k. No-op if k is absent.
func (s *ModeStore) Reset(k types.ContextKey) {
	s.mu.Lock()
	delete(s.m, k)
	s.mu.Unlock()
}

// All returns a defensive deep copy of the current entries. Intended for
// tests; mutating the returned map does not affect subsequent Get calls.
func (s *ModeStore) All() map[types.ContextKey]types.Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[types.ContextKey]types.Mode, len(s.m))
	maps.Copy(out, s.m)
	return out
}
