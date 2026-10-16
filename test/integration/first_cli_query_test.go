//go:build integration && linux

package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/cli"
	"github.com/davesavic/pgsavvy/pkg/models"
)

type firstOutput struct{ bytes.Buffer }

func (*firstOutput) Close() error { return nil }

func firstMachineFixture(t *testing.T) (cli.InvocationService, *pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv(envDSN)
	if dsn == "" || pgProbeErr != nil || pgStaleErr != nil {
		t.Fatal("FIRST requires a reachable designated PostgreSQL fixture; missing/stale fixtures do not skip")
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
	home := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, filepath.Join(home, name))
	}
	t.Setenv("PGPASSFILE", filepath.Join(home, "missing-pass"))
	t.Setenv("PGSERVICEFILE", filepath.Join(home, "missing-service"))
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(home, "missing-agent"))
	admin, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal("designated fixture connection failed")
	}
	name := "first_" + strings.ToLower(rand.Text())
	role := name + "_reader"
	quotedSchema, quotedRole := pgx.Identifier{name}.Sanitize(), pgx.Identifier{role}.Sanitize()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE; DROP ROLE IF EXISTS "+quotedRole); err != nil {
			t.Error("test-owned schema/role teardown failed")
		}
		_ = admin.Close(ctx)
		for _, dir := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
			if _, err := os.Stat(filepath.Join(home, dir)); !os.IsNotExist(err) {
				t.Error("machine path created persistent state")
			}
		}
	})
	ddl := []string{
		"CREATE ROLE " + quotedRole + " LOGIN PASSWORD 'v17a_synthetic_password'",
		"CREATE SCHEMA " + quotedSchema,
		"CREATE TABLE " + quotedSchema + ".protected (id integer PRIMARY KEY, value text NOT NULL)",
		"INSERT INTO " + quotedSchema + ".protected VALUES (1,'original')",
		"CREATE TYPE " + quotedSchema + ".mood AS ENUM ('ok','not ok')",
		"CREATE FUNCTION " + quotedSchema + ".attempt_write() RETURNS integer LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN INSERT INTO " + quotedSchema + ".protected VALUES (2,'changed'); RETURN 2; END $$",
		"GRANT USAGE ON SCHEMA " + quotedSchema + " TO " + quotedRole,
		"GRANT SELECT ON " + quotedSchema + ".protected TO " + quotedRole,
		"GRANT USAGE ON TYPE " + quotedSchema + ".mood TO " + quotedRole,
		"GRANT EXECUTE ON FUNCTION " + quotedSchema + ".attempt_write() TO " + quotedRole,
	}
	for _, sql := range ddl {
		if _, err := admin.Exec(context.Background(), sql); err != nil {
			t.Fatal("test-owned fixture setup failed")
		}
	}
	endpoint, err := url.Parse(dsn)
	if err != nil || endpoint.Scheme == "" {
		t.Fatal("FIRST fixture DSN must be PostgreSQL URL")
	}
	endpoint.User = url.UserPassword(role, "v17a_synthetic_password")
	query := endpoint.Query()
	query.Set("default_query_exec_mode", "simple_protocol")
	endpoint.RawQuery = query.Encode()
	profile := models.Connection{Name: "fixture", Driver: "postgres", DSN: endpoint.String(), CLIAccess: models.CLIAccessRowRead}
	return cli.InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }}, admin, name
}

func firstQuery(t *testing.T, service cli.InvocationService, sql string, params *string, extra ...string) (map[string]any, int) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "query.sql")
	if err := os.WriteFile(file, []byte(sql), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"query", "--connection", "fixture", "--file", file}
	if params != nil {
		path := filepath.Join(dir, "params.json")
		if err := os.WriteFile(path, []byte(*params), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "--params-file", path)
	}
	args = append(args, extra...)
	out, diagnostic := &firstOutput{}, &firstOutput{}
	handled, exit := cli.Dispatch(context.Background(), args, cli.IO{In: io.NopCloser(strings.NewReader("")), Out: out, Err: diagnostic}, map[string]cli.Command{"query": cli.QueryCommand(service)})
	if !handled || out.Len() == 0 || out.Len() > cli.MaxOutputBytes || out.Bytes()[out.Len()-1] != '\n' {
		t.Fatalf("invalid result publication exit%d bytes%d", exit, out.Len())
	}
	decoder := json.NewDecoder(bytes.NewReader(out.Bytes()))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal("invalid JSON result")
	}
	if decoder.Decode(new(any)) != io.EOF {
		t.Fatal("more than one result document")
	}
	if document["schema_version"] != json.Number("1") || document["ok"] != (exit == 0) || document["warnings"] == nil {
		t.Fatal("invalid envelope")
	}
	if exit != 0 && strings.Contains(out.String()+diagnostic.String(), "SECRET") {
		t.Fatal("backend error leaked")
	}
	return document, exit
}

func TestMachineQuery_TextValues_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	sql := `SELECT NULL::text AS dup, 'null'::text AS dup, 'null'::json, '9223372036854775807'::int8, '9007199254740993.1234567890123456789'::numeric, decode('00ff','hex'), ' { "a" : 9007199254740993, "b": null } '::json, '[0:1]={a,b}'::text[], 'infinity'::date, '-infinity'::timestamp, 'NaN'::numeric, 'Infinity'::float8, '2024-02-03 04:05:06+11'::timestamptz, 'ok'::` + pgx.Identifier{schema, "mood"}.Sanitize()
	doc, exit := firstQuery(t, service, sql, nil)
	if exit != 0 {
		t.Fatalf("text query exit%d", exit)
	}
	data := doc["data"].(map[string]any)
	rows := data["rows"].([]any)
	row := rows[0].([]any)
	want := []any{nil, "null", "null", "9223372036854775807", "9007199254740993.1234567890123456789", `\x00ff`, ` { "a" : 9007199254740993, "b": null } `, "[0:1]={a,b}", "infinity", "-infinity", "NaN", "Infinity", "2024-02-02 17:05:06+00", "ok"}
	if len(row) != len(want) {
		t.Fatal("positional text shape lost")
	}
	for i, value := range want {
		if row[i] != value {
			t.Errorf("text column%d: got%v want%v", i, row[i], value)
		}
	}
	columns := data["columns"].([]any)
	if columns[0].(map[string]any)["name"] != "dup" || columns[1].(map[string]any)["name"] != "dup" {
		t.Fatal("duplicate aliases collapsed")
	}
	var oid uint32
	if err := admin.QueryRow(context.Background(), "SELECT $1::regtype::oid", pgx.Identifier{schema, "mood"}.Sanitize()).Scan(&oid); err != nil {
		t.Fatal("custom type lookup failed")
	}
	if columns[13].(map[string]any)["type_oid"] != json.Number(strconv.FormatUint(uint64(oid), 10)) {
		t.Fatal("custom type OID lost")
	}
	for _, column := range columns {
		if column.(map[string]any)["encoding"] != "postgres-text" {
			t.Fatal("non-text encoding")
		}
	}
	params := `[9007199254740993,null,true,"{\"n\":9007199254740993}",1.2300e+25]`
	doc, exit = firstQuery(t, service, "SELECT $1::numeric, $2::text, $3::bool, $4::json, $5::numeric", &params)
	if exit != 0 {
		t.Fatal("scalar parameters failed")
	}
	row = doc["data"].(map[string]any)["rows"].([]any)[0].([]any)
	if row[0] != "9007199254740993" || row[1] != nil || row[2] != "t" || row[3] != `{"n":9007199254740993}` || row[4] != "12300000000000000000000000" {
		t.Fatalf("parameter values changed: %v", row)
	}
}

func TestMachineQuery_Policy_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	table := pgx.Identifier{schema, "protected"}.Sanitize()
	for _, sql := range []string{"INSERT INTO " + table + " VALUES(2,'changed')", "WITH changed AS (DELETE FROM " + table + " RETURNING *) SELECT * FROM changed", "BEGIN", "COMMIT", "SET transaction_read_only=off", "SELECT * FROM " + table + " FOR UPDATE", "SELECT * INTO other FROM " + table} {
		if _, exit := firstQuery(t, service, sql, nil); exit != cli.ExitInput {
			t.Fatalf("modifying/control syntax exit%d", exit)
		}
	}
	if document, exit := firstQuery(t, service, "SELECT "+pgx.Identifier{schema, "attempt_write"}.Sanitize()+"()", nil); exit != cli.ExitDatabase || document["error"].(map[string]any)["sqlstate"]!="25006" {
		t.Fatalf("server read-only enforcement exit%d", exit)
	}
	var count int
	var value string
	if err := admin.QueryRow(context.Background(), "SELECT count(*),min(value) FROM "+table).Scan(&count, &value); err != nil || count != 1 || value != "original" {
		t.Fatal("fixture contents changed")
	}
	profiles, _ := service.Load()
	profiles[0].CLIAccess = models.CLIAccessMetadata
	denied := service
	denied.Load = func() ([]models.Connection, error) { return profiles, nil }
	if _, exit := firstQuery(t, denied, "SELECT 1", nil); exit != cli.ExitPolicy {
		t.Fatal("metadata client read rows")
	}
}

func TestMachineQuery_Bounds_Live(t *testing.T) {
	service, _, _ := firstMachineFixture(t)
	for _, tc := range []struct {
		sql       string
		limit     string
		rows      int
		truncated bool
	}{{"SELECT i FROM generate_series(1,2) AS t(i)", "2", 2, false}, {"SELECT i FROM generate_series(1,3) AS t(i)", "2", 2, true}, {"SELECT 1 WHERE false", "2", 0, false}} {
		doc, exit := firstQuery(t, service, tc.sql, nil, "--max-rows", tc.limit)
		if exit != 0 || doc["truncated"] != tc.truncated || doc["result_complete"] == tc.truncated {
			t.Fatal("row boundary metadata lost")
		}
		if rows := doc["data"].(map[string]any)["rows"]; rows == nil || len(rows.([]any)) != tc.rows {
			t.Fatal("row prefix/empty array lost")
		}
	}
	if _, exit := firstQuery(t, service, "SELECT repeat('x',65536)", nil); exit != 0 {
		t.Fatal("exact cell cap rejected")
	}
	for _, sql := range []string{"SELECT repeat('x',65537)", "SELECT repeat('x',65536) FROM generate_series(1,17)"} {
		doc, exit := firstQuery(t, service, sql, nil)
		if exit != cli.ExitResource || doc["data"] != nil {
			t.Fatal("oversized result clipped or published")
		}
	}
}

func TestMachineQuery_TerminalErrors_Live(t *testing.T) {
	service, _, _ := firstMachineFixture(t)
	doc, exit := firstQuery(t, service, "SELECT CASE WHEN i=3 THEN 1/(i-i) ELSE i END FROM generate_series(1,3) AS t(i)", nil, "--max-rows", "2")
	if exit != cli.ExitDatabase || doc["data"] != nil || doc["ok"] != false {
		t.Fatal("erroring tail became successful prefix")
	}
	doc, exit = firstQuery(t, service, "SELECT nonexistent_SECRET_column", nil)
	if exit != cli.ExitDatabase || doc["data"] != nil {
		t.Fatal("database error mapping failed")
	}
}

func TestMachineQuery_Cancel_Live(t *testing.T) {
	service, admin, schema := firstMachineFixture(t)
	doc, exit := firstQuery(t, service, "SELECT i, pg_sleep(CASE WHEN i>2 THEN 0.3 ELSE 0 END) FROM generate_series(1,3) AS t(i)", nil, "--max-rows", "2", "--timeout", "200ms")
	if exit != cli.ExitTimeout || doc["data"] != nil {
		t.Fatal("slow tail became successful prefix")
	}
	profiles, _ := service.Load()
	profiles[0].StatementTimeout = "20ms"
	short := service
	short.Load = func() ([]models.Connection, error) { return profiles, nil }
	if _, exit := firstQuery(t, short, "SELECT pg_sleep(0.3)", nil); exit != cli.ExitTimeout {
		t.Fatal("shorter saved statement timeout lost")
	}
	lock, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal("lock fixture begin failed")
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err := lock.Exec(context.Background(), "LOCK TABLE "+pgx.Identifier{schema, "protected"}.Sanitize()+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal("lock fixture setup failed")
	}
	start := time.Now()
	if _, exit := firstQuery(t, service, "SELECT * FROM "+pgx.Identifier{schema, "protected"}.Sanitize(), nil); exit != cli.ExitTimeout || time.Since(start) > 2500*time.Millisecond {
		t.Fatal("server lock ceiling failed")
	}
	_ = lock.Rollback(context.Background())
	start = time.Now()
	if _, exit := firstQuery(t, service, "SELECT pg_sleep(20)", nil); exit != cli.ExitTimeout || time.Since(start) > 12*time.Second {
		t.Fatal("server statement ceiling failed")
	}
}
