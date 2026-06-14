package keys

import "sync"

// RegisterStore is the mutex-protected map[rune]string that backs vim
// registers. This ships only the storage primitive (Get/Set); the
// `q{reg}` / `@{reg}` record + replay flow ships in E9.
//
// Concurrency: every method takes the internal mutex. In production the
// store is touched from the gocui MainLoop only, but the mutex keeps it
// safe for the AC suite (which races register reads/writes against
// macro fixtures).
type RegisterStore struct {
	mu sync.Mutex
	m  map[rune]string
}

// NewRegisterStore returns an empty store with a non-nil backing map.
func NewRegisterStore() *RegisterStore {
	return &RegisterStore{m: make(map[rune]string)}
}

// Get returns the contents of register r, or "" if the register is
// unset.
func (s *RegisterStore) Get(r rune) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[r]
}

// Set writes v into register r. Setting a register to "" is allowed and
// stores the empty string (rather than deleting the entry).
func (s *RegisterStore) Set(r rune, v string) {
	s.mu.Lock()
	s.m[r] = v
	s.mu.Unlock()
}
