package grid

import "github.com/davesavic/dbsavvy/pkg/models"

// SetPendingEdits installs the per-View staged-edit set used by the dirty-cell
// renderer and the status indicator. Pass nil to clear. The pointer is stored
// directly (not copied) so callers that mutate the set after install see their
// changes reflected on the next Render.
func (v *View) SetPendingEdits(s *models.PendingEditSet) {
	v.mu.Lock()
	v.pendingEdits = s
	v.mu.Unlock()
}

// PendingEdits returns the currently-installed staged-edit set, or nil when
// none is installed. The returned pointer aliases the View's field; callers
// must treat it as read-only for the duration of the snapshot they consume.
func (v *View) PendingEdits() *models.PendingEditSet {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.pendingEdits
}

// HasPendingEdits reports whether the View has any staged (uncommitted) edits.
// Used by the sort flow to block a re-run while edits are pending — sorting
// re-runs the query, which would discard the staged set.
func (v *View) HasPendingEdits() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.pendingEdits != nil && !v.pendingEdits.IsEmpty()
}
