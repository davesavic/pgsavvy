package ui

import (
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/popup"
)

// newPathMenuOnFile builds an export menu whose cursor sits on the Path
// field with a File destination — the only state in which the 'i'
// edit-path binding does anything.
func newPathMenuOnFile(initial string) *popup.ExportMenu {
	m := popup.NewExportMenu(
		[]string{"CSV"},
		[]string{"File", "Clipboard"},
		[]string{"Loaded"},
		-1, false,
	)
	m.Prefill(initial)
	// Format(0) -> Destination(1) -> Path(2).
	m.MoveField(+1)
	m.MoveField(+1)
	if !m.IsPathFieldActive() {
		panic("setup: Path field not active")
	}
	return m
}

// TestExportMenuEditPath_SubmitWritesBackAndReopens verifies that the
// seeded PROMPT receives the current path, a trimmed valid value is
// written back via SetPath, and the menu is re-pushed (return-to-menu).
func TestExportMenuEditPath_SubmitWritesBackAndReopens(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	var gotInitial string
	var submit func(string) error
	pushes := 0

	h := NewResultTabsHelper(ResultTabsHelperDeps{
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(initial string, onSubmit func(string) error, _ func() error) error {
			gotInitial = initial
			submit = onSubmit
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}

	h.ExportMenuEditPath()

	if gotInitial != "/tmp/out.csv" {
		t.Fatalf("prompt seeded with %q; want %q", gotInitial, "/tmp/out.csv")
	}
	if submit == nil {
		t.Fatal("EditExportPath onSubmit not captured")
	}

	if err := submit("  /tmp/new.csv  "); err != nil {
		t.Fatalf("submit returned error: %v", err)
	}
	if got := m.Path(); got != "/tmp/new.csv" {
		t.Errorf("Path = %q; want trimmed %q", got, "/tmp/new.csv")
	}
	if pushes != 1 {
		t.Errorf("PushExportMenu called %d times on submit; want 1", pushes)
	}
}

// TestExportMenuEditPath_RejectsControlChars verifies that on a control-char
// submit the helper: (a) leaves Path unchanged, (b) surfaces an error toast,
// (c) RE-OPENS the prompt seeded with the rejected value, and (d) returns nil
// from onSubmit (so master_editor.go does not swallow an error and drop the
// user out of the flow). The menu must NOT be re-pushed on rejection.
func TestExportMenuEditPath_RejectsControlChars(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	toast := &fakeToaster{}
	var submit func(string) error
	var initials []string
	pushes := 0
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		Toast:          toast,
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(initial string, onSubmit func(string) error, _ func() error) error {
			initials = append(initials, initial)
			submit = onSubmit
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()

	bad := "/tmp/ba\nd.csv"
	if err := submit(bad); err != nil {
		t.Fatalf("onSubmit returned error %v; want nil (error must not propagate to master_editor)", err)
	}
	if got := m.Path(); got != "/tmp/out.csv" {
		t.Errorf("Path mutated on rejected submit: %q", got)
	}
	if pushes != 0 {
		t.Errorf("PushExportMenu called %d times on rejected submit; want 0", pushes)
	}
	if len(toast.Messages()) == 0 {
		t.Error("no toast surfaced on rejected submit; want a validation toast")
	}
	// First open seeds the current path; the rejection re-opens seeded with
	// the rejected value so the user can fix it in place.
	if len(initials) != 2 {
		t.Fatalf("EditExportPath invoked %d times; want 2 (initial open + re-open)", len(initials))
	}
	if initials[1] != bad {
		t.Errorf("re-open seeded with %q; want rejected value %q", initials[1], bad)
	}
}

// TestExportMenuEditPath_CancelLeavesPathAndReopens verifies cancel keeps
// the path unchanged and re-pushes the menu.
func TestExportMenuEditPath_CancelLeavesPathAndReopens(t *testing.T) {
	m := newPathMenuOnFile("/tmp/out.csv")

	var cancel func() error
	pushes := 0
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		PushExportMenu: func() error { pushes++; return nil },
		EditExportPath: func(_ string, _ func(string) error, onCancel func() error) error {
			cancel = onCancel
			return nil
		},
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()

	if err := cancel(); err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	if got := m.Path(); got != "/tmp/out.csv" {
		t.Errorf("Path changed on cancel: %q", got)
	}
	if pushes != 1 {
		t.Errorf("PushExportMenu called %d times on cancel; want 1", pushes)
	}
}

// TestExportMenuEditPath_NoOpWhenPathFieldNotActive verifies the edit
// prompt does not open when the cursor is not on the Path field.
func TestExportMenuEditPath_NoOpWhenPathFieldNotActive(t *testing.T) {
	m := popup.NewExportMenu([]string{"CSV"}, []string{"File"}, []string{"Loaded"}, -1, false)
	// Cursor stays on FieldFormat — Path not active.
	opened := false
	h := NewResultTabsHelper(ResultTabsHelperDeps{
		EditExportPath: func(string, func(string) error, func() error) error { opened = true; return nil },
	})
	h.exportMenu = &activeExportMenu{menu: m}
	h.ExportMenuEditPath()
	if opened {
		t.Error("edit prompt opened while Path field inactive")
	}
}
