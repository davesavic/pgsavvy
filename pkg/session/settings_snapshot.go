package session

import (
	"maps"
	"sync"
)

type SettingsSnapshot struct {
	mu sync.RWMutex
	m  map[string]string
}

func NewSettingsSnapshot() *SettingsSnapshot {
	return &SettingsSnapshot{m: make(map[string]string)}
}

func (s *SettingsSnapshot) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[key]
	return v, ok
}

func (s *SettingsSnapshot) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
}

func (s *SettingsSnapshot) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

func (s *SettingsSnapshot) All() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.m))
	maps.Copy(out, s.m)
	return out
}
