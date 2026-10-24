//go:build integration && (linux || darwin)

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
)

type fullProcessPG struct {
	*firstProcessFixture
	observer          *pgx.Conn
	role, denied, gid string
}

type fullProcessCleanupDB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Cleanup cannot depend on the restricted observer having connected: setup can
// fail after creating only some resources. The independent admin owns cleanup.
func fullProcessCleanup(admin fullProcessCleanupDB, observer *pgx.Conn, schema, gid string, roles ...string) []error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var failures []error
	record := func(stage string, err error) {
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", stage, err))
		}
	}
	if observer != nil {
		record("close observer", observer.Close(ctx))
	}
	var exists bool
	probeErr := admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_prepared_xacts WHERE gid=$1)", gid).Scan(&exists)
	record("inspect owned prepared transaction", probeErr)
	if exists || probeErr != nil {
		_, err := admin.Exec(ctx, "ROLLBACK PREPARED '"+strings.ReplaceAll(gid, "'", "''")+"'")
		record("rollback owned prepared transaction", err)
	}
	_, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	record("drop owned schema", err)
	for _, role := range roles {
		_, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
		record("drop owned role", err)
	}
	return failures
}

type fullProcessCleanupProbe struct {
	exists bool
	err    error
}

func (p fullProcessCleanupProbe) Scan(dest ...any) error {
	if p.err == nil {
		*dest[0].(*bool) = p.exists
	}
	return p.err
}

type fullProcessCleanupStub struct {
	probe      fullProcessCleanupProbe
	failPrefix string
	statements []string
	probeGID   any
}

func (s *fullProcessCleanupStub) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	s.statements = append(s.statements, sql)
	s.probeGID = args[0]
	return s.probe
}

func (s *fullProcessCleanupStub) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	s.statements = append(s.statements, sql)
	if s.failPrefix != "" && strings.HasPrefix(sql, s.failPrefix) {
		return pgconn.CommandTag{}, errors.New("injected cleanup failure")
	}
	return pgconn.CommandTag{}, nil
}

func fullProcessFixture(t *testing.T) *fullProcessPG {
	t.Helper()
	f := &fullProcessPG{firstProcessFixture: firstProcess(t, false)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, firstProcessDSN)
	require.NoError(t, err, "designated fixture unreachable")
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var prepared int
	require.NoError(t, admin.QueryRow(ctx, "SELECT current_setting('max_prepared_transactions')::int").Scan(&prepared))
	require.GreaterOrEqual(t, prepared, 20, "designated fixture must enable prepared transactions")
	f.schema = fmt.Sprintf("full_process_%d_%d", os.Getpid(), time.Now().UnixNano())
	login := f.schema + "_login"
	f.role, f.denied, f.gid = f.schema+"_owner", f.schema+"_denied", f.schema+"_prepared"
	id := func(value string) string { return pgx.Identifier{value}.Sanitize() }
	t.Cleanup(func() {
		for _, err := range fullProcessCleanup(admin, f.observer, f.schema, f.gid, login, f.denied, f.role) {
			t.Errorf("test-owned fixture cleanup: %v", err)
		}
	})
	for _, sql := range []string{
		"CREATE ROLE " + id(login) + " LOGIN NOINHERIT PASSWORD 'disposable-process-test'",
		"CREATE ROLE " + id(f.role) + " NOLOGIN",
		"CREATE ROLE " + id(f.denied) + " NOLOGIN",
		"GRANT " + id(f.role) + ", " + id(f.denied) + " TO " + id(login),
		"CREATE SCHEMA " + id(f.schema) + " AUTHORIZATION " + id(f.role),
	} {
		_, err := admin.Exec(ctx, sql)
		require.NoError(t, err, "restricted fixture provisioning")
	}
	u, err := url.Parse(firstProcessDSN)
	require.NoError(t, err)
	u.User = url.UserPassword(login, "disposable-process-test")
	f.observer, err = pgx.Connect(ctx, u.String())
	require.NoError(t, err)
	_, err = f.observer.Exec(ctx, "SET ROLE "+id(f.role)+"; CREATE TABLE "+id(f.schema)+".effects(v int)")
	require.NoError(t, err)
	f.profile = models.Connection{Name: "Exact.Profile", Driver: "postgres", DSN: u.String(), Role: f.role, CLIAccess: models.CLIAccessFull, ConfirmWrites: true, ConfirmDDL: true}
	f.save(t, f.profile)
	return f
}

func (f *fullProcessPG) query(t *testing.T, sql string, want int, extra ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	args := append([]string{"query", "--connection=Exact.Profile", "--file=-", "--schema=" + f.schema, "--approve", "--confirm-connection=Exact.Profile"}, extra...)
	command := f.command(ctx, args...)
	command.Stdin = strings.NewReader(sql)
	var out, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostic
	exit := processExit(command.Run())
	require.Equal(t, want, exit, "process exit; diagnostics=%s", diagnostic.String())
	require.LessOrEqual(t, out.Len(), MaxOutputBytes)
	require.True(t, bytes.HasSuffix(out.Bytes(), []byte("\n")))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &doc), "exactly one JSON document")
	require.Equal(t, float64(1), doc["schema_version"])
	require.Equal(t, want == 0, doc["ok"])
	for _, sentinel := range []string{"disposable-process-test", "password", "INSERT INTO", "pg_terminate_backend"} {
		require.NotContains(t, out.String(), sentinel)
		require.NotContains(t, diagnostic.String(), sentinel)
	}
	if diagnostic.Len() > 0 {
		var safe map[string]any
		require.NoError(t, json.Unmarshal(diagnostic.Bytes(), &safe))
		require.Len(t, safe, 2)
		require.Equal(t, float64(exit), safe["exit"])
		require.Equal(t, doc["command"], safe["command"])
	}
	return doc
}

func (f *fullProcessPG) count(t *testing.T, value int) int {
	t.Helper()
	var count int
	require.NoError(t, f.observer.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{f.schema}.Sanitize()+".effects WHERE v=$1", value).Scan(&count))
	return count
}

func TestMachineFullQueryProcessTransactions(t *testing.T) {
	t.Run("cleanup_without_observer_attempts_every_owned_resource", func(t *testing.T) {
		for _, test := range []struct {
			name, failPrefix     string
			prepared, probeFails bool
			failures             int
		}{
			{name: "partial_provision"},
			{name: "prepared", prepared: true},
			{name: "probe_failure", probeFails: true, failures: 1},
			{name: "rollback_failure", prepared: true, failPrefix: "ROLLBACK", failures: 1},
			{name: "schema_failure", failPrefix: "DROP SCHEMA", failures: 1},
			{name: "role_failures", failPrefix: "DROP ROLE", failures: 3},
		} {
			t.Run(test.name, func(t *testing.T) {
				stub := &fullProcessCleanupStub{probe: fullProcessCleanupProbe{exists: test.prepared}, failPrefix: test.failPrefix}
				if test.probeFails {
					stub.probe.err = errors.New("injected probe failure")
				}
				failures := fullProcessCleanup(stub, nil, `owned"schema`, "owned'gid", "owned_login", "owned_denied", `owned"role`)
				require.Len(t, failures, test.failures)
				require.Equal(t, "owned'gid", stub.probeGID)
				want := []string{"SELECT EXISTS(SELECT 1 FROM pg_prepared_xacts WHERE gid=$1)"}
				if test.prepared || test.probeFails {
					want = append(want, "ROLLBACK PREPARED 'owned''gid'")
				}
				want = append(want, `DROP SCHEMA IF EXISTS "owned""schema" CASCADE`, `DROP ROLE IF EXISTS "owned_login"`, `DROP ROLE IF EXISTS "owned_denied"`, `DROP ROLE IF EXISTS "owned""role"`)
				require.Equal(t, want, stub.statements, "cleanup must continue after each failure, using only exact quoted owned names")
			})
		}
	})
	f := fullProcessFixture(t)
	for _, test := range []struct {
		name, sql, status string
		exit, value, rows int
	}{
		{"commit", "INSERT INTO effects VALUES(1)", "completed", 0, 1, 1},
		{"rollback", "BEGIN; INSERT INTO effects VALUES(2); ROLLBACK", "completed", 0, 2, 0},
		{"implicit_atomic", "INSERT INTO effects VALUES(3); SELECT 1/0", "failed", 5, 3, 0},
		{"partial", "BEGIN; INSERT INTO effects VALUES(4); COMMIT; SELECT 1/0", "failed", 5, 4, 1},
		{"incomplete", "BEGIN; INSERT INTO effects VALUES(5)", "failed", 5, 5, 0},
		{"overflow_before_commit", "BEGIN; SELECT repeat('x',65537); INSERT INTO effects VALUES(6); COMMIT", "completed", 7, 6, 1},
		{"document_overflow_before_commit", "BEGIN; SELECT repeat(chr(1),65536) FROM generate_series(1,20); INSERT INTO effects VALUES(11); COMMIT", "completed", 7, 11, 1},
		{"disconnect_after_commit", "BEGIN; INSERT INTO effects VALUES(7); COMMIT; RESET ROLE; SELECT pg_terminate_backend(pg_backend_pid())", "unknown", 5, 7, 1},
		{"protocol_abort", "SELECT repeat('x',9*1024*1024)", "unknown", 5, 8, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := f.query(t, test.sql, test.exit)
			execution := doc["execution"].(map[string]any)
			require.Equal(t, test.status, execution["status"])
			require.Equal(t, true, execution["effects_may_have_occurred"])
			if test.exit != 0 {
				require.Nil(t, doc["data"])
			}
			require.Equal(t, test.rows, f.count(t, test.value), "independent restricted observer")
		})
	}
	t.Run("row_and_result_caps_drain", func(t *testing.T) {
		doc := f.query(t, "BEGIN; SELECT generate_series(1,20); INSERT INTO effects VALUES(12); COMMIT", 0, "--max-rows=1")
		require.Equal(t, "row_limit", doc["truncation_reason"])
		require.Equal(t, 1, f.count(t, 12))
		doc = f.query(t, "BEGIN; "+strings.Repeat("SELECT 1;", 260)+"INSERT INTO effects VALUES(13); COMMIT", 0)
		require.Equal(t, "result_limit", doc["truncation_reason"])
		require.Len(t, doc["data"].(map[string]any)["results"], 256)
		require.Equal(t, 1, f.count(t, 13))
	})
	t.Run("parameterized_single_statement", func(t *testing.T) {
		params := filepath.Join(f.root, "params.json")
		require.NoError(t, os.WriteFile(params, []byte("[14]"), 0o600))
		f.query(t, "INSERT INTO effects VALUES($1::int)", 0, "--params-file="+params)
		require.Equal(t, 1, f.count(t, 14))
	})
	t.Run("prepared_recovery", func(t *testing.T) {
		doc := f.query(t, "BEGIN; INSERT INTO effects VALUES(9); PREPARE TRANSACTION '"+f.gid+"'", 0)
		require.Equal(t, true, doc["execution"].(map[string]any)["prepared_transactions_may_remain"])
		var owner string
		require.NoError(t, f.observer.QueryRow(context.Background(), "SELECT owner::text FROM pg_prepared_xacts WHERE gid=$1", f.gid).Scan(&owner))
		require.Equal(t, f.role, owner)
		require.Zero(t, f.count(t, 9))
		f.query(t, "COMMIT PREPARED '"+f.gid+"'", 0)
		require.Equal(t, 1, f.count(t, 9))
		f.query(t, "BEGIN; INSERT INTO effects VALUES(10); PREPARE TRANSACTION '"+f.gid+"'", 0)
		f.query(t, "ROLLBACK PREPARED '"+f.gid+"'", 0)
		require.Zero(t, f.count(t, 10))
	})
}

func TestMachineFullQueryProcessDeliveryAndDeadline(t *testing.T) {
	f := fullProcessFixture(t)
	t.Run("deadline_after_commit", func(t *testing.T) {
		doc := f.query(t, "BEGIN; INSERT INTO effects VALUES(30); COMMIT; SELECT pg_sleep(2)", 6, "--timeout=400ms")
		require.Equal(t, "unknown", doc["execution"].(map[string]any)["status"])
		require.Equal(t, true, doc["execution"].(map[string]any)["effects_may_have_occurred"])
		require.Equal(t, 1, f.count(t, 30))
	})
	t.Run("broken_stdout_after_commit", func(t *testing.T) {
		read, write, err := os.Pipe()
		require.NoError(t, err)
		require.NoError(t, read.Close())
		defer func() { _ = write.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		command := f.command(ctx, "query", "--connection=Exact.Profile", "--file=-", "--schema="+f.schema, "--approve", "--confirm-connection=Exact.Profile")
		command.Stdin = strings.NewReader("INSERT INTO effects VALUES(31)")
		command.Stdout = write
		var diagnostic bytes.Buffer
		command.Stderr = &diagnostic
		require.Equal(t, ExitResource, processExit(command.Run()))
		require.Equal(t, 1, f.count(t, 31), "failed delivery must not imply rollback or replay")
	})
}

func TestMachineFullQueryProcessRestrictedPolicy(t *testing.T) {
	f := fullProcessFixture(t)
	t.Run("identities", func(t *testing.T) {
		doc := f.query(t, "SELECT current_user,session_user", 0)
		row := doc["data"].(map[string]any)["results"].([]any)[0].(map[string]any)["rows"].([]any)[0].([]any)
		require.Equal(t, []any{f.role, f.schema + "_login"}, row)
	})
	t.Run("guarded_readonly_precedence", func(t *testing.T) {
		profile := f.profile
		profile.ReadOnly = true
		f.save(t, profile)
		defer f.save(t, f.profile)
		for _, sql := range []string{"INSERT INTO effects VALUES(20)", "SET default_transaction_read_only=off; INSERT INTO effects VALUES(20)"} {
			doc := f.query(t, sql, 2)
			require.Equal(t, string(UnsupportedSQL), doc["error"].(map[string]any)["code"])
			require.Nil(t, doc["execution"])
		}
		doc := f.query(t, "SELECT 1", 0)
		require.Contains(t, doc["data"].(map[string]any), "columns")
		require.Zero(t, f.count(t, 20))
	})
	t.Run("server_readonly_default", func(t *testing.T) {
		profile := f.profile
		u, err := url.Parse(profile.DSN)
		require.NoError(t, err)
		q := u.Query()
		q.Set("default_transaction_read_only", "on")
		u.RawQuery = q.Encode()
		profile.DSN = u.String()
		f.save(t, profile)
		defer f.save(t, f.profile)
		f.query(t, "INSERT INTO effects VALUES(21)", 5)
		require.Zero(t, f.count(t, 21))
	})
	t.Run("permissions_and_role_changes", func(t *testing.T) {
		profile := f.profile
		profile.Role = f.denied
		f.save(t, profile)
		f.query(t, "INSERT INTO "+pgx.Identifier{f.schema}.Sanitize()+".effects VALUES(22)", 5)
		f.query(t, "CREATE DATABASE "+pgx.Identifier{f.schema + "_forbidden"}.Sanitize(), 5)
		f.save(t, f.profile)
		f.query(t, "RESET ROLE; INSERT INTO "+pgx.Identifier{f.schema}.Sanitize()+".effects VALUES(22)", 5)
		f.query(t, "SET ROLE "+pgx.Identifier{f.denied}.Sanitize()+"; INSERT INTO "+pgx.Identifier{f.schema}.Sanitize()+".effects VALUES(22)", 5)
		require.Zero(t, f.count(t, 22))
	})
	t.Run("native_maintenance_limits", func(t *testing.T) {
		f.query(t, "VACUUM effects", 0)
		f.query(t, "SELECT 1; VACUUM effects", 5)
	})
}
