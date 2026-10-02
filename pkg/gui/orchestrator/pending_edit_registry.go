package orchestrator

import (
	"sync"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// pendingEditRegistry maps (connID, catalog relation) → *models.PendingEditSet.
// Each distinct connection/relation gets its own set; repeated lookups
// for the same key return the same instance (idempotent For).
//
// Replaces the single process-wide PendingEditSet pinned on *Gui:
// result tabs for different tables / different
// connections no longer share staged-edit state, so a commit dialog
// scoped to one table cannot accidentally surface another table's
// staged rows.
type pendingEditRegistry struct {
	mu   sync.Mutex
	sets map[pendingEditRegKey]*models.PendingEditSet
}

type pendingEditRegKey struct {
	connID string
	ref    models.Ref
}

func newPendingEditRegistry() *pendingEditRegistry {
	return &pendingEditRegistry{
		sets: make(map[pendingEditRegKey]*models.PendingEditSet),
	}
}

// For returns the PendingEditSet for (connID, ref), creating a
// fresh empty set on first lookup. Returns nil when either key
// component is empty — callers treat nil as "no active editable target"
// and surface a disabled-reason rather than staging into a sentinel.
func (r *pendingEditRegistry) For(connID string, ref models.Ref) *models.PendingEditSet {
	if connID == "" || ref.Schema == "" || ref.Table == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := pendingEditRegKey{connID: connID, ref: ref}
	if s, ok := r.sets[k]; ok {
		return s
	}
	s := &models.PendingEditSet{Table: ref}
	r.sets[k] = s
	return s
}

// Lookup returns the existing set for (connID, ref) without
// allocating one. Returns nil when no set has been touched for that
// key yet. Used by read-only callers (status indicator, dirty-cell
// render) that should not materialise empty sets just by asking.
func (r *pendingEditRegistry) Lookup(connID string, ref models.Ref) *models.PendingEditSet {
	if connID == "" || ref.Schema == "" || ref.Table == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sets[pendingEditRegKey{connID: connID, ref: ref}]
}

// All returns a snapshot of every materialised PendingEditSet across all
// connection/relation keys. Order is unspecified. Drives the cross-table
// `<leader>cU` discard-all flow; sets never touched (no edit staged) are
// absent because For only allocates on demand.
func (r *pendingEditRegistry) All() []*models.PendingEditSet {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*models.PendingEditSet, 0, len(r.sets))
	for _, s := range r.sets {
		out = append(out, s)
	}
	return out
}

// pendingEditCount sums the staged-edit count across every table in the
// registry. Drives the status-bar "[N pending]" indicator. Returns 0 when
// the registry is unwired (bootstrap safety).
func (g *Gui) pendingEditCount() int {
	if g.pendingEditReg == nil {
		return 0
	}
	n := 0
	for _, s := range g.pendingEditReg.All() {
		if s != nil {
			n += s.Count()
		}
	}
	return n
}
