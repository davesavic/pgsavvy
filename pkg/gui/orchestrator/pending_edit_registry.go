package orchestrator

import (
	"sync"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// pendingEditRegistry maps (connID, baseTable) → *models.PendingEditSet.
// Each distinct (connID, baseTable) gets its own set; repeated lookups
// for the same key return the same instance (idempotent For).
//
// Replaces the single process-wide PendingEditSet that dbsavvy-bwq.py4
// pinned on *Gui: result tabs for different tables / different
// connections no longer share staged-edit state, so a commit dialog
// scoped to one table cannot accidentally surface another table's
// staged rows (dbsavvy-8oo stub #10).
type pendingEditRegistry struct {
	mu   sync.Mutex
	sets map[pendingEditRegKey]*models.PendingEditSet
}

type pendingEditRegKey struct {
	connID    string
	baseTable string
}

func newPendingEditRegistry() *pendingEditRegistry {
	return &pendingEditRegistry{
		sets: make(map[pendingEditRegKey]*models.PendingEditSet),
	}
}

// For returns the PendingEditSet for (connID, baseTable), creating a
// fresh empty set on first lookup. Returns nil when either key
// component is empty — callers treat nil as "no active editable target"
// and surface a disabled-reason rather than staging into a sentinel.
func (r *pendingEditRegistry) For(connID, baseTable string) *models.PendingEditSet {
	if connID == "" || baseTable == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := pendingEditRegKey{connID: connID, baseTable: baseTable}
	if s, ok := r.sets[k]; ok {
		return s
	}
	s := &models.PendingEditSet{Table: refFromBaseTable(baseTable)}
	r.sets[k] = s
	return s
}

// Lookup returns the existing set for (connID, baseTable) without
// allocating one. Returns nil when no set has been touched for that
// key yet. Used by read-only callers (status indicator, dirty-cell
// render) that should not materialise empty sets just by asking.
func (r *pendingEditRegistry) Lookup(connID, baseTable string) *models.PendingEditSet {
	if connID == "" || baseTable == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sets[pendingEditRegKey{connID: connID, baseTable: baseTable}]
}

// refFromBaseTable parses a "schema.table" identifier into a models.Ref.
// query.ResultIdentity.BaseTable carries schema-qualified names; a bare
// identifier (no dot) lands in Table with an empty Schema.
func refFromBaseTable(s string) models.Ref {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return models.Ref{Schema: s[:i], Table: s[i+1:]}
		}
	}
	return models.Ref{Table: s}
}
