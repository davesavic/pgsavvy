package keys

import (
	"log/slog"
	"maps"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/logs"
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
	mu         sync.RWMutex
	m          map[types.ContextKey]types.Mode
	sessionLog *slog.Logger
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
	old := s.m[k]
	s.m[k] = mode
	log := s.sessionLog
	s.mu.Unlock()
	logs.Event(log, "input", "mode_set",
		slog.String("ctx", string(k)),
		slog.String("old_mode", old.String()),
		slog.String("new_mode", mode.String()),
	)
}

// Reset deletes the entry for k. No-op if k is absent.
func (s *ModeStore) Reset(k types.ContextKey) {
	s.mu.Lock()
	old := s.m[k]
	delete(s.m, k)
	log := s.sessionLog
	s.mu.Unlock()
	logs.Event(log, "input", "mode_reset",
		slog.String("ctx", string(k)),
		slog.String("old_mode", old.String()),
		slog.String("new_mode", types.ModeNormal.String()),
	)
}

// SetSessionLog installs the per-session logger used by Set/Reset to
// emit cat=input mode_set / mode_reset events. nil disables emission.
// Wired by the orchestrator at bootstrap; nil-default keeps test
// fixtures that never call this method silent.
func (s *ModeStore) SetSessionLog(l *slog.Logger) {
	s.mu.Lock()
	s.sessionLog = l
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
