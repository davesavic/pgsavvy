package orchestrator_test

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestConnectionManagerDeleteInactive deletes a connection that is NOT the
// active session. The config file is updated and the modal list refreshes.
func TestConnectionManagerDeleteInactive(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	seed := []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost:5432/dev"},
		{Name: "staging", Driver: "postgres", DSN: "postgres://localhost:5432/staging"},
	}
	if err := config.SaveConnections(fs, path, seed); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	// Verify both rows loaded.
	if n := len(modal.Items()); n != 2 {
		t.Fatalf("modal items = %d, want 2", n)
	}

	// Select the first row ("dev").
	modal.SetCursor(0)
	conn, ok := modal.SelectedItem().(*models.Connection)
	if !ok || conn == nil || conn.Name != "dev" {
		t.Fatalf("selected = %+v, want dev", modal.SelectedItem())
	}

	// Press d → opens confirmation popup.
	if err := g.Controllers().ConnectionManager.Delete(commands.ExecCtx{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Confirm with y.
	if err := g.Controllers().Confirmation.Yes(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm Yes: %v", err)
	}

	// Verify the config file was updated.
	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("post LoadConnections: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("post: got %d rows, want 1", len(loaded))
	}
	if loaded[0].Name != "staging" {
		t.Fatalf("remaining connection = %q, want staging", loaded[0].Name)
	}

	// Verify the modal list was refreshed.
	if n := len(modal.Items()); n != 1 {
		t.Fatalf("modal items after delete = %d, want 1", n)
	}
	row, ok := modal.SelectedItem().(*models.Connection)
	if !ok || row == nil || row.Name != "staging" {
		t.Fatalf("modal row after delete = %+v, want staging", modal.SelectedItem())
	}

	// Modal stays in ModeList.
	if modal.Mode() != guicontext.ModeList {
		t.Fatalf("mode = %v after delete, want ModeList", modal.Mode())
	}
}

// TestConnectionManagerDeleteCancel presses n at the confirmation prompt. No
// change to the config file or modal list.
func TestConnectionManagerDeleteCancel(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	seed := []models.Connection{
		{Name: "dev", Driver: "postgres", DSN: "postgres://localhost:5432/dev"},
	}
	if err := config.SaveConnections(fs, path, seed); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}
	before, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	if err := g.Controllers().ConnectionManager.Delete(commands.ExecCtx{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Cancel with n.
	if err := g.Controllers().Confirmation.No(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm No: %v", err)
	}

	// Config file unchanged.
	after, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("connections.yml changed after cancel:\nbefore=%q\nafter=%q", before, after)
	}

	// Modal list unchanged.
	if n := len(modal.Items()); n != 1 {
		t.Fatalf("modal items after cancel = %d, want 1", n)
	}
}

// TestConnectionManagerDeleteEmptyListNoOp verifies that pressing d on an
// empty list is a no-op (no confirmation popup, no panic).
func TestConnectionManagerDeleteEmptyListNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	g, _ := bootstrapSaveGui(t, fs, path)
	_ = pushModal(t, g)

	// No items, d should be a no-op.
	if err := g.Controllers().ConnectionManager.Delete(commands.ExecCtx{}); err != nil {
		t.Fatalf("Delete on empty list: %v", err)
	}
}

// TestConnectionManagerDeleteRefreshesList seeds two connections, deletes one,
// and verifies the modal list reflects the remaining connection.
func TestConnectionManagerDeleteRefreshesList(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	seed := []models.Connection{
		{Name: "alpha", Driver: "postgres", DSN: "postgres://localhost:5432/alpha"},
		{Name: "beta", Driver: "postgres", DSN: "postgres://localhost:5432/beta"},
		{Name: "gamma", Driver: "postgres", DSN: "postgres://localhost:5432/gamma"},
	}
	if err := config.SaveConnections(fs, path, seed); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	// Delete the middle one ("beta").
	modal.SetCursor(1)
	if err := g.Controllers().ConnectionManager.Delete(commands.ExecCtx{}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := g.Controllers().Confirmation.Yes(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm Yes: %v", err)
	}

	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("post LoadConnections: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("post: got %d rows, want 2", len(loaded))
	}
	names := make([]string, len(loaded))
	for i := range loaded {
		names[i] = loaded[i].Name
	}
	if names[0] != "alpha" || names[1] != "gamma" {
		t.Fatalf("remaining = %v, want [alpha gamma]", names)
	}

	// Modal list reflects the change.
	if n := len(modal.Items()); n != 2 {
		t.Fatalf("modal items after delete = %d, want 2", n)
	}
}
