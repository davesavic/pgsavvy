//go:build integration && (linux || darwin)

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Unlike the general live suite, these tests refuse arbitrary PGSAVVY_TEST_PG.
// The native runner owns the cluster, role, schema, socket and teardown.
type temporaryLiveOwnership struct {
	Token    string `json:"token"`
	DSN      string `json:"observer_dsn"`
	SystemID string `json:"system_identifier"`
	DataDir  string `json:"data_directory"`
}

func temporaryLiveObserverConfig(root, token, dsn string) (*pgx.ConnConfig, temporaryLiveOwnership, error) {
	var owner temporaryLiveOwnership
	denied := errors.New("owned native observer authorization failed")
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "pgsavvy-native.") || token == "" {
		return nil, owner, denied
	}
	path := filepath.Join(root, "temporary-owner")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8192 {
		return nil, owner, denied
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Token != token || owner.DSN != dsn {
		return nil, owner, denied
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	identifier, idErr := strconv.ParseUint(owner.SystemID, 10, 64)
	if err != nil || idErr != nil || identifier == 0 || owner.DataDir != directory {
		return nil, owner, denied
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Database != "pgsavvy_test" || cfg.User != "pgsavvy" || cfg.Password == "" {
		return nil, owner, denied
	}
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.RuntimeParams = map[string]string{}
	cfg.ConnectTimeout = 5 * time.Second
	endpoint := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != endpoint {
			return nil, denied
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return cfg, owner, nil
}

func temporaryLiveVerifyObserver(ctx context.Context, observer *pgx.Conn, owner temporaryLiveOwnership) error {
	var identifier, directory, database, username string
	err := observer.QueryRow(ctx, "SELECT system_identifier::text,current_setting('data_directory'),current_database(),current_user FROM pg_control_system()").Scan(&identifier, &directory, &database, &username)
	if err != nil || identifier != owner.SystemID || directory != owner.DataDir || database != "pgsavvy_test" || username != "pgsavvy" {
		return errors.New("connected observer identity is not owned")
	}
	return nil
}

func temporaryLiveFixture(t *testing.T) (*pgx.Conn, []string) {
	t.Helper()
	root := os.Getenv("PGSAVVY_TEMP_FIXTURE_ROOT")
	token := os.Getenv("PGSAVVY_TEMP_FIXTURE_TOKEN")
	cfg, owner, err := temporaryLiveObserverConfig(root, token, os.Getenv("PGSAVVY_TEST_PG"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observer, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err, "owned observer connection failed")
	t.Cleanup(func() { require.NoError(t, observer.Close(context.Background())) })
	require.NoError(t, temporaryLiveVerifyObserver(ctx, observer, owner))
	passfile := os.Getenv("PGSAVVY_TEMP_FIXTURE_PASSFILE")
	require.Equal(t, filepath.Join(root, "temporary.pass"), passfile)
	args := []string{"--temporary", "--port", strconv.Itoa(int(observer.Config().Port)), "--database", observer.Config().Database, "--username", "pgsavvy_temporary", "--passfile", passfile}
	cleanTemporaryEnvironment(t)
	return observer, args
}

func TestTemporaryCLIObserverOwnership(t *testing.T) {
	root, err := os.MkdirTemp(t.TempDir(), "pgsavvy-native.")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "data"), 0o700))
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	require.NoError(t, err)
	const dsn = "postgres://pgsavvy:private-fixture@127.0.0.1:65432/pgsavvy_test?sslmode=disable"
	owner := temporaryLiveOwnership{Token: "private-token", DSN: dsn, SystemID: "1234567890123456789", DataDir: directory}
	data, err := json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	for _, wrong := range []string{
		strings.Replace(dsn, "127.0.0.1", "192.0.2.1", 1), strings.Replace(dsn, "65432", "65433", 1),
		strings.Replace(dsn, "pgsavvy_test", "unrelated", 1), strings.Replace(dsn, "private-fixture", "different", 1), dsn + "&options=-cstatement_timeout=0",
	} {
		cfg, _, err := temporaryLiveObserverConfig(root, owner.Token, wrong)
		require.Error(t, err)
		require.Nil(t, cfg, "mismatched DSN produced a connectable artifact")
	}
	cfg, _, err := temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.NoError(t, err)
	// The only executable network hook also rejects alternate addresses before dialing.
	conn, err := cfg.DialFunc(context.Background(), "tcp", "127.0.0.1:65433")
	require.Error(t, err)
	require.Nil(t, conn)
	_, _, err = temporaryLiveObserverConfig(root, "wrong-token", dsn)
	require.Error(t, err)
	owner.SystemID = ""
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "missing cluster identity was accepted")
	owner.SystemID, owner.DataDir = "1234567890123456789", filepath.Join(directory, "unrelated")
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "unowned data directory was accepted")
}

func temporaryLiveDispatch(t *testing.T, args []string, sql string, observePort ...*int) (map[string]any, int) {
	t.Helper()
	streams, out, diagnostic := testStreams()
	streams.In = io.NopCloser(strings.NewReader(sql))
	service := forbiddenTemporaryService(t)
	if len(observePort) > 0 {
		service.Options.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err == nil {
				*observePort[0] = conn.LocalAddr().(*net.TCPAddr).Port
			}
			return conn, err
		}
	}
	service.Options.Credentials.Keyring = func(context.Context, string) (string, error) {
		t.Fatal("temporary operation read saved keyring")
		return "", nil
	}
	_, exit := Dispatch(context.Background(), args, streams, map[string]Command{
		"query": QueryCommand(service), "schema": SchemaCommand(service), "connections": ConnectionsCommand(service),
	})
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &doc))
	for _, secret := range []string{os.Getenv("PGSAVVY_TEMP_FIXTURE_PASSFILE"), "pgsavvy-native.", "postgres://"} {
		require.NotContains(t, out.String()+diagnostic.String(), secret)
	}
	return doc, exit
}

func TestTemporaryCLIIntegration(t *testing.T) {
	observer, target := temporaryLiveFixture(t)
	t.Run("reject-connected-identity-mismatch", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.Error(t, temporaryLiveVerifyObserver(ctx, observer, temporaryLiveOwnership{SystemID: "not-owned"}))
	})
	count := func(id int) int {
		t.Helper()
		var n int
		require.NoError(t, observer.QueryRow(context.Background(), "SELECT count(*) FROM temporary_cli.effects WHERE id=$1", id).Scan(&n))
		return n
	}
	lastPort := 0
	query := func(sql string, extra ...string) (map[string]any, int) {
		t.Helper()
		args := append([]string{"query"}, target...)
		args = append(args, "--file", "-")
		lastPort = 0
		return temporaryLiveDispatch(t, append(args, extra...), sql, &lastPort)
	}
	t.Run("restricted-login", func(t *testing.T) {
		var unsafe bool
		require.NoError(t, observer.QueryRow(context.Background(), `SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls
OR EXISTS(SELECT 1 FROM pg_auth_members WHERE member=pg_roles.oid)
OR has_schema_privilege(rolname,'temporary_cli','CREATE') OR has_schema_privilege(rolname,'public','CREATE')
OR has_database_privilege(rolname,current_database(),'CREATE') OR has_database_privilege(rolname,current_database(),'TEMP')
FROM pg_roles WHERE rolname='pgsavvy_temporary'`).Scan(&unsafe))
		require.False(t, unsafe)
	})
	t.Run("all-six-routes-positive-no-saved-access", func(t *testing.T) {
		for _, prefix := range [][]string{
			{"query", "--file", "-"}, {"schema", "list"}, {"schema", "tables", "--schema", "temporary_cli"},
			{"schema", "describe", "--schema", "temporary_cli", "--table", "effects"},
			{"schema", "relationships", "--schema", "temporary_cli", "--table", "effects"}, {"connections", "test"},
		} {
			doc, exit := temporaryLiveDispatch(t, append(prefix, target...), "SELECT current_setting('transaction_read_only')")
			require.Equal(t, 0, exit, "route %s", prefix[0])
			require.Nil(t, doc["connection"])
			require.Equal(t, true, doc["target"].(map[string]any)["read_only"])
			if prefix[0] == "query" {
				require.Equal(t, []any{[]any{"on"}}, doc["data"].(map[string]any)["results"].([]any)[0].(map[string]any)["rows"])
			}
			if prefix[0] == "schema" {
				temporaryLiveSchemaProof(t, prefix[1], doc)
			}
			if prefix[0] == "connections" {
				require.Equal(t, true, doc["data"].(map[string]any)["connected"])
			}
		}
	})
	t.Run("parameterized-crud", func(t *testing.T) {
		params := filepath.Join(t.TempDir(), "params.json")
		require.NoError(t, os.WriteFile(params, []byte(`[1001,"seed"]`), 0o600))
		for i, sql := range []string{
			"INSERT INTO temporary_cli.effects VALUES($1::int,$2::text) RETURNING value",
			"UPDATE temporary_cli.effects SET value=$2::text || '-updated' WHERE id=$1::int RETURNING value",
			"SELECT value FROM temporary_cli.effects WHERE id=$1::int AND value<>$2::text",
			"DELETE FROM temporary_cli.effects WHERE id=$1::int AND value<>$2::text RETURNING id",
		} {
			extra := []string{"--params-file", params}
			if i != 2 {
				extra = append(extra, "--allow-write")
			}
			doc, exit := query(sql, extra...)
			require.Equal(t, 0, exit)
			require.Equal(t, "committed", doc["transaction_outcome"])
			results := doc["data"].(map[string]any)["results"].([]any)
			require.Len(t, results, 1)
			want := []string{"seed", "seed-updated", "seed-updated", "1001"}[i]
			require.Equal(t, []any{[]any{want}}, results[0].(map[string]any)["rows"])
			if i < 3 {
				var observed string
				require.NoError(t, observer.QueryRow(context.Background(), "SELECT value FROM temporary_cli.effects WHERE id=1001").Scan(&observed))
				require.Equal(t, want, observed)
			} else {
				require.Zero(t, count(1001))
			}
		}
	})
	t.Run("supported-multi-crud-commits-once", func(t *testing.T) {
		doc, exit := query(`INSERT INTO temporary_cli.effects VALUES(1030,'first') RETURNING id;
INSERT INTO temporary_cli.effects VALUES(1030,'conflict') ON CONFLICT(id) DO UPDATE SET value=excluded.value RETURNING value;
UPDATE temporary_cli.effects AS e SET value=s.value FROM (VALUES('from')) AS s(value) WHERE e.id=1030 RETURNING e.value;
WITH removed AS (DELETE FROM temporary_cli.effects WHERE id=1030 RETURNING id) SELECT id FROM removed;
INSERT INTO temporary_cli.effects VALUES(1031,'durable')`, "--allow-write")
		require.Zero(t, exit)
		require.Equal(t, "committed", doc["transaction_outcome"])
		require.Len(t, doc["data"].(map[string]any)["results"], 5)
		require.Zero(t, count(1030))
		require.Equal(t, 1, count(1031))
	})
	t.Run("no-opt-in-no-dispatch", func(t *testing.T) {
		doc, exit := query("INSERT INTO temporary_cli.effects VALUES(1002,'blocked')")
		require.Equal(t, int(ExitPolicy), exit)
		require.Equal(t, "not_started", doc["transaction_outcome"])
		require.Zero(t, count(1002))
	})
	t.Run("server-enforces-read-only-even-for-select", func(t *testing.T) {
		doc, exit := query("SELECT nextval('temporary_cli.dispatch_guard')")
		require.Equal(t, int(ExitDatabase), exit)
		require.Contains(t, []string{"rolled_back", "disposed"}, doc["transaction_outcome"])
		require.Equal(t, "25006", doc["error"].(map[string]any)["sqlstate"])
	})
	t.Run("unsupported-suffix-zero-dispatch", func(t *testing.T) {
		var before, after int64
		sequence := "SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM temporary_cli.dispatch_guard"
		require.NoError(t, observer.QueryRow(context.Background(), sequence).Scan(&before))
		doc, exit := query("INSERT INTO temporary_cli.effects VALUES(1003,nextval('temporary_cli.dispatch_guard')::text); CREATE TABLE forbidden(id int)", "--allow-write")
		require.Equal(t, int(ExitInput), exit)
		require.Equal(t, "not_started", doc["transaction_outcome"])
		require.NoError(t, observer.QueryRow(context.Background(), sequence).Scan(&after))
		require.Equal(t, before, after, "nontransactional dispatch sentinel advanced")
		require.Zero(t, count(1003))
	})
	for _, tc := range []struct {
		name, suffix, outcome string
		id                    int
		extra                 []string
	}{
		{"late-sql", "; SELECT 1/0", "rolled_back", 1010, nil},
		{"deferred-commit", "; INSERT INTO temporary_cli.deferred VALUES(1),(1)", "rolled_back", 1011, nil},
		{"result-validation", "; SELECT repeat('x',65537)", "disposed", 1012, nil},
		{"omitted-row-validation", "; SELECT 'small' UNION ALL SELECT repeat('x',65537)", "disposed", 1013, []string{"--max-rows", "1"}},
		{"cancellation", "; SELECT pg_sleep(5)", "disposed", 1014, []string{"--timeout", "500ms"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var before, after int64
			sequence := "SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM temporary_cli.rollback_attempts"
			require.NoError(t, observer.QueryRow(context.Background(), sequence).Scan(&before))
			extra := append([]string{"--allow-write"}, tc.extra...)
			doc, exit := query("INSERT INTO temporary_cli.effects VALUES("+strconv.Itoa(tc.id)+",nextval('temporary_cli.rollback_attempts')::text)"+tc.suffix, extra...)
			require.NotZero(t, exit)
			require.Equal(t, tc.outcome, doc["transaction_outcome"])
			require.Equal(t, true, doc["execution"].(map[string]any)["effects_may_have_occurred"], "scenario never dispatched")
			require.Positive(t, lastPort, "backend identity was not captured")
			require.Eventually(t, func() bool {
				var active int
				err := observer.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE usename='pgsavvy_temporary' AND client_addr='127.0.0.1'::inet AND client_port=$1", lastPort).Scan(&active)
				return err == nil && active == 0
			}, 3*time.Second, 10*time.Millisecond, "invocation backend still active")
			require.NoError(t, observer.QueryRow(context.Background(), sequence).Scan(&after))
			require.Equal(t, before+1, after, "nontransactional marker proves exactly one submitted INSERT executed")
			require.Zero(t, count(tc.id), "transactional row survived backend termination")
			if tc.name == "deferred-commit" {
				var n int
				require.NoError(t, observer.QueryRow(context.Background(), "SELECT count(*) FROM temporary_cli.deferred").Scan(&n))
				require.Zero(t, n)
			}
			t.Logf("rollback proof: id=%d marker_attempts=1 backend_terminated=true rows=0", tc.id)
		})
	}
	t.Run("truncation-commits", func(t *testing.T) {
		doc, exit := query("INSERT INTO temporary_cli.effects VALUES(1020,'durable'); SELECT 1 UNION ALL SELECT 2", "--allow-write", "--max-rows", "1")
		require.Zero(t, exit)
		require.Equal(t, "committed", doc["transaction_outcome"])
		require.Equal(t, true, doc["truncated"])
		require.Equal(t, "row_limit", doc["truncation_reason"])
		require.Equal(t, 1, count(1020))
	})
}

func temporaryLiveSchemaProof(t *testing.T, operation string, doc map[string]any) {
	t.Helper()
	data := doc["data"].(map[string]any)
	switch operation {
	case "list":
		var names []string
		for _, item := range data["items"].([]any) {
			names = append(names, item.(map[string]any)["name"].(string))
		}
		require.Contains(t, names, "temporary_cli")
	case "tables":
		require.Equal(t, "temporary_cli", data["schema"])
		var names []string
		for _, item := range data["items"].([]any) {
			relation := item.(map[string]any)
			require.Equal(t, "temporary_cli", relation["schema"])
			require.Equal(t, "table", relation["kind"])
			names = append(names, relation["name"].(string))
		}
		require.ElementsMatch(t, []string{"children", "deferred", "effects"}, names)
	case "describe", "relationships":
		relation := data["relation"].(map[string]any)
		require.Equal(t, "temporary_cli", relation["schema"])
		require.Equal(t, "effects", relation["name"])
		if operation == "describe" {
			columns := map[string]string{}
			for _, item := range data["columns"].([]any) {
				column := item.(map[string]any)
				columns[column["name"].(string)] = column["data_type"].(string)
			}
			require.Equal(t, map[string]string{"id": "integer", "value": "text"}, columns)
		} else {
			require.JSONEq(t, `[{"name":"children_parent_fkey","schema":"temporary_cli","table":"children","columns":["parent"],"ref_schema":"temporary_cli","ref_table":"effects","ref_columns":["id"],"on_update":"NO ACTION","on_delete":"NO ACTION"}]`, string(mustTemporaryJSON(t, data["inbound"])))
			require.Empty(t, data["outbound"])
		}
	}
}

func mustTemporaryJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestTemporaryCLILexicalPostgres(t *testing.T) {
	observer, target := temporaryLiveFixture(t)
	// Expected values/results are compared with the live server executing the
	// exact bytes; rejected forms are only parsed in a rolled-back observer tx.
	for _, tc := range []struct {
		name, sql string
		accepted  bool
	}{
		{"plain-backslash", `SELECT 'plain\path'`, true},
		{"escaped-quote", `SELECT E'it\'s; fine'`, true},
		{"escaped-unicode", `SELECT E'\n\t\x41\101\u00e9'`, true},
		{"unicode-dollar-tag", "SELECT $タグ$'; /* ignored */$タグ$", true},
		{"quoted-unicode", `SELECT 'é; --', '日本語' AS "日本語"`, true},
		{"nested-comment", "/* outer /* inner ; */ outer */ SELECT 1", true},
		{"crlf", "SELECT 1 -- comment\r\n; SELECT 2", true},
		{"bare-cr", "SELECT 1\r; SELECT 2", true},
		{"bare-cr-comment", "SELECT 1 -- hidden\r; DELETE FROM temporary_cli.effects", false},
		{"escape-a", `SELECT E'\a'`, false},
		{"escape-v", `SELECT E'\v'`, false},
		{"escape-uppercase-x", `SELECT E'\X41'`, false},
		{"bit", `SELECT B'101'`, false},
		{"hex", `SELECT X'41'`, false},
		{"national", `SELECT N'text'`, false},
		{"unicode-prefix", `SELECT U&'d\0061t'`, false},
		{"adjacent-strings", "SELECT 'a'\n'b'", false},
		{"vertical-tab", "SELECT 1\v", false},
		{"unicode-casefold", "SELECT 1 AS İD", false},
		{"nul-escape", `SELECT E'\000'`, false},
		{"invalid-utf8-escape", `SELECT E'\xff'`, false},
		{"invalid-surrogate", `SELECT E'\ud800'`, false},
		{"parameter-junk", `SELECT $1junk`, false},
		{"numeric-junk", `SELECT 1_foo`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"query"}, target...)
			doc, exit := temporaryLiveDispatch(t, append(args, "--file", "-", "--allow-write"), tc.sql)
			if !tc.accepted {
				require.Equal(t, int(ExitInput), exit)
				require.Equal(t, "not_started", doc["transaction_outcome"])
				// A live parse records the PostgreSQL side of each rejected lexical
				// difference without executing even a valid modifying suffix.
				tx, err := observer.Begin(context.Background())
				require.NoError(t, err)
				_, parseErr := tx.Conn().PgConn().Prepare(context.Background(), "", tc.sql, nil)
				t.Logf("PostgreSQL parse accepted=%t; temporary validator rejected before dispatch", parseErr == nil)
				require.NoError(t, tx.Rollback(context.Background()))
				return
			}
			require.Zero(t, exit)
			results := doc["data"].(map[string]any)["results"].([]any)
			reader := observer.PgConn().Exec(context.Background(), tc.sql)
			i := 0
			for reader.NextResult() {
				result := reader.ResultReader().Read()
				require.NoError(t, result.Err)
				require.Less(t, i, len(results))
				wire := results[i].(map[string]any)
				require.Equal(t, result.CommandTag.String(), wire["command_tag"])
				want := make([][]*string, len(result.Rows))
				for row, values := range result.Rows {
					for _, value := range values {
						text := string(value)
						want[row] = append(want[row], &text)
					}
				}
				require.JSONEq(t, string(mustTemporaryJSON(t, want)), string(mustTemporaryJSON(t, wire["rows"])))
				i++
			}
			require.NoError(t, reader.Close())
			require.Equal(t, len(results), i)
		})
	}
}
