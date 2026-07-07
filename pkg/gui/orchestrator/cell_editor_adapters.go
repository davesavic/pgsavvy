package orchestrator

import (
	"fmt"
	"reflect"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// cellEditorPicker satisfies controllers.GridStatePicker by resolving
// the active result tab on every call. The active tab is mutable, so
// capturing a snapshot at wire time would dangle once the user switches
// tabs.
//
// Read-only / inline-edit / per-relation editability are already folded
// into grid.View by F2's introspection pass (drivers/pg/editability.go
// ApplyConnectionGate), so IsReadOnly and SupportsInlineEdit return
// neutral values here — the !Editable / DisabledReason gate surfaces
// the canonical reason. StreamBlocksEdit is reported live from TabState
// because the tab's lifecycle phase can still change after wire time.
type cellEditorPicker struct{ tabs *ui.ResultTabsHelper }

func (p cellEditorPicker) Editable() bool {
	g := p.activeGrid()
	if g == nil {
		return false
	}
	return g.Editable()
}

func (p cellEditorPicker) StreamBlocksEdit() bool {
	tab := p.activeTab()
	if tab == nil {
		return false
	}
	return streamBlocksEdit(tab.State())
}

// streamBlocksEdit reports whether a tab's lifecycle phase has no stable
// buffer to edit against. StateRunning is editable: rows are buffered,
// appends are append-only on the UI thread, and pending edits are
// PK-keyed (not row-index keyed), so editing a buffered row is safe
// while more rows still stream in. Only the phases with no usable buffer
// block edits — StateQueued (no rows opened yet) and StateSorting (a
// re-run cleared the buffer and reset the cursor; it flips to
// StateRunning on the first appended batch).
func streamBlocksEdit(state ui.TabState) bool {
	switch state {
	case ui.StateQueued, ui.StateSorting:
		return true
	default:
		return false
	}
}

func (p cellEditorPicker) SupportsInlineEdit() bool { return true }

func (p cellEditorPicker) IsReadOnly() bool { return false }

func (p cellEditorPicker) DisabledReason() string {
	g := p.activeGrid()
	if g == nil {
		return "no active result grid"
	}
	return g.DisabledReason()
}

func (p cellEditorPicker) CellSnapshot() (any, models.ColumnMeta, []any, bool) {
	g := p.activeGrid()
	if g == nil {
		return nil, models.ColumnMeta{}, nil, false
	}
	row, col := g.CursorPosition()
	cols := g.Columns()
	if col < 0 || col >= len(cols) {
		return nil, models.ColumnMeta{}, nil, false
	}
	rows := g.AllRows()
	if row < 0 || row >= len(rows) {
		return nil, models.ColumnMeta{}, nil, false
	}
	values := rows[row].Values
	if col >= len(values) {
		return nil, models.ColumnMeta{}, nil, false
	}
	ri := g.RowIdentity()
	if len(ri) == 0 {
		return values[col], cols[col], nil, true
	}
	pk := make([]any, len(ri))
	for i, idx := range ri {
		if idx < 0 || idx >= len(values) {
			return nil, models.ColumnMeta{}, nil, false
		}
		pk[i] = values[idx]
	}
	return values[col], cols[col], pk, true
}

func (p cellEditorPicker) FormatForEdit(v any, col models.ColumnMeta) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if t, ok := v.(time.Time); ok {
		return t.Format("2006-01-02 15:04:05.999999-07:00")
	}
	// json/jsonb cells seed with JSON text — the same string the grid
	// shows — rather than Go's default form. pgx decodes them into a Go
	// map (objects/arrays), a []byte, or a string depending on shape;
	// classify by column type so every form routes through
	// FormatJSONValue rather than guessing from the Go value, which
	// misses raw []byte json and prints "[123 34 ...]".
	if grid.IsJSONColumn(col) {
		return grid.FormatJSONValue(v)
	}
	// Fallback for cells with no column meta classification: a Go map is
	// always JSON-shaped.
	if reflect.ValueOf(v).Kind() == reflect.Map {
		return grid.FormatJSONValue(v)
	}
	// Array columns (text[] etc.) decode to a Go slice; seed the editor
	// with Postgres array syntax {a,b,c} — the same string the grid
	// shows — so the edited value commits as a valid array literal rather
	// than Go's "[a b c]" form Postgres rejects.
	if lit, ok := grid.FormatArrayLiteral(v); ok {
		return lit
	}
	return fmt.Sprintf("%v", v)
}

func (p cellEditorPicker) activeTab() *ui.Tab {
	if p.tabs == nil {
		return nil
	}
	return p.tabs.Active()
}

func (p cellEditorPicker) activeGrid() interface {
	Editable() bool
	DisabledReason() string
	CursorPosition() (int, int)
	Columns() []models.ColumnMeta
	AllRows() []models.Row
	RowIdentity() []int
} {
	tab := p.activeTab()
	if tab == nil {
		return nil
	}
	g := tab.Grid()
	if g == nil {
		return nil
	}
	return g
}

// cellEditorStore satisfies controllers.PendingEditStore by resolving
// the active (connID, baseTable) PendingEditSet on every Add / Remove
// / HasEdit call. Routes through the same helperBag closure the commit
// dialog uses, so both flows agree on which set is live.
type cellEditorStore struct {
	resolve func() *models.PendingEditSet
}

func (s cellEditorStore) Add(e models.PendingEdit) error {
	set := s.set()
	if set == nil {
		return fmt.Errorf("cell editor: no active pending edit set")
	}
	return set.Add(e)
}

func (s cellEditorStore) Remove(pk []any, col string) {
	set := s.set()
	if set == nil {
		return
	}
	set.Remove(pk, col)
}

func (s cellEditorStore) HasEdit(pk []any, col string) bool {
	set := s.set()
	if set == nil {
		return false
	}
	return set.HasEdit(pk, col)
}

func (s cellEditorStore) set() *models.PendingEditSet {
	if s.resolve == nil {
		return nil
	}
	return s.resolve()
}
