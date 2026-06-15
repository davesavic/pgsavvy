package ui

import (
	"time"

	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// tableDoubleClickTTL is the duration the table-data-editor-deferred
// toast remains visible. Long enough for an experienced user to read
// without forcing them to dismiss it manually.
const tableDoubleClickTTL = 3 * time.Second

// TablesHelper hosts the table-row "activate" stub that fires when the
// user hits <cr> or double-clicks a row in the TABLES rail. In v1 the
// inline table-data editor is deferred (DESIGN.md §17), so activation
// renders an informational toast via the supplied ToastHelper rather
// than transitioning into a real editor.
//
// Concurrency: methods run on the MainLoop. The helper carries no
// mutable state of its own — its dependencies are responsible for their
// own concurrency.
type TablesHelper struct {
	toast *ToastHelper
	tr    *i18n.TranslationSet
}

// NewTablesHelper builds a helper that delegates the deferred-toast
// render to the supplied ToastHelper. Both args are required.
func NewTablesHelper(toast *ToastHelper, tr *i18n.TranslationSet) *TablesHelper {
	return &TablesHelper{toast: toast, tr: tr}
}

// DoubleClickStub is invoked when the user activates a TABLES row. It
// pushes the i18n.TableDataEditDeferred message into the toast slot for
// 3 seconds (per AC). The table argument is accepted for future use
// (the eventual TABLE_DATA_EDITOR opening logic will need the schema +
// name); today it is ignored.
//
// Signature matches the controllers.TablesDoubleClickHelper interface
// so the helper plugs into HelperBag without an adapter.
func (h *TablesHelper) DoubleClickStub(_ *models.Table) error {
	if h.toast == nil || h.tr == nil {
		return nil
	}
	h.toast.Show(h.tr.TableDataEditDeferred, tableDoubleClickTTL)
	return nil
}
