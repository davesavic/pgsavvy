package session

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// CancelRegistry maps an active session to its in-flight RunHandle so a
// caller holding only a models.SessionID can locate the currently-running
// stream and cancel it. Keyed by SessionID because the SQLSession queue
// guarantees at most one run in flight per session at any moment. All
// methods are safe for concurrent use.
type CancelRegistry struct {
	mu  sync.RWMutex
	run map[models.SessionID]*RunHandle
}

// NewCancelRegistry returns an empty registry.
func NewCancelRegistry() *CancelRegistry {
	return &CancelRegistry{run: make(map[models.SessionID]*RunHandle)}
}

// Register associates rh with sid. A subsequent Register for the same sid
// overwrites the prior entry — callers should Unregister before Register-ing
// a successor.
func (r *CancelRegistry) Register(sid models.SessionID, rh *RunHandle) {
	r.mu.Lock()
	r.run[sid] = rh
	r.mu.Unlock()
}

// Lookup returns the RunHandle currently registered for sid, or (nil, false).
func (r *CancelRegistry) Lookup(sid models.SessionID) (*RunHandle, bool) {
	r.mu.RLock()
	rh, ok := r.run[sid]
	r.mu.RUnlock()
	return rh, ok
}

// Unregister removes any RunHandle registered for sid. Safe to call when sid
// has no current entry.
func (r *CancelRegistry) Unregister(sid models.SessionID) {
	r.mu.Lock()
	delete(r.run, sid)
	r.mu.Unlock()
}
