package controllers_test

import (
	stdcontext "context"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// saveCtrlPrompter is a minimal data.ChainedPrompter for the controller-level
// <leader>s tests. It records whether each prompt fired so tests can assert
// the empty-SQL path never reaches a prompt, and the collision path drives
// PromptChoice (NOT a nested Confirm).
type saveCtrlPrompter struct {
	t *testing.T

	name       string
	nameCancel bool
	pick       string

	stringCalls int
	choiceCalls int
}

func (p *saveCtrlPrompter) PromptString(_ stdcontext.Context, _, _ string, _ func(string) error) (string, error) {
	p.stringCalls++
	if p.nameCancel {
		return "", data.PromptCanceledErr()
	}
	return p.name, nil
}

func (p *saveCtrlPrompter) PromptChoice(_ stdcontext.Context, _, _ string, _ []string) (string, error) {
	p.choiceCalls++
	return p.pick, nil
}

// newSaveCtrl builds a QueryEditorController wired for the <leader>s flow: a
// real SaveQueryHelper over a MemMapFs (so we can read what was persisted) and
// the scripted prompter. The base bag's OnWorker runs the worker fn inline.
func newSaveCtrl(t *testing.T, buf *fakeEditorBuffer, fs afero.Fs, p data.ChainedPrompter) (*controllers.QueryEditorController, *commands.Registry) {
	t.Helper()
	base := newBag()
	base.HelperBag.EditorBuffer = buf

	c := &common.Common{Tr: i18n.EnglishTranslationSet()}
	ctrl := controllers.NewQueryEditorController(c, base.HelperBag.CoreDeps, base.HelperBag.NavDeps, base.HelperBag.UIDeps, base.HelperBag.QueryDeps, base.HelperBag.ThreadingDeps)
	ctrl.SetSaveQuery(data.NewSaveQueryHelper(c, fs, "/queries.yml"), p)

	reg := commands.NewRegistry()
	ctrl.RegisterActions(reg)
	return ctrl, reg
}

func runSave(t *testing.T, reg *commands.Registry, ec commands.ExecCtx) {
	t.Helper()
	cmd, ok := reg.Get(commands.QuerySave)
	if !ok {
		t.Fatal("QuerySave not registered")
	}
	if err := cmd.Handler(ec); err != nil {
		t.Fatalf("QuerySave handler err = %v", err)
	}
}

func TestSavePublishesBinding(t *testing.T) {
	b := newQueryBag(t, drivers.Capabilities{})
	ctrl := controllers.NewQueryEditorController(nil, b.HelperBag.CoreDeps, b.HelperBag.NavDeps, b.HelperBag.UIDeps, b.HelperBag.QueryDeps, b.HelperBag.ThreadingDeps)
	found := false
	for _, kb := range ctrl.GetKeybindings(types.KeybindingsOpts{}) {
		if kb.ActionID != commands.QuerySave {
			continue
		}
		found = true
		if kb.Scope != types.QUERY_EDITOR {
			t.Errorf("QuerySave scope = %s, want QUERY_EDITOR", kb.Scope)
		}
		if kb.Mode&types.ModeInsert != 0 {
			t.Errorf("QuerySave Mode has ModeInsert set; leader chords must exclude INSERT")
		}
	}
	if !found {
		t.Error("QuerySave binding not published")
	}
}

// TestSaveCapturesStatementUnderCursor: Normal mode persists the trimmed
// statement under the cursor.
func TestSaveCapturesStatementUnderCursor(t *testing.T) {
	fs := afero.NewMemMapFs()
	buf := &fakeEditorBuffer{Text: "SELECT 1;", Off: 3}
	p := &saveCtrlPrompter{t: t, name: "n1"}
	_, reg := newSaveCtrl(t, buf, fs, p)

	runSave(t, reg, commands.ExecCtx{Mode: types.ModeNormal})

	if p.stringCalls != 1 {
		t.Fatalf("PromptString calls = %d, want 1", p.stringCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	// statementUnderCursor returns the statement WITHOUT its terminator.
	if len(got) != 1 || got[0].SQL != "SELECT 1" {
		t.Errorf("stored = %+v; want one {n1, SELECT 1}", got)
	}
}

// TestSaveCapturesVisualSelection: Visual mode persists the trimmed
// SelectionText, NOT the statement under cursor.
func TestSaveCapturesVisualSelection(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Buffer text differs from selection so we know which one was captured.
	buf := &fakeEditorBuffer{Text: "SELECT 999;", Off: 3, Sel: "  SELECT 1; SELECT 2;  ", HasSel: true}
	p := &saveCtrlPrompter{t: t, name: "sel"}
	_, reg := newSaveCtrl(t, buf, fs, p)

	runSave(t, reg, commands.ExecCtx{Mode: types.ModeVisual})

	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 {
		t.Fatalf("stored %d entries; want 1 (verbatim, not split)", len(got))
	}
	if got[0].SQL != "SELECT 1; SELECT 2;" {
		t.Errorf("stored SQL = %q; want trimmed selection 'SELECT 1; SELECT 2;'", got[0].SQL)
	}
}

// TestSaveEmptySQLNoPromptNoWrite: an empty/whitespace statement surfaces one
// toast and never reaches the prompt or a write.
func TestSaveEmptySQLNoPromptNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	buf := &fakeEditorBuffer{Text: "   ", Off: 0}
	p := &saveCtrlPrompter{t: t, name: "x"}
	_, reg := newSaveCtrl(t, buf, fs, p)

	runSave(t, reg, commands.ExecCtx{Mode: types.ModeNormal})

	if p.stringCalls != 0 {
		t.Errorf("PromptString calls = %d on empty SQL; want 0", p.stringCalls)
	}
	if p.choiceCalls != 0 {
		t.Errorf("PromptChoice calls = %d on empty SQL; want 0", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 0 {
		t.Errorf("stored = %+v; want no write", got)
	}
}

// TestSaveCollisionDrivesPromptChoiceOverwrite: a name collision drives
// PromptChoice (NOT a nested Confirm); Overwrite replaces in place via Upsert.
func TestSaveCollisionDrivesPromptChoiceOverwrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := config.AppendQuery(fs, "/queries.yml", models.SavedQuery{Name: "dup", SQL: "OLD"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	buf := &fakeEditorBuffer{Text: "SELECT 42;", Off: 3}
	p := &saveCtrlPrompter{t: t, name: "dup", pick: "Overwrite"}
	_, reg := newSaveCtrl(t, buf, fs, p)

	runSave(t, reg, commands.ExecCtx{Mode: types.ModeNormal})

	if p.choiceCalls != 1 {
		t.Errorf("PromptChoice calls = %d; want exactly 1 (collision path)", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	// statementUnderCursor returns the statement WITHOUT its terminator.
	if len(got) != 1 || got[0].SQL != "SELECT 42" {
		t.Errorf("stored = %+v; want one {dup, SELECT 42} replaced in place", got)
	}
}

// TestSaveCollisionCancelNoWrite: Cancel at the overwrite choice writes
// nothing; the existing entry is untouched.
func TestSaveCollisionCancelNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := config.AppendQuery(fs, "/queries.yml", models.SavedQuery{Name: "dup", SQL: "OLD"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	buf := &fakeEditorBuffer{Text: "SELECT 42;", Off: 3}
	p := &saveCtrlPrompter{t: t, name: "dup", pick: "Cancel"}
	_, reg := newSaveCtrl(t, buf, fs, p)

	runSave(t, reg, commands.ExecCtx{Mode: types.ModeNormal})

	if p.choiceCalls != 1 {
		t.Errorf("PromptChoice calls = %d; want 1", p.choiceCalls)
	}
	got, _ := config.LoadQueries(fs, "/queries.yml")
	if len(got) != 1 || got[0].SQL != "OLD" {
		t.Errorf("stored = %+v; want unchanged {dup, OLD}", got)
	}
}
