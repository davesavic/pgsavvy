package orchestrator_test

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/stretchr/testify/require"
)

type connectionsTestFs struct {
	afero.Fs
	lock chan struct{}
}

func newConnectionsTestFs() *connectionsTestFs {
	return &connectionsTestFs{Fs: afero.NewMemMapFs(), lock: make(chan struct{}, 1)}
}

func (fs *connectionsTestFs) LockConnections(path string, deadline time.Time) (string, func(), error) {
	timer := time.NewTimer(max(0, time.Until(deadline)))
	defer timer.Stop()
	select {
	case fs.lock <- struct{}{}:
		return path, func() { <-fs.lock }, nil
	case <-timer.C:
		return "", nil, os.ErrDeadlineExceeded
	}
}

// bootstrapSaveGui wires a real *orchestrator.Gui whose ConnectionsProvider
// reads the live afero memfs at connsPath. The
// OnSaveConnection seam is the production g.saveConnectionForm, so driving the
// modal form's Confirm writes through to connections.yml end-to-end.
func bootstrapSaveGui(t *testing.T, fs afero.Fs, connsPath string) (*orchestrator.Gui, *testfake.RecorderGuiDriver) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/state/state.yml", common.DefaultClock())
	var historyOpens atomic.Int32

	g := orchestrator.NewGui(orchestrator.Deps{
		Common:          c,
		Store:           store,
		ConnectionsPath: connsPath,
		ConnectionsProvider: func() []models.Connection {
			conns, _ := config.LoadConnections(fs, connsPath)
			return conns
		},
		DriverNamesFn:   func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) { historyOpens.Add(1); return nil, nil },
	})
	rec := testfake.NewRecorderGuiDriver()
	if err := g.UseDriverForTest(rec); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	t.Cleanup(func() {
		_ = g.Close()
		if got := historyOpens.Load(); got != 1 {
			t.Errorf("history provider called %d times, want only the bootstrap call", got)
		}
	})
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

// TestConnectionManagerEditPreservesHiddenFields is the headline case
// (AC1-edit + AC2 + AC3): a profile carrying ssh_tunnel AND password_command is
// edited (name changed) via the modal form, and after the rewrite BOTH fields
// survive on the renamed row.
func TestConnectionManagerEditPreservesHiddenFields(t *testing.T) {
	fs := newConnectionsTestFs()
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
	fs := newConnectionsTestFs()
	path := "/cfg/connections.yml"

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	if err := g.Controllers().ConnectionManager.Add(commands.ExecCtx{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if modal.Mode() != guicontext.ModeForm {
		t.Fatalf("mode = %v after Add, want ModeForm", modal.Mode())
	}
	// Discrete-field order is name(0) driver(1) host(2) port(3) user(4)
	// database(5) sslmode(6). The Add form pre-seeds host=localhost, port=5432;
	// here we override host and set a database via the text seam. The driver
	// field defaults via the form's driversFn; we don't assert its exact value
	// (the test binary's registry is shared across tests).
	modal.FormSetFocusedValue("alice") // name @ 0
	modal.FormMoveFocus(2)
	modal.FormSetFocusedValue("dbhost") // host @ 2
	modal.FormMoveFocus(3)
	modal.FormSetFocusedValue("appdb") // database @ 5

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
	// Saved as discrete fields (host overridden, port from the default seed,
	// database entered); no raw dsn is written for a discrete connection.
	if got.Name != "alice" || got.Host != "dbhost" || got.Database != "appdb" ||
		got.Port != 5432 || got.DSN != "" {
		t.Fatalf("loaded[0] = %+v, want Name=alice Host=dbhost Database=appdb Port=5432 DSN=\"\"", got)
	}

	// AC4 (refresh): the modal list reflects the new row without restart.
	row, ok := modal.SelectedItem().(*models.Connection)
	if !ok || row == nil || row.Name != "alice" {
		t.Fatalf("modal row after add = %+v, want alice", modal.SelectedItem())
	}
}

// TestConnectionManagerSaveSSHTunnelRoundTrip covers T6: an add form with SSH
// host/user/port set and identity_from_agent toggled on persists those values
// to connections.yml and reloads them intact.
func TestConnectionManagerSaveSSHTunnelRoundTrip(t *testing.T) {
	fs := newConnectionsTestFs()
	path := "/cfg/connections.yml"

	g, _ := bootstrapSaveGui(t, fs, path)
	modal := pushModal(t, g)

	if err := g.Controllers().ConnectionManager.Add(commands.ExecCtx{}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	modal.FormSetFocusedValue("ssh-conn") // name @ 0
	focusConnectionFormField(t, modal, "use_ssh_tunnel")
	modal.FormToggleFocused() // enable tunnel; auth defaults to agent
	modal.FormMoveFocus(2)    // ssh_host, past ssh_auth
	modal.FormSetFocusedValue("bastion.prod")
	modal.FormMoveFocus(1)
	modal.FormSetFocusedValue("deploy") // ssh_user @ 19
	modal.FormMoveFocus(1)
	modal.FormSetFocusedValue("2222") // ssh_port @ 20
	// IdentityFromAgent is set from the agent auth method at save (applySSHAuth).

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
	tun := loaded[0].SSHTunnel
	if tun == nil {
		t.Fatal("SSHTunnel = nil after save")
	}
	if tun.Host != "bastion.prod" || tun.User != "deploy" || tun.Port != 2222 || !tun.IdentityFromAgent {
		t.Fatalf("SSHTunnel = %+v, want host=bastion.prod user=deploy port=2222 agent=true", tun)
	}
}

func focusConnectionFormField(t *testing.T, modal *guicontext.ConnectionManagerContext, label string) {
	t.Helper()
	modal.FormMoveFocus(-100)
	for range 50 {
		if modal.FormFocusedLabel() == label {
			return
		}
		modal.FormMoveFocus(1)
	}
	t.Fatalf("form field %q missing", label)
}

func TestCLIHistoryForm_AtomicConsentSave(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		add, initial, rename  bool
		toggles, accessCycles int
	}{
		{name: "add-default", add: true},
		{name: "add-enabled", add: true, toggles: 1},
		{name: "enable", toggles: 1},
		{name: "disable", initial: true, toggles: 1},
		{name: "toggle-back", initial: true, toggles: 2},
		{name: "atomic-both-and-rename", rename: true, toggles: 1, accessCycles: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newConnectionsTestFs()
			const path = "/cfg/connections.yml"
			source := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessMetadata, CLIHistory: tc.initial, ReadOnly: true, PasswordCommand: "must-not-run", Label: "preserved"}
			neighbor := models.Connection{Name: "neighbor", Driver: "postgres", CLIHistory: true, CLIAccess: models.CLIAccessRowRead}
			profiles := []models.Connection{source, neighbor}
			if tc.add {
				profiles = []models.Connection{neighbor}
			}
			require.NoError(t, config.SaveConnections(fs, path, profiles))
			g, _ := bootstrapSaveGui(t, fs, path)
			modal := pushModal(t, g)
			ctrl := g.Controllers().ConnectionManager
			if tc.add {
				require.NoError(t, ctrl.Add(commands.ExecCtx{}))
			} else {
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			}
			name := "dev"
			if tc.rename {
				name = "renamed"
			}
			modal.FormSetFocusedValue(name)
			focusConnectionFormField(t, modal, "cli_history")
			for range tc.toggles {
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			}
			focusConnectionFormField(t, modal, "cli_access")
			for range tc.accessCycles {
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			}
			require.NoError(t, ctrl.Confirm(commands.ExecCtx{}))
			require.Equal(t, guicontext.ModeList, modal.Mode())
			loaded, err := config.LoadConnections(fs, path)
			require.NoError(t, err)
			require.Len(t, loaded, 2)
			var got, other models.Connection
			for _, profile := range loaded {
				if profile.Name == name {
					got = profile
				} else {
					other = profile
				}
			}
			require.Equal(t, neighbor, other)
			require.Equal(t, tc.initial != (tc.toggles%2 == 1), got.CLIHistory)
			if !tc.add {
				require.Equal(t, source.PasswordCommand, got.PasswordCommand)
				require.Equal(t, source.Label, got.Label)
				require.Equal(t, source.ReadOnly, got.ReadOnly)
				wantAccess := source.CLIAccess
				if tc.accessCycles > 0 {
					wantAccess = models.CLIAccessRowRead
				}
				require.Equal(t, wantAccess, got.CLIAccess)
			}
			require.Nil(t, g.HistoryStoreForTest(), "saving consent must not open history")
		})
	}
}

func TestCLIHistoryForm_StaleRevocation(t *testing.T) {
	for _, toggles := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("toggles_%d", toggles), func(t *testing.T) {
			fs := newConnectionsTestFs()
			const path = "/cfg/connections.yml"
			source := models.Connection{Name: "dev", Driver: "postgres", CLIHistory: true, CLIAccess: models.CLIAccessRowRead}
			require.NoError(t, config.SaveConnections(fs, path, []models.Connection{source}))
			g, _ := bootstrapSaveGui(t, fs, path)
			modal := pushModal(t, g)
			ctrl := g.Controllers().ConnectionManager
			require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			modal.FormSetFocusedValue("renamed")
			focusConnectionFormField(t, modal, "cli_history")
			for range toggles {
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			}
			source.CLIHistory, source.CLIAccess = false, models.CLIAccessDisabled
			require.NoError(t, config.SaveConnections(fs, path, []models.Connection{source}))
			before, err := afero.ReadFile(fs, path)
			require.NoError(t, err)
			err = ctrl.Confirm(commands.ExecCtx{})
			if toggles == 1 {
				require.ErrorIs(t, err, config.ErrConnectionsChanged)
				require.Equal(t, guicontext.ModeForm, modal.Mode())
				after, readErr := afero.ReadFile(fs, path)
				require.NoError(t, readErr)
				require.Equal(t, before, after)
			} else {
				require.NoError(t, err)
				loaded, loadErr := config.LoadConnections(fs, path)
				require.NoError(t, loadErr)
				require.Len(t, loaded, 1)
				require.False(t, loaded[0].CLIHistory)
				require.Equal(t, models.CLIAccessDisabled, loaded[0].CLIAccess)
				require.Equal(t, "renamed", loaded[0].Name)
			}
		})
	}
}

func TestCLIHistoryForm_SaveFailures(t *testing.T) {
	for _, fixture := range []string{"missing", "renamed", "duplicate", "collision", "contention", "denied", "access_conflict"} {
		t.Run(fixture, func(t *testing.T) {
			base := newConnectionsTestFs()
			const path = "/cfg/connections.yml"
			source := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessMetadata, Password: "never-emit"}
			neighbor := models.Connection{Name: "neighbor", Driver: "postgres"}
			require.NoError(t, config.SaveConnections(base, path, []models.Connection{source, neighbor}))
			// Save intentionally drops inline passwords; a loaded legacy secret
			// must still not enter validation/error rendering on failed saves.
			require.NoError(t, afero.WriteFile(base, path, []byte("connections:\n - name: dev\n   driver: postgres\n   cli_access: metadata\n   password: never-emit\n - name: neighbor\n   driver: postgres\n"), 0o600))
			fs := base
			if fixture == "denied" {
				fs = &connectionsTestFs{Fs: afero.NewReadOnlyFs(base.Fs), lock: base.lock}
			}
			g, rec := bootstrapSaveGui(t, fs, path)
			modal := pushModal(t, g)
			ctrl := g.Controllers().ConnectionManager
			require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			if fixture == "collision" {
				modal.FormSetFocusedValue("neighbor")
			}
			focusConnectionFormField(t, modal, "cli_history")
			require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
			switch fixture {
			case "missing":
				require.NoError(t, config.SaveConnections(base, path, []models.Connection{neighbor}))
			case "renamed":
				source.Name = "externally-renamed"
				require.NoError(t, config.SaveConnections(base, path, []models.Connection{source, neighbor}))
			case "duplicate":
				require.NoError(t, afero.WriteFile(base, path, []byte("connections:\n - name: dev\n - name: dev\n"), 0o600))
			case "contention":
				base.lock <- struct{}{}
				defer func() { <-base.lock }()
			case "access_conflict":
				focusConnectionFormField(t, modal, "cli_access")
				require.NoError(t, ctrl.Edit(commands.ExecCtx{}))
				source.CLIAccess = models.CLIAccessDisabled
				require.NoError(t, config.SaveConnections(base, path, []models.Connection{source, neighbor}))
			}
			before, err := afero.ReadFile(base, path)
			require.NoError(t, err)
			err = ctrl.Confirm(commands.ExecCtx{})
			if fixture == "collision" {
				require.NoError(t, err, "local validation returns nil while keeping inline error/form")
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "never-emit")
			}
			require.Equal(t, guicontext.ModeForm, modal.Mode())
			after, err := afero.ReadFile(base, path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.NoError(t, g.RunLayout(130, 42))
			require.NotContains(t, rec.GetViewBuffer(string(types.CONNECTION_MANAGER)), "never-emit")
			require.Nil(t, g.HistoryStoreForTest())
		})
	}
}

// TestConnectionManagerEscBeforeSaveNoWrite covers AC4: opening a form and
// pressing Esc (cancel) before save leaves connections.yml byte-for-byte
// unchanged.
func TestConnectionManagerEscBeforeSaveNoWrite(t *testing.T) {
	fs := newConnectionsTestFs()
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
	fs := newConnectionsTestFs()
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

func TestConnectionManagerCLIAccessSave(t *testing.T) {
	for _, tc := range []struct {
		name     string
		add      bool
		initial  models.CLIAccess
		cycles   int
		want     models.CLIAccess
		stale    bool
		conflict bool
		cancel   bool
		rename   bool
	}{
		{name: "add-default", add: true},
		{name: "add-metadata", add: true, cycles: 1, want: models.CLIAccessMetadata},
		{name: "add-row-read", add: true, cycles: 2, want: models.CLIAccessRowRead},
		{name: "add-full", add: true, cycles: 3, want: models.CLIAccessFull},
		{name: "enable", cycles: 1, want: models.CLIAccessMetadata},
		{name: "upgrade-and-rename", initial: models.CLIAccessMetadata, cycles: 1, want: models.CLIAccessRowRead, rename: true},
		{name: "upgrade-full-and-rename", initial: models.CLIAccessRowRead, cycles: 1, want: models.CLIAccessFull, rename: true},
		{name: "revoke", initial: models.CLIAccessRowRead, cycles: 2, want: models.CLIAccessDisabled},
		{name: "revoke-full", initial: models.CLIAccessFull, cycles: 1, want: models.CLIAccessDisabled},
		{name: "untouched-legacy"},
		{name: "untouched-full", initial: models.CLIAccessFull, want: models.CLIAccessFull},
		{name: "untouched-revoked", initial: models.CLIAccessRowRead, stale: true, want: models.CLIAccessDisabled},
		{name: "cycled-back-revoked", initial: models.CLIAccessRowRead, cycles: 4, stale: true, want: models.CLIAccessDisabled},
		{name: "full-cycled-back-revoked", initial: models.CLIAccessFull, cycles: 4, stale: true, want: models.CLIAccessDisabled},
		{name: "stale-explicit-change", initial: models.CLIAccessRowRead, cycles: 2, stale: true, conflict: true, want: models.CLIAccessDisabled},
		{name: "stale-upgrade-full", initial: models.CLIAccessRowRead, cycles: 1, stale: true, conflict: true, want: models.CLIAccessDisabled},
		{name: "cancel", initial: models.CLIAccessMetadata, cycles: 1, cancel: true, want: models.CLIAccessMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := newConnectionsTestFs()
			const path = "/cfg/connections.yml"
			if !tc.add {
				if err := config.SaveConnections(fs, path, []models.Connection{{Name: "dev", Driver: "postgres", CLIAccess: tc.initial}}); err != nil {
					t.Fatal(err)
				}
			}
			g, rec := bootstrapSaveGui(t, fs, path)
			modal := pushModal(t, g)
			ctrl := g.Controllers().ConnectionManager
			if tc.add {
				if err := ctrl.Add(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
			} else if err := ctrl.Edit(commands.ExecCtx{}); err != nil {
				t.Fatal(err)
			}
			name := "dev"
			if tc.rename {
				name = "renamed"
			}
			modal.FormSetFocusedValue(name)
			for range 40 {
				if modal.FormFocusedLabel() == "cli_access" {
					break
				}
				modal.FormMoveFocus(1)
			}
			if modal.FormFocusedLabel() != "cli_access" {
				t.Fatal("CLI access selector unavailable")
			}
			for range tc.cycles {
				if err := ctrl.Edit(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stale {
				if err := config.SaveConnections(fs, path, []models.Connection{{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessDisabled}}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := afero.ReadFile(fs, path)
			if tc.cancel {
				if err := ctrl.Close(commands.ExecCtx{}); err != nil {
					t.Fatal(err)
				}
			} else {
				err := ctrl.Confirm(commands.ExecCtx{})
				if tc.conflict {
					if !errors.Is(err, config.ErrConnectionsChanged) || modal.Mode() != guicontext.ModeForm {
						t.Fatalf("stale edit error = %v, mode = %v", err, modal.Mode())
					}
					if err := g.RunLayout(180, 42); err != nil {
						t.Fatal(err)
					}
					if body := rec.GetViewBuffer(string(types.CONNECTION_MANAGER)); !strings.Contains(body, "close and reopen") {
						t.Fatalf("missing conflict recovery message: %q", body)
					}
				} else if err != nil || modal.Mode() != guicontext.ModeList {
					t.Fatalf("save error = %v, mode = %v", err, modal.Mode())
				}
			}
			after, err := afero.ReadFile(fs, path)
			if err != nil {
				t.Fatal(err)
			}
			if (tc.cancel || tc.conflict) && string(before) != string(after) {
				t.Fatal("cancelled or stale edit wrote to disk")
			}
			got, err := config.LoadConnections(fs, path)
			if err != nil || len(got) != 1 || got[0].CLIAccess != tc.want || got[0].Name != name {
				t.Fatalf("saved profiles = %v, error = %v; want policy %q, name %q", got, err, tc.want, name)
			}
			if !tc.cancel && !tc.conflict {
				if row, ok := modal.SelectedItem().(*models.Connection); !ok || row.CLIAccess != tc.want {
					t.Fatal("saved policy not reflected in refreshed connection list")
				}
			}
		})
	}
}
