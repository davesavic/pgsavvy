package grid

import "github.com/davesavic/dbsavvy/pkg/models"

// SetPendingEdits installs the per-View staged-edit set used by the dirty-cell
// renderer and the status indicator. Pass nil to clear. The pointer is stored
// directly (not copied) so callers that mutate the set after install see their
// changes reflected on the next Render. dbsavvy-bwq.6 (A3).
func (v *View) SetPendingEdits(s *models.PendingEditSet) {
	v.mu.Lock()
	v.pendingEdits = s
	v.mu.Unlock()
}

// PendingEdits returns the currently-installed staged-edit set, or nil when
// none is installed. The returned pointer aliases the View's field; callers
// must treat it as read-only for the duration of the snapshot they consume.
// dbsavvy-bwq.6 (A3).
func (v *View) PendingEdits() *models.PendingEditSet {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.pendingEdits
}
