package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// fakeSelectionState is a hand-rolled SelectionState fake mirroring the
// shape ui.ChoiceHelper exposes (Active/Label/Choices/Cursor).
type fakeSelectionState struct {
	active  bool
	label   string
	choices []string
	cursor  int
}

func (f *fakeSelectionState) Active() bool      { return f.active }
func (f *fakeSelectionState) Label() string     { return f.label }
func (f *fakeSelectionState) Choices() []string { return f.choices }
func (f *fakeSelectionState) Cursor() int       { return f.cursor }

func newTestSelection(state SelectionState, drv types.GuiDriver) *SelectionContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SELECTION,
		ViewName: string(types.SELECTION),
		Kind:     types.TEMPORARY_POPUP,
	})
	deps := types.ContextTreeDeps{GuiDriver: drv}
	c := NewSelectionContext(base, deps)
	if state != nil {
		c.SetState(state)
	}
	return c
}

func TestSelectionContext_NilStateNoOps(t *testing.T) {
	drv := &captureDriver{}
	c := newTestSelection(nil, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times with nil state; want 0", drv.writes)
	}
}

func TestSelectionContext_InactiveNoOps(t *testing.T) {
	drv := &captureDriver{}
	state := &fakeSelectionState{
		active:  false,
		label:   "Pick a driver",
		choices: []string{"postgres", "mysql"},
	}
	c := newTestSelection(state, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 0 {
		t.Errorf("SetContent called %d times when inactive; want 0", drv.writes)
	}
}

func TestSelectionContext_RendersLabelAndCursorMarker(t *testing.T) {
	drv := &captureDriver{}
	state := &fakeSelectionState{
		active:  true,
		label:   "Pick a driver",
		choices: []string{"postgres", "mysql", "sqlite"},
		cursor:  1,
	}
	c := newTestSelection(state, drv)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if drv.writes != 1 {
		t.Fatalf("writes = %d, want 1", drv.writes)
	}
	if drv.lastView != string(types.SELECTION) {
		t.Errorf("view = %q, want %q", drv.lastView, string(types.SELECTION))
	}
	body := drv.lastContent
	if !strings.Contains(body, "Pick a driver") {
		t.Errorf("body missing label %q; got %q", "Pick a driver", body)
	}
	for _, choice := range state.choices {
		if !strings.Contains(body, choice) {
			t.Errorf("body missing choice %q; got %q", choice, body)
		}
	}
	// Cursor sits on choice index 1 ("mysql"). The marker line must
	// distinguish the selected choice from the unselected ones — same
	// line containing "mysql" must contain "> "; lines for postgres /
	// sqlite must not have the marker prefix.
	lines := strings.Split(body, "\n")
	var pgLine, mysqlLine, sqliteLine string
	for _, ln := range lines {
		switch {
		case strings.Contains(ln, "postgres"):
			pgLine = ln
		case strings.Contains(ln, "mysql"):
			mysqlLine = ln
		case strings.Contains(ln, "sqlite"):
			sqliteLine = ln
		}
	}
	if !strings.Contains(mysqlLine, "> ") {
		t.Errorf("selected line lacks '> ' marker; got %q", mysqlLine)
	}
	if strings.Contains(pgLine, "> ") {
		t.Errorf("unselected pg line has '> ' marker; got %q", pgLine)
	}
	if strings.Contains(sqliteLine, "> ") {
		t.Errorf("unselected sqlite line has '> ' marker; got %q", sqliteLine)
	}
}

func TestSelectionContext_NilGuiDriverNoPanic(t *testing.T) {
	state := &fakeSelectionState{active: true, label: "x", choices: []string{"a"}}
	c := newTestSelection(state, nil)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender with nil driver: %v", err)
	}
}
