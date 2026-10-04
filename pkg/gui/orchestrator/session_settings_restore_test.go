package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func TestReplaySessionSettings(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	t.Run("empty saved map returns empty hint", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		got := replaySessionSettings(ctx, map[string]string{}, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("nil saved map returns empty hint", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		got := replaySessionSettings(ctx, nil, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("role is filtered out by allowlist", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"role": "admin"}
		got := replaySessionSettings(ctx, saved, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
		assert.Empty(t, snap.All())
	})

	t.Run("search_path schemas are identifier-quoted", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"search_path": "app, public"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, `restored: search_path=app, public`, got)
		assert.Equal(t, []string{`SET search_path TO "app", "public"`}, executed)
		all := snap.All()
		assert.Equal(t, "app, public", all["search_path"])
	})

	t.Run("search_path injection payload is safely quoted", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"search_path": `app"; DROP TABLE users--`}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.NotEmpty(t, got)
		assert.Equal(t, []string{`SET search_path TO "app""; DROP TABLE users--"`}, executed)
	})

	t.Run("statement_timeout is canonicalized", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"statement_timeout": "30s"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: statement_timeout=30s", got)
		assert.Equal(t, []string{"SET statement_timeout = '30s'"}, executed)
		assert.Equal(t, "30s", snap.All()["statement_timeout"])
	})

	t.Run("invalid statement_timeout is skipped", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"statement_timeout": "DROP TABLE users"}
		got := replaySessionSettings(ctx, saved, noopExec, snap, log, "conn1")

		assert.Equal(t, "", got)
		assert.Empty(t, snap.All())
	})

	t.Run("timezone value is single-quote escaped", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"timezone": "America/New_York"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: timezone=America/New_York", got)
		assert.Equal(t, []string{"SET timezone TO 'America/New_York'"}, executed)
	})

	t.Run("application_name with single quote is escaped", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"application_name": "my'app"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: application_name=my'app", got)
		assert.Equal(t, []string{"SET application_name TO 'my''app'"}, executed)
	})

	t.Run("failed SET is skipped, others continue", func(t *testing.T) {
		exec := func(_ context.Context, sql string) error {
			if sql == `SET search_path TO "gone_schema"` {
				return errors.New("schema does not exist")
			}
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{
			"application_name": "pgsavvy",
			"search_path":      "gone_schema",
		}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Contains(t, got, "application_name=pgsavvy")
		assert.NotContains(t, got, "search_path")
		all := snap.All()
		assert.Equal(t, "", all["search_path"])
		assert.Equal(t, "pgsavvy", all["application_name"])
	})

	t.Run("all settings fail returns empty hint", func(t *testing.T) {
		exec := func(_ context.Context, _ string) error {
			return errors.New("fail")
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"timezone": "UTC"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("multiple settings restored in sorted order", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{
			"timezone":          "UTC",
			"search_path":       "myschema",
			"statement_timeout": "10s",
		}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, `restored: search_path=myschema, statement_timeout=10s, timezone=UTC`, got)
		assert.Len(t, executed, 3)
	})
}

func noopExec(_ context.Context, _ string) error { return nil }

func TestReview_settings_legacy(t *testing.T) {
	for _, tc := range []struct {
		name, key, value, sql string
	}{
		{"quoted timezone", "timezone", "'America/New_York'", "SET timezone TO 'America/New_York'"},
		{"quoted application", "application_name", "'my''app'", "SET application_name TO 'my''app'"},
		{"quoted comma schema", "search_path", `"a,b", "say""hi", public`, `SET search_path TO "a,b", "say""hi", "public"`},
		{"malformed quote", "search_path", `"a,b`, ""},
		{"ambiguous scalar", "application_name", "E'escaped'", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sqls []string
			var logs bytes.Buffer
			replaySessionSettings(context.Background(), map[string]string{tc.key: tc.value}, func(_ context.Context, sql string) error {
				sqls = append(sqls, sql)
				return nil
			}, session.NewSettingsSnapshot(), slog.New(slog.NewTextHandler(&logs, nil)), "conn")
			if tc.sql == "" {
				require.Empty(t, sqls)
				require.Contains(t, logs.String(), "legacy")
			} else {
				require.Equal(t, []string{tc.sql}, sqls)
			}
		})
	}
}

func TestReview_settings_canonical(t *testing.T) {
	for _, tc := range []struct {
		key    string
		values []string
		sql    string
	}{
		{"application_name", []string{"'literal'"}, "SET application_name TO '''literal'''"},
		{"application_name", []string{""}, "SET application_name TO ''"},
		{"application_name", []string{`back\'slash`}, `SET application_name TO E'back\\''slash'`},
		{"search_path", []string{"a,b", `say"hi`, "$user"}, `SET search_path TO "a,b", "say""hi", "$user"`},
		{"search_path", []string{""}, "SET search_path TO ''"},
		{"statement_timeout", []string{"500ms"}, "SET statement_timeout = '500ms'"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			var executed []string
			snap := session.NewSettingsSnapshot()
			replayCanonicalSessionSettings(context.Background(), map[string]common.CanonicalSessionSetting{tc.key: decodedSetting(tc.values...)}, func(_ context.Context, sql string) error {
				executed = append(executed, sql)
				return nil
			}, snap, slog.New(slog.DiscardHandler), "conn")
			require.Equal(t, []string{tc.sql}, executed)
			require.Equal(t, strings.Join(tc.values, ", "), snap.All()[tc.key])
		})
	}
	var executed []string
	replayCanonicalSessionSettings(context.Background(), map[string]common.CanonicalSessionSetting{
		"role": decodedSetting("admin"), "unknown": decodedSetting("bad"),
		"search_path": decodedSetting("valid", ""), "timezone": {Values: []string{"UTC"}, Provenance: "sql"},
	}, func(_ context.Context, sql string) error { executed = append(executed, sql); return nil }, session.NewSettingsSnapshot(), slog.New(slog.DiscardHandler), "conn")
	require.Empty(t, executed)
}

type reviewSettingsSession struct {
	drivers.Session
	value            string
	showErr          error
	setErr           error
	panicSet         bool
	entered, release chan struct{}
	queries          []string
}

func (*reviewSettingsSession) ID() models.SessionID                    { return 1 }
func (*reviewSettingsSession) CurrentTransaction() drivers.Transaction { return nil }
func (*reviewSettingsSession) ListSchemas(context.Context, string) ([]models.Schema, error) {
	return nil, nil
}

func (*reviewSettingsSession) ListTables(context.Context, string) ([]*models.Table, error) {
	return nil, nil
}
func (*reviewSettingsSession) Close() error { return nil }
func (s *reviewSettingsSession) Execute(ctx context.Context, q models.Query) (models.Result, error) {
	s.queries = append(s.queries, q.SQL)
	if strings.HasPrefix(q.SQL, "SET ") {
		if s.panicSet {
			panic("synthetic SET panic")
		}
		if s.entered != nil {
			close(s.entered)
			select {
			case <-s.release:
			case <-ctx.Done():
				return models.Result{}, ctx.Err()
			}
		}
		return models.Result{}, s.setErr
	}
	if s.showErr != nil {
		return models.Result{}, s.showErr
	}
	return models.Result{Rows: []*models.Row{{Values: []any{s.value}}}}, nil
}

type reviewSettingsConn struct {
	drivers.Connection
	sess drivers.Session
}

func (c *reviewSettingsConn) AcquireSession(context.Context) (drivers.Session, error) {
	return c.sess, nil
}
func (*reviewSettingsConn) Close() error { return nil }

type reviewSettingsDriver struct {
	drivers.Driver
	conn drivers.Connection
}

var reviewSettingsDriverCount atomic.Uint64

func reviewSettingsRegister(t *testing.T, factory drivers.Factory) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", t.Name(), reviewSettingsDriverCount.Add(1))
	drivers.Register(name, factory)
	return name
}

func (d *reviewSettingsDriver) Open(context.Context, drivers.ConnectionProfile, drivers.ProgressReporter) (drivers.Connection, error) {
	return d.conn, nil
}

func reviewSettingsGui(t *testing.T, driverName, dsn string) (*Gui, *common.AppStateStore, drivers.Connection, string) {
	t.Helper()
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "state.yml")
	store := common.NewAppStateStore(fs, path, common.DefaultClock())
	g := NewGui(Deps{
		Common: common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs), Store: store,
		ConnectionsPath:     filepath.Join(t.TempDir(), "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		HistoryProvider:     func() (*query.History, error) { return query.New(filepath.Join(filepath.Dir(path), "history.sqlite")) },
	})
	conn, _, err := g.connectHelper.Connect(context.Background(), &models.Connection{Name: "conn", Driver: driverName, DSN: dsn}, nil)
	require.NoError(t, err)
	inner, err := conn.AcquireSession(context.Background())
	require.NoError(t, err)
	g.queryState.activeSQLSession = session.New(conn, inner, session.Options{})
	g.connectionState.activeConnID = "conn"
	t.Cleanup(func() {
		g.spinnerState.workersWG.Wait()
		_, loadErr := store.CanonicalSessionSettingsSnapshot("conn")
		if loadErr != nil {
			require.ErrorIs(t, g.Close(), loadErr)
			require.NoError(t, store.Close())
		} else {
			require.NoError(t, g.Close())
			require.NoError(t, store.Close())
		}
	})
	return g, store, conn, path
}

func TestReview_settings_writer(t *testing.T) {
	for _, scenario := range []string{"decoded", "empty", "readback_error", "set_error", "captured_connection", "reset"} {
		t.Run(scenario, func(t *testing.T) {
			fake := &reviewSettingsSession{value: "my'app"}
			if scenario == "empty" {
				fake.value = ""
			}
			if scenario == "readback_error" {
				fake.showErr = errors.New("synthetic readback error")
			}
			if scenario == "set_error" {
				fake.setErr = errors.New("synthetic SET error")
			}
			if scenario == "captured_connection" {
				fake.entered, fake.release = make(chan struct{}), make(chan struct{})
			}
			name := reviewSettingsRegister(t, func(context.Context) (drivers.Driver, error) {
				return &reviewSettingsDriver{conn: &reviewSettingsConn{sess: fake}}, nil
			})
			g, store, _, _ := reviewSettingsGui(t, name, "")
			if fake.release != nil {
				t.Cleanup(func() {
					select {
					case <-fake.release:
					default:
						close(fake.release)
					}
				})
			}
			require.NoError(t, store.SetCanonicalSessionSetting("conn", "application_name", decodedSetting("old")))
			oldSession := g.queryState.activeSQLSession
			if scenario == "reset" {
				require.NoError(t, g.handleResetEx([]string{"application_name"}, commands.ExecCtx{}))
			} else {
				require.NoError(t, g.handleSetEx([]string{"application_name", "TO", "'my''app'"}, commands.ExecCtx{}))
			}
			if scenario == "captured_connection" {
				select {
				case <-fake.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("SET did not enter")
				}
				g.connectionState.activeConnID = "other"
				g.queryState.activeSQLSession = session.New(nil, &reviewSettingsSession{}, session.Options{})
				close(fake.release)
			}
			g.spinnerState.workersWG.Wait()
			saved, err := store.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			switch scenario {
			case "readback_error", "reset":
				require.NotContains(t, saved, "application_name")
			case "set_error":
				require.Equal(t, decodedSetting("old"), saved["application_name"])
			default:
				require.Equal(t, decodedSetting(fake.value), saved["application_name"])
				require.Equal(t, fake.value, oldSession.SettingsSnapshot().All()["application_name"])
				require.Empty(t, store.LastSessionSettingsSnapshot("conn"))
			}
			other, err := store.CanonicalSessionSettingsSnapshot("other")
			require.NoError(t, err)
			require.Empty(t, other)
			if scenario == "captured_connection" {
				require.Empty(t, g.queryState.activeSQLSession.SettingsSnapshot().All())
				require.NoError(t, oldSession.Close())
			}
		})
	}
}

func TestReview_settings_live(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires run-owned PGSAVVY_TEST_PG fixture")
	}
	name := reviewSettingsRegister(t, pg.New(nil))
	g, store, conn, path := reviewSettingsGui(t, name, dsn)
	for _, sql := range []string{`CREATE SCHEMA IF NOT EXISTS "a,b"`, `CREATE SCHEMA IF NOT EXISTS "say""hi"`} {
		_, err := g.queryState.activeSQLSession.Execute(context.Background(), models.Query{SQL: sql})
		require.NoError(t, err)
	}
	for _, args := range [][]string{
		{"timezone", "TO", "'America/New_York'"},
		{"application_name", "TO", "'my''app'"},
		{"search_path", "TO", `"a,b",`, `"say""hi",`, `"$user",`, "public"},
		{"statement_timeout", "TO", "500"},
	} {
		require.NoError(t, g.handleSetEx(args, commands.ExecCtx{}))
		g.spinnerState.workersWG.Wait()
	}
	values, err := store.CanonicalSessionSettingsSnapshot("conn")
	require.NoError(t, err)
	require.Equal(t, decodedSetting("America/New_York"), values["timezone"])
	require.Equal(t, decodedSetting("my'app"), values["application_name"])
	require.Equal(t, decodedSetting("a,b", `say"hi`, "$user", "public"), values["search_path"])
	require.Equal(t, decodedSetting("500ms"), values["statement_timeout"])
	require.NoError(t, store.Save())
	loaded := common.NewAppStateStore(afero.NewOsFs(), path, common.DefaultClock())
	defer func() { require.NoError(t, loaded.Close()) }()
	require.NoError(t, loaded.Load())
	inner, err := conn.AcquireSession(context.Background())
	require.NoError(t, err)
	reconnected := session.New(conn, inner, session.Options{})
	defer func() { require.NoError(t, reconnected.Close()) }()
	replayGui := &Gui{deps: Deps{Common: g.deps.Common, Store: loaded}}
	hint := (&connectInvoker{g: replayGui}).restoreSessionSettings(context.Background(), reconnected, "conn")
	require.Contains(t, hint, "timezone=America/New_York")
	for key, want := range values {
		got, err := readTrackedSetting(context.Background(), reconnected, key)
		require.NoError(t, err)
		require.Equal(t, want, got, key)
	}
	res, err := reconnected.Execute(context.Background(), models.Query{SQL: "SELECT current_schema()"})
	require.NoError(t, err)
	require.Equal(t, "a,b", res.Rows[0].Values[0])
	_, err = reconnected.Execute(context.Background(), models.Query{SQL: "SET standard_conforming_strings TO off"})
	require.NoError(t, err)
	for _, tc := range []struct {
		key, token string
		want       common.CanonicalSessionSetting
	}{
		{"application_name", `E'back\\''slash'`, decodedSetting(`back\'slash`)},
		{"application_name", "''", decodedSetting("")},
		{"search_path", "''", decodedSetting("")},
	} {
		require.NoError(t, g.handleSetEx([]string{tc.key, "TO", tc.token}, commands.ExecCtx{}))
		g.spinnerState.workersWG.Wait()
		require.NoError(t, store.Save())
		require.NoError(t, loaded.Load())
		(&connectInvoker{g: replayGui}).restoreSessionSettings(context.Background(), reconnected, "conn")
		got, err := readTrackedSetting(context.Background(), reconnected, tc.key)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestReview_settings_restoreStore(t *testing.T) {
	for _, scenario := range []string{"canonical_precedence", "unknown_version", "read_error"} {
		t.Run(scenario, func(t *testing.T) {
			fake := &reviewSettingsSession{}
			name := reviewSettingsRegister(t, func(context.Context) (drivers.Driver, error) {
				return &reviewSettingsDriver{conn: &reviewSettingsConn{sess: fake}}, nil
			})
			g, store, _, path := reviewSettingsGui(t, name, "")
			switch scenario {
			case "canonical_precedence":
				require.NoError(t, store.SetCanonicalSessionSetting("conn", "application_name", decodedSetting("")))
				require.NoError(t, store.SetCanonicalSessionSetting("conn", "statement_timeout", decodedSetting("30s")))
				store.MutateAndSave(func(a *common.AppState) {
					a.LastSessionSettings = map[string]map[string]string{"conn": {"application_name": "old", "statement_timeout": "1s", "timezone": "'UTC'", "role": "admin", "unknown": "ignored"}}
					a.StatementTimeoutOverride = map[string]string{"conn": "2s"}
				})
			case "unknown_version":
				require.NoError(t, os.WriteFile(path, []byte("last_session_settings:\n  conn:\n    timezone: UTC\ncanonical_session_settings:\n  version: 99\n"), 0o600))
				require.ErrorIs(t, store.Load(), common.ErrUnsupportedSessionSettingsVersion)
			default:
				require.NoError(t, os.WriteFile(path, []byte("canonical_session_settings: [\n"), 0o600))
				require.Error(t, store.Load())
			}
			inv := &connectInvoker{g: g}
			hint := inv.restoreSessionSettings(context.Background(), g.queryState.activeSQLSession, "conn")
			if scenario == "canonical_precedence" {
				require.Equal(t, []string{"SET application_name TO ''", "SET statement_timeout = '30s'", "SET timezone TO 'UTC'"}, fake.queries)
				require.Contains(t, hint, "timezone=UTC")
			} else {
				require.Empty(t, fake.queries)
				require.Empty(t, hint)
			}
		})
	}
}

func TestReview_settings_timeoutPrompt(t *testing.T) {
	for _, scenario := range []string{"success", "set_error", "readback_error"} {
		t.Run(scenario, func(t *testing.T) {
			fake := &reviewSettingsSession{value: "5s", entered: make(chan struct{}), release: make(chan struct{})}
			if scenario == "set_error" {
				fake.setErr = errors.New("synthetic SET failure")
			}
			if scenario == "readback_error" {
				fake.showErr = errors.New("synthetic SHOW failure")
			}
			name := reviewSettingsRegister(t, func(context.Context) (drivers.Driver, error) {
				return &reviewSettingsDriver{conn: &reviewSettingsConn{sess: fake}}, nil
			})
			g, store, _, _ := reviewSettingsGui(t, name, "")
			t.Cleanup(func() {
				select {
				case <-fake.release:
				default:
					close(fake.release)
				}
			})
			require.NoError(t, g.UseDriverForTest(testfake.NewRecorderGuiDriver()))
			g.queryState.queryRunner.Bind(g.queryState.activeSQLSession, drivers.Capabilities{})
			cmd, ok := g.keybindingSystem.cmdRegistry.Get(commands.StatementTimeoutSet)
			require.True(t, ok)
			require.NoError(t, cmd.Handler(commands.ExecCtx{}))
			require.NoError(t, g.promptHelp.Submit("5s"))
			select {
			case <-fake.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("timeout SET did not enter")
			}
			premature := store.StatementTimeoutOverrideValue("conn")
			close(fake.release)
			g.spinnerState.workersWG.Wait()
			require.Empty(t, premature, "prompt must not persist ahead of SET completion")
			require.Empty(t, store.StatementTimeoutOverrideValue("conn"))
			saved, err := store.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			if scenario == "success" {
				require.Equal(t, decodedSetting("5s"), saved["statement_timeout"])
			} else {
				require.NotContains(t, saved, "statement_timeout")
			}
		})
	}
}
