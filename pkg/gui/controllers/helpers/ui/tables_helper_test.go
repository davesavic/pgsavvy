package ui_test

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestTablesDoubleClickStubShowsDeferredToast(t *testing.T) {
	toast := ui.NewToastHelper(nil)
	tr := i18n.EnglishTranslationSet()
	h := ui.NewTablesHelper(toast, tr)

	if err := h.DoubleClickStub(&models.Table{Schema: "public", Name: "users"}); err != nil {
		t.Fatalf("DoubleClickStub: %v", err)
	}
	if got := toast.Current(); got != tr.TableDataEditDeferred {
		t.Fatalf("toast = %q; want %q", got, tr.TableDataEditDeferred)
	}
}

func TestTablesDoubleClickStubNoToastSafeNoop(t *testing.T) {
	h := ui.NewTablesHelper(nil, nil)
	if err := h.DoubleClickStub(&models.Table{}); err != nil {
		t.Fatalf("DoubleClickStub: %v", err)
	}
}
