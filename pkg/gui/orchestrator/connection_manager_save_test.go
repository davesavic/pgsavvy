package orchestrator_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/dbsavvy/pkg/gui/context"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// bootstrapSaveGui wires a real *orchestrator.Gui whose ConnectionsProvider
// reads the live afero memfs at connsPath. dbsavvy-zod: the
// OnSaveConnection seam is the production g.saveConnectionForm, so driving the
// modal form's Confirm writes through to connections.yml end-to-end.
func bootstrapSaveGui(t *testing.T, fs afero.Fs, connsPath string) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:          c,
		Store:           store,
		ConnectionsPath: connsPath,
		ConnectionsProvider: func() []models.Connection {
			conns, _ := config.LoadConnections(fs, connsPath)
			return conns
		},
		DriverNamesFn:   func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) { return nil, nil },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, rec
}

// pushModal pushes the CONNECTION_MANAGER modal and returns its context.
func pushModal(t *testing.T, g *orchestrator.Gui) *guicontext.ConnectionManagerContext {
	t.Helper()
	modal := g.Registry().ConnectionManager
	if err := g.ContextTree().Push(modal); err != nil {
		t.Fatalf("push modal: %v", err)
	}
	if top := g.ContextTree().Current(); top == nil || top.GetKey() != types.CONNECTION_MANAGER {
		t.Fatalf("modal not top after push: %v", top)
	}
	return modal
}

// TestConnectionManagerEditPreservesHiddenFields is the dbsavvy-zod headline
// (AC1-edit + AC2 + AC3): a profile carrying ssh_tunnel AND password_command is
// edited (name changed) via the modal form, and after the rewrite BOTH fields
// survive on the renamed row.
func TestConnectionManagerEditPreservesHiddenFields(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	orig := []models.Connection{{
		Name:            "prod",
		Driver:          "postgres",
		DSN:             "postgres://localhost:5432/prod",
		PasswordCommand: "op read 'op://Prod/db/password'",
		SSHTunnel:       &models.SSHTunnelConfig{Host: "bastion.prod", User: "deploy", Port: 22, IdentityFile: "~/.ssh/id_ed25519"},
	}}
	if err := config.SaveConnections(fs, path, orig); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)
	// onShow (push) loaded rows from the provider; the single profile is row 0.
	if got, ok := modal.SelectedItem().(*models.Connection); !ok || got == nil || got.Name != "prod" {
		t.Fatalf("selected row = %+v, want prod", modal.SelectedItem())
	}

	// Open the edit form for the selected row via the controller.
	if err := g.Controllers().ConnectionManager.Edit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("mode = %v after Edit, want ModeForm", modal.Mode())
	}
	// Focus defaults to the Name field (focus 0). Rename it.
	modal.FormSetFocusedValue("prod-renamed")

	// Save via Confirm (Enter). Success → controller flips to ModeList.
	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if modal.Mode() != guicontext.ModeList {
		t.Fatalf("mode = %v after save, want ModeList", modal.Mode())
	}

	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("post LoadConnections: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("post: got %d rows, want 1", len(loaded))
	}
	got := loaded[0]
	if got.Name != "prod-renamed" {
		t.Fatalf("Name = %q, want prod-renamed", got.Name)
	}
	if got.PasswordCommand != orig[0].PasswordCommand {
		t.Fatalf("PasswordCommand = %q, want %q (preservation lost)", got.PasswordCommand, orig[0].PasswordCommand)
	}
	if got.SSHTunnel == nil {
		t.Fatal("SSHTunnel = nil after edit; preservation lost")
	}
	if got.SSHTunnel.Host != "bastion.prod" {
		t.Fatalf("SSHTunnel.Host = %q, want bastion.prod", got.SSHTunnel.Host)
	}

	// AC4 (refresh): the modal list reflects the rename without restart.
	row, ok := modal.SelectedItem().(*models.Connection)
	if !ok || row == nil || row.Name != "prod-renamed" {
		t.Fatalf("modal row after save = %+v, want prod-renamed", modal.SelectedItem())
	}
}

// TestConnectionManagerAddAppendsRow covers AC1-add: an add form, filled and
// saved, gains exactly one row via AppendConnection.
func TestConnectionManagerAddAppendsRow(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	if err := g.Controllers().ConnectionManager.Add(commands.ExecCtx{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("mode = %v after Add, want ModeForm", modal.Mode())
	}
	// Field order is name, driver, dsn — name is focus 0, dsn is focus 2. Set
	// both via the text seam. The driver field defaults via the form's
	// driversFn; we don't assert its exact value (the test binary's registry
	// is shared across tests).
	modal.FormSetFocusedValue("alice")
	modal.FormMoveFocus(2)
	modal.FormSetFocusedValue("postgres://localhost:5432/db")

	if err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{}); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if modal.Mode() != guicontext.ModeList {
		t.Fatalf("mode = %v after save, want ModeList", modal.Mode())
	}

	loaded, err := config.LoadConnections(fs, path)
	if err != nil {
		t.Fatalf("post LoadConnections: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("post: got %d rows, want 1 (AppendConnection)", len(loaded))
	}
	got := loaded[0]
	if got.Name != "alice" || got.DSN != "postgres://localhost:5432/db" {
		t.Fatalf("loaded[0] = %+v, want Name=alice DSN=postgres://localhost:5432/db", got)
	}

	// AC4 (refresh): the modal list reflects the new row without restart.
	row, ok := modal.SelectedItem().(*models.Connection)
	if !ok || row == nil || row.Name != "alice" {
		t.Fatalf("modal row after add = %+v, want alice", modal.SelectedItem())
	}
}

// TestConnectionManagerEscBeforeSaveNoWrite covers AC4: opening a form and
// pressing Esc (cancel) before save leaves connections.yml byte-for-byte
// unchanged.
func TestConnectionManagerEscBeforeSaveNoWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	orig := []models.Connection{{Name: "prod", Driver: "postgres", DSN: "postgres://localhost:5432/prod"}}
	if err := config.SaveConnections(fs, path, orig); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}
	before, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	if err := g.Controllers().ConnectionManager.Edit(commands.ExecCtx{}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	modal.FormSetFocusedValue("prod-renamed")

	// Esc cancels the form (Close handler in form mode → back to ModeList, no
	// write).
	if err := g.Controllers().ConnectionManager.Close(commands.ExecCtx{}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("connections.yml changed after cancel:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestConnectionManagerSaveErrorStampsFormError covers the failure seam: an
// edit whose originalName is absent triggers ErrConnectionNotFound; the
// callback stamps the inline form error and the controller stays in ModeForm.
func TestConnectionManagerSaveErrorStampsFormError(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/cfg/connections.yml"

	// Seed a DIFFERENT profile so the on-disk file exists but the edited
	// originalName ("ghost") is not present → UpdateConnection returns
	// ErrConnectionNotFound.
	if err := config.SaveConnections(fs, path, []models.Connection{{Name: "other", Driver: "postgres", DSN: "postgres://x"}}); err != nil {
		t.Fatalf("seed SaveConnections: %v", err)
	}

	g, rec := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	// Open an edit form seeded from a profile NOT on disk (originalName=ghost).
	modal.OpenEditForm(
		models.Connection{Name: "ghost", Driver: "postgres", DSN: "postgres://ghost"},
		[]string{"other"},
		func() []string { return []string{"postgres"} },
	)
	// Keep the name as-is (ghost) so validation passes but UpdateConnection
	// cannot find originalName.
	modal.FormSetFocusedValue("ghost")

	err := g.Controllers().ConnectionManager.Confirm(commands.ExecCtx{})
	if !errors.Is(err, config.ErrConnectionNotFound) {
		t.Fatalf("Confirm err = %v, want ErrConnectionNotFound propagated", err)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("mode = %v after failed save, want ModeForm", modal.Mode())
	}
	// The inline form error must be stamped (renders under the focused field).
	if err := g.RunLayout(80, 24); err != nil {
		t.Fatalf("RunLayout: %v", err)
	}
	body := rec.GetViewBuffer(string(types.CONNECTION_MANAGER))
	if !strings.Contains(body, i18n.EnglishTranslationSet().SaveConnectionFailed) {
		t.Fatalf("form body missing stamped error %q; body=%q",
			i18n.EnglishTranslationSet().SaveConnectionFailed, body)
	}
}
