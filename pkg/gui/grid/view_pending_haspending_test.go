package grid

import (
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// TestHasPendingEdits covers the three states the sort flow gates on:
// no set installed, an empty set, and a set with a staged edit.
func TestHasPendingEdits(t *testing.T) {
	v := NewView()

	if v.HasPendingEdits() {
		t.Fatalf("HasPendingEdits = true with no set installed, want false")
	}

	pe := &models.PendingEditSet{}
	v.SetPendingEdits(pe)
	if v.HasPendingEdits() {
		t.Fatalf("HasPendingEdits = true with empty set, want false")
	}

	if err := pe.Add(models.PendingEdit{
		PrimaryKey: []any{1},
		Column:     "name",
		NewValue:   "bob",
		Kind:       models.Literal,
		LoadedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !v.HasPendingEdits() {
		t.Fatalf("HasPendingEdits = false with a staged edit, want true")
	}

	pe.Clear()
	if v.HasPendingEdits() {
		t.Fatalf("HasPendingEdits = true after Clear, want false")
	}

	v.SetPendingEdits(nil)
	if v.HasPendingEdits() {
		t.Fatalf("HasPendingEdits = true after clearing the set pointer, want false")
	}
}
