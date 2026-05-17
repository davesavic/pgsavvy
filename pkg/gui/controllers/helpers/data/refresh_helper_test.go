package data_test

import (
	"context"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
)

// Nil-connect tests confirm the controller-friendly contract: a
// RefreshHelper bound to a nil ConnectHelper is a silent no-op so
// early-boot wiring order does not panic the controllers.

func TestRefreshNilConnectIsNoop(t *testing.T) {
	h := data.NewRefreshHelper(nil)
	if err := h.RefreshSchemas(context.Background()); err != nil {
		t.Errorf("RefreshSchemas: %v", err)
	}
	if err := h.RefreshTables(context.Background(), "public"); err != nil {
		t.Errorf("RefreshTables: %v", err)
	}
	if err := h.RefreshColumns(context.Background(), "public", "users"); err != nil {
		t.Errorf("RefreshColumns: %v", err)
	}
	if err := h.RefreshIndexes(context.Background(), "public", "users"); err != nil {
		t.Errorf("RefreshIndexes: %v", err)
	}
}
