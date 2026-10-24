//go:build integration && (linux || darwin)

package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

var firstProcessBinary, firstProcessDSN string

func TestMain(m *testing.M) {
	if os.Getenv("PGSAVVY_HISTORY_PROCESS_HELPER") == "1" {
		os.Exit(m.Run())
	}

	firstProcessDSN = os.Getenv("PGSAVVY_TEST_PG")
	if firstProcessDSN == "" {
		fmt.Fprintln(os.Stderr, "machine process tests require designated PGSAVVY_TEST_PG")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	conn, err := pgx.Connect(ctx, firstProcessDSN)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "machine process fixture unreachable")
		os.Exit(1)
	}
	var version int
	err = conn.QueryRow(ctx, "SELECT version FROM app._fixture_meta").Scan(&version)
	_ = conn.Close(ctx)
	cancel()
	if err != nil || version != 2 {
		fmt.Fprintln(os.Stderr, "machine process fixture missing or stale")
		os.Exit(1)
	}
	if binary := os.Getenv("PGSAVVY_TEST_BINARY"); binary != "" {
		if !filepath.IsAbs(binary) {
			fmt.Fprintln(os.Stderr, "process test binary must be absolute")
			os.Exit(1)
		}
		firstProcessBinary = binary
		os.Exit(m.Run())
	}
	base := os.TempDir()
	if info, err := os.Stat("/tmp/opencode"); err == nil && info.IsDir() {
		base = "/tmp/opencode"
	}
	dir, err := os.MkdirTemp(base, "pgsavvy-first-process-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create owned process build directory")
		os.Exit(1)
	}
	firstProcessBinary = filepath.Join(dir, "pgsavvy")
	_, file, _, _ := runtime.Caller(0)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	command := exec.CommandContext(ctx, "go", "build", "-o", firstProcessBinary, ".")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	output, err := command.CombinedOutput()
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "process binary build failed: %s\n", output)
		_ = os.Remove(dir)
		os.Exit(1)
	}
	exit := m.Run()
	_ = os.Remove(firstProcessBinary)
	_ = os.Remove(dir)
	os.Exit(exit)
}

type firstProcessFixture struct {
	root, path           string
	env                  []string
	admin                *pgx.Conn
	profile              models.Connection
	schema, helperMarker string
}

func firstProcess(t *testing.T, live bool) *firstProcessFixture {
	t.Helper()
	root := t.TempDir()
	fixture := &firstProcessFixture{root: root, path: filepath.Join(root, "config", "pgsavvy", "connections.yml"), helperMarker: filepath.Join(root, "helper-ran")}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "PG") || strings.HasPrefix(name, "XDG_") || name == "HOME" || name == "SSH_AUTH_SOCK" || name == "ENV" || name == "BASH_ENV" || name == "SHELL" {
			continue
		}
		fixture.env = append(fixture.env, entry)
	}
	fixture.env = append(fixture.env, "HOME="+filepath.Join(root, "home"), "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "XDG_STATE_HOME="+filepath.Join(root, "state"), "XDG_CACHE_HOME="+filepath.Join(root, "cache"), "XDG_RUNTIME_DIR="+filepath.Join(root, "runtime"), "PGPASSFILE="+filepath.Join(root, "no-pass"), "PGSERVICEFILE="+filepath.Join(root, "no-service"), "SSH_AUTH_SOCK="+filepath.Join(root, "no-agent"), "SHELL=/bin/sh", "ENV=", "BASH_ENV=", "PGSAVVY_PG_RAW_ERRORS=1", "PGSAVVY_LOG_LEVEL=debug")
	fixture.profile = models.Connection{Name: "Exact.Profile", Driver: "postgres", DSN: "postgres://SECRET@unavailable.invalid/SECRET", Tags: []string{"tag"}, Label: "label", PasswordCommand: "printf SECRET > '" + fixture.helperMarker + "'; printf SECRET", KeyringRef: "SECRET", PgpassPath: filepath.Join(root, "SECRET")}
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, firstProcessDSN)
		if err != nil {
			t.Fatal("process fixture admin connection failed")
		}
		fixture.admin = admin
		fixture.schema = "first_process_" + strings.ToLower(rand.Text())
		role := fixture.schema + "_reader"
		schema, quotedRole := pgx.Identifier{fixture.schema}.Sanitize(), pgx.Identifier{role}.Sanitize()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; DROP ROLE IF EXISTS "+quotedRole); err != nil {
				t.Error("test-owned process schema/role cleanup failed")
			}
			_ = admin.Close(ctx)
		})
		for _, sql := range []string{"CREATE ROLE " + quotedRole + " LOGIN PASSWORD 'v17a_process_SECRET'", "CREATE SCHEMA " + schema, "CREATE TABLE " + schema + ".parent (id integer PRIMARY KEY, value text)", "CREATE TABLE " + schema + ".child (id integer REFERENCES " + schema + ".parent(id))", "INSERT INTO " + schema + ".parent VALUES (1,'unchanged')", "GRANT USAGE ON SCHEMA " + schema + " TO " + quotedRole, "GRANT SELECT ON ALL TABLES IN SCHEMA " + schema + " TO " + quotedRole} {
			if _, err := admin.Exec(ctx, sql); err != nil {
				t.Fatal("test-owned process DDL failed")
			}
		}
		u, err := url.Parse(firstProcessDSN)
		if err != nil {
			t.Fatal("process fixture URL required")
		}
		u.User = url.UserPassword(role, "v17a_process_SECRET")
		fixture.profile.DSN = u.String()
		fixture.profile.CLIAccess = models.CLIAccessRowRead
		fixture.profile.PasswordCommand = ""
		fixture.profile.KeyringRef = ""
		fixture.profile.PgpassPath = ""
	}
	t.Cleanup(func() {
		for _, name := range []string{"home", "state", "data", "cache", "runtime"} {
			if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
				t.Errorf("machine invocation created %s state", name)
			}
		}
		if _, err := os.Stat(fixture.helperMarker); !os.IsNotExist(err) {
			t.Error("denied/offline invocation ran helper")
		}
	})
	return fixture
}

func (f *firstProcessFixture) save(t *testing.T, profiles ...models.Connection) {
	t.Helper()
	data, err := yaml.Marshal(struct {
		Connections []models.Connection `yaml:"connections"`
	}{profiles})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *firstProcessFixture) command(ctx context.Context, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, firstProcessBinary, args...)
	command.Env = f.env
	command.Stdin = strings.NewReader("")
	return command
}

func processExit(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func processDocument(t *testing.T, output, diagnostics []byte, exit int) map[string]any {
	t.Helper()
	if len(output) == 0 || len(output) > MaxOutputBytes || output[len(output)-1] != '\n' {
		t.Fatalf("process output invalid: exit%d bytes%d stderr=%q", exit, len(output), diagnostics)
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var doc map[string]any
	if decoder.Decode(&doc) != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatal("process did not return exactly one JSON document")
	}
	if doc["schema_version"] != json.Number("1") || doc["ok"] != (exit == 0) || doc["warnings"] == nil {
		t.Fatalf("process envelope mismatch exit%d", exit)
	}
	if secret := processLeakedSentinel(output, diagnostics, doc); secret != "" {
		t.Fatalf("process leaked sentinel %s", secret)
	}
	if len(diagnostics) > 0 {
		var diagnostic map[string]any
		if json.Unmarshal(diagnostics, &diagnostic) != nil || len(diagnostic) != 2 || diagnostic["exit"] != float64(exit) || diagnostic["command"] != doc["command"] {
			t.Fatalf("unsafe/mismatched process diagnostic %q", diagnostics)
		}
	}
	return doc
}

const processGuardedAccessText = "row-read or full with read_only=true: SELECT validation and a READ ONLY transaction; columns/rows payload"

func processLeakedSentinel(output, diagnostics []byte, doc map[string]any) string {
	selectOutput := output
	data, _ := doc["data"].(map[string]any)
	policyKey := ""
	if doc["ok"] == true && doc["command"] == "capabilities" {
		policyKey = "query_policy"
	} else if doc["ok"] == true && doc["command"] == "query" && data["usage"] == queryUsage && data["read_only"] == false {
		policyKey = "policy"
	}
	if policy, ok := data[policyKey].(map[string]any); policyKey != "" && ok && policy["guarded_access"] == processGuardedAccessText {
		if !processUniqueJSONKeys(output) {
			return "SELECT "
		}
		// Permit only this static documentation field, without changing the result
		// or exempting SQL/credential sentinels elsewhere in output or diagnostics.
		check := maps.Clone(doc)
		checkData, checkPolicy := maps.Clone(data), maps.Clone(policy)
		checkPolicy["guarded_access"] = ""
		checkData[policyKey], check["data"] = checkPolicy, checkData
		selectOutput, _ = json.Marshal(check)
	}
	for _, secret := range []string{"SECRET", "unavailable.invalid", "pgx", "SELECT ", "PasswordCommand"} {
		candidate := output
		if secret == "SELECT " {
			candidate = selectOutput
		}
		if bytes.Contains(candidate, []byte(secret)) || bytes.Contains(diagnostics, []byte(secret)) {
			return secret
		}
	}
	return ""
}

// A decoded-map copy must not hide earlier values overwritten by duplicate keys.
func processUniqueJSONKeys(output []byte) bool {
	if len(output) > MaxOutputBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value func() bool
	value = func() bool {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delim, container := token.(json.Delim)
		if !container {
			return true
		}
		if delim != '{' && delim != '[' {
			return false
		}
		keys := map[string]bool{}
		for decoder.More() {
			if delim == '{' {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || keys[key] {
					return false
				}
				keys[key] = true
			}
			if !value() {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && (delim == '{' && closing == json.Delim('}') || delim == '[' && closing == json.Delim(']'))
	}
	if !value() {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func (f *firstProcessFixture) run(t *testing.T, want int, args ...string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	command := f.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	start := time.Now()
	exit := processExit(command.Run())
	if exit != want || time.Since(start) > 35*time.Second {
		t.Fatalf("process %v: exit%d want%d elapsed%s stderr=%q", args, exit, want, time.Since(start), stderr.String())
	}
	return processDocument(t, stdout.Bytes(), stderr.Bytes(), exit)
}

func TestMachineCLIProcess_Discovery(t *testing.T) {
	t.Run("privacy_policy_exception_is_schema_scoped", func(t *testing.T) {
		makeDoc := func(command string) map[string]any {
			data := map[string]any{"query_policy": map[string]any{"guarded_access": processGuardedAccessText}}
			if command == "query" {
				data = map[string]any{"usage": queryUsage, "read_only": false, "policy": data["query_policy"]}
			}
			return map[string]any{"schema_version": json.Number("1"), "ok": true, "command": command, "warnings": []any{}, "data": data}
		}
		for _, command := range []string{"capabilities", "query"} {
			doc := makeDoc(command)
			before, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			got := processDocument(t, append(append([]byte{}, before...), '\n'), nil, 0)
			after, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			returned, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !bytes.Equal(before, returned) {
				t.Fatal("privacy check changed original or returned document")
			}
		}
		t.Run("escaped_policy_duplicate_key", func(t *testing.T) {
			output, err := json.Marshal(makeDoc("capabilities"))
			if err != nil {
				t.Fatal(err)
			}
			output = bytes.Replace(output, []byte("SELECT "), []byte(`\u0053ELECT `), 1)
			output = append(output[:len(output)-1], []byte(`,"extra":"SELECT private","extra":"safe"}`)...)
			var doc map[string]any
			if err := json.Unmarshal(output, &doc); err != nil {
				t.Fatal(err)
			}
			if processLeakedSentinel(output, nil, doc) != "SELECT " {
				t.Fatal("duplicate key concealed a raw SQL sentinel behind escaped static documentation")
			}
		})
		for _, test := range []struct {
			name       string
			mutate     func(map[string]any)
			diagnostic []byte
		}{
			{"other_field", func(d map[string]any) { d["data"].(map[string]any)["extra"] = processGuardedAccessText }, nil},
			{"modified_policy", func(d map[string]any) {
				d["data"].(map[string]any)["query_policy"].(map[string]any)["guarded_access"] = processGuardedAccessText + " changed"
			}, nil},
			{"other_policy_field", func(d map[string]any) {
				d["data"].(map[string]any)["query_policy"].(map[string]any)["extra"] = "SELECT private"
			}, nil},
			{"warnings", func(d map[string]any) { d["warnings"] = []any{"SELECT private"} }, nil},
			{"wrong_command", func(d map[string]any) { d["command"] = "connections list" }, nil},
			{"wrong_schema_field", func(d map[string]any) {
				data := d["data"].(map[string]any)
				data["policy"] = data["query_policy"]
				delete(data, "query_policy")
			}, nil},
			{"diagnostic", func(map[string]any) {}, []byte("SELECT private")},
		} {
			t.Run(test.name, func(t *testing.T) {
				doc := makeDoc("capabilities")
				test.mutate(doc)
				output, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if processLeakedSentinel(output, test.diagnostic, doc) != "SELECT " {
					t.Fatal("SQL sentinel outside the exact documentation field was accepted")
				}
			})
		}
		for _, secret := range []string{"SECRET", "unavailable.invalid", "pgx", "PasswordCommand"} {
			doc := makeDoc("capabilities")
			doc["data"].(map[string]any)["extra"] = secret
			output, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if processLeakedSentinel(output, nil, doc) != secret {
				t.Fatal("credential sentinel was accepted")
			}
			clean := makeDoc("capabilities")
			output, err = json.Marshal(clean)
			if err != nil {
				t.Fatal(err)
			}
			if processLeakedSentinel(output, []byte(secret), clean) != secret {
				t.Fatal("diagnostic credential sentinel was accepted")
			}
		}
	})
	f := firstProcess(t, false)
	doc := f.run(t, 0, "connections", "list")
	if len(doc["data"].([]any)) != 0 {
		t.Fatal("missing profile config not empty")
	}
	if _, err := os.Stat(filepath.Join(f.root, "config")); !os.IsNotExist(err) {
		t.Fatal("offline list created config")
	}
	f.save(t, f.profile)
	before, _ := os.ReadFile(f.path)
	for _, args := range [][]string{{"connections", "list"}, {"connections", "show", "Exact.Profile"}} {
		doc = f.run(t, 0, args...)
		var profile map[string]any
		if args[1] == "list" {
			profile = doc["data"].([]any)[0].(map[string]any)
		} else {
			profile = doc["data"].(map[string]any)
		}
		if len(profile) != 7 || profile["credential_status"] != "not_checked" || profile["cli_access"] != "disabled" || profile["name"] != "Exact.Profile" {
			t.Fatal("profile allowlist lost")
		}
	}
	for _, args := range [][]string{{"capabilities"}, {"--help"}, {"connections", "--help"}, {"schema", "--help"}, {"query", "--help"}} {
		f.run(t, 0, args...)
	}
	doc = f.run(t, 0, "capabilities")
	commands := doc["data"].(map[string]any)["commands"].([]any)
	if len(commands) != 10 {
		t.Fatal("capability registry mismatch")
	}
	for _, args := range [][]string{{"unknown_SECRET_command"}, {"--version", "unknown_SECRET_command"}, {"query", "--connection", "x", "--file", "-", "--format", "yaml"}, {"connections", "list", "--format", "json", "--format", "json"}} {
		f.run(t, 2, args...)
	}
	f.run(t, 3, "connections", "show", "exact.profile")
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) {
		t.Fatal("machine discovery rewrote profiles")
	}
	dash := f.profile
	dash.Name = "-literal"
	f.save(t, dash)
	f.run(t, 0, "connections", "show", "--", "-literal")
	fifo := filepath.Join(f.root, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	f.path = fifo // move the FIFO onto the trusted config path without opening it
	if err := os.Rename(fifo, filepath.Join(f.root, "config", "pgsavvy", "connections.yml")); err != nil {
		t.Fatal(err)
	}
	f.run(t, 3, "connections", "list")
}

func TestMachineCLIProcess_Test(t *testing.T) {
	f := firstProcess(t, true)
	f.save(t, f.profile)
	doc := f.run(t, 0, "connections", "test", f.profile.Name)
	if doc["data"].(map[string]any)["connected"] != true {
		t.Fatal("connectivity result absent")
	}
	denied := f.profile
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var networkCalls atomic.Int32
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			networkCalls.Add(1)
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-joined
		if networkCalls.Load() != 0 {
			t.Error("denied/ambiguous profile performed network I/O")
		}
	})
	denied.CLIAccess = ""
	denied.PasswordCommand = "printf SECRET > '" + f.helperMarker + "'"
	denied.DSN = "postgres://u@" + listener.Addr().String() + "/d?sslmode=disable"
	f.save(t, denied)
	f.run(t, 4, "connections", "test", denied.Name)
	ambiguous := denied
	ambiguous.CLIAccess = models.CLIAccessRowRead
	f.save(t, ambiguous, ambiguous)
	f.run(t, 3, "connections", "test", f.profile.Name)
	wrong := f.profile
	u, _ := url.Parse(wrong.DSN)
	u.User = url.UserPassword(u.User.Username(), "wrong_SECRET")
	wrong.DSN = u.String()
	f.save(t, wrong)
	f.run(t, 3, "connections", "test", wrong.Name)
}

func TestMachineCLIProcess_TargetSelection(t *testing.T) {
	f := firstProcess(t, true)
	base, err := url.Parse(f.profile.DSN)
	if err != nil {
		t.Fatal("process fixture URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	direct, err := pgx.Connect(ctx, base.String())
	if err != nil {
		t.Fatal("process fixture role baseline connection failed")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if err := direct.Close(cleanup); err != nil {
			t.Error("process fixture baseline cleanup failed")
		}
	})
	var readOnly string
	var recovery bool
	if err := direct.QueryRow(ctx, "SELECT current_setting('transaction_read_only'), pg_is_in_recovery()").Scan(&readOnly, &recovery); err != nil || readOnly != "off" || recovery {
		t.Fatal("process fixture role must start writable on a primary")
	}
	sql := f.sql(t, "SELECT id,value,current_setting('transaction_read_only'),current_setting('default_transaction_read_only') FROM "+pgx.Identifier{f.schema, "parent"}.Sanitize())
	for _, tc := range []struct {
		name, selector, env, startup string
		reject                       bool
	}{
		{name: "explicit_read_write", selector: "read-write"},
		{name: "environment_read_write", env: "read-write"},
		{name: "explicit_read_write_over_environment", selector: "read-write", env: "read-only"},
		{name: "explicit_any_over_environment", selector: "any", env: "read-write", startup: "on"},
		{name: "any_control", selector: "any"},
		{name: "primary_control", selector: "primary"},
		{name: "startup_off", selector: "read-write", startup: "off"},
		{name: "read_only_rejection", selector: "read-only", reject: true},
		{name: "startup_on_read_write_rejection", selector: "read-write", startup: "on", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := *base
			params := u.Query()
			params.Del("target_session_attrs")
			if tc.selector != "" {
				params.Set("target_session_attrs", tc.selector)
			}
			if tc.startup != "" {
				params.Set("default_transaction_read_only", tc.startup)
			}
			u.RawQuery = params.Encode()
			profile := f.profile
			profile.DSN = u.String()
			f.save(t, profile)
			originalEnv := f.env
			f.env = append(append([]string{}, originalEnv...), "PGTARGETSESSIONATTRS="+tc.env)
			defer func() { f.env = originalEnv }()
			want := 0
			if tc.reject {
				want = 3
			}
			for _, args := range [][]string{
				{"connections", "test", profile.Name},
				{"schema", "list", "--connection", profile.Name, "--limit", "1000"},
				{"query", "--connection", profile.Name, "--file", sql},
			} {
				doc := f.run(t, want, args...)
				if tc.reject {
					if doc["error"].(map[string]any)["code"] != "CONNECTION_UNAVAILABLE" {
						t.Fatal("process selector rejection misclassified")
					}
					continue
				}
				switch args[0] {
				case "connections":
					data := doc["data"].(map[string]any)
					if data["connected"] != true || data["read_only"] != true {
						t.Fatal("process connection result lost")
					}
				case "schema":
					found := false
					for _, item := range doc["data"].(map[string]any)["items"].([]any) {
						if item.(map[string]any)["name"] == f.schema {
							found = true
						}
					}
					if !found {
						t.Fatal("process schema list omitted fixture")
					}
				case "query":
					rows := doc["data"].(map[string]any)["rows"].([]any)
					if len(rows) != 1 {
						t.Fatal("process query fixture row missing")
					}
					row := rows[0].([]any)
					if len(row) != 4 || row[0] != "1" || row[1] != "unchanged" || row[2] != "on" || row[3] != "on" {
						t.Fatalf("process fixture/read-only values changed: %v", row)
					}
				}
			}
		})
	}
}

func TestMachineCLIProcess_Schema(t *testing.T) {
	f := firstProcess(t, true)
	f.profile.CLIAccess = models.CLIAccessMetadata
	f.save(t, f.profile)
	for _, args := range [][]string{{"list", "--limit", "1000"}, {"tables", "--schema", f.schema}, {"describe", "--schema", f.schema, "--table", "parent"}, {"relationships", "--schema", f.schema, "--table", "parent"}} {
		f.run(t, 0, append([]string{"schema", args[0], "--connection", f.profile.Name}, args[1:]...)...)
	}
	f.run(t, 5, "schema", "describe", "--connection", f.profile.Name, "--schema", f.schema, "--table", "missing")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			f.run(t, 0, "schema", "describe", "--connection", f.profile.Name, "--schema", f.schema, "--table", "parent")
		})
	}
	wg.Wait()
}

func (f *firstProcessFixture) sql(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(f.root, "query.sql")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMachineCLIProcess_Query(t *testing.T) {
	f := firstProcess(t, true)
	f.save(t, f.profile)
	sql := f.sql(t, "SELECT id,value FROM "+pgx.Identifier{f.schema, "parent"}.Sanitize())
	doc := f.run(t, 0, "query", "--connection", f.profile.Name, "--file", sql)
	rows := doc["data"].(map[string]any)["rows"].([]any)
	if len(rows) != 1 || rows[0].([]any)[1] != "unchanged" {
		t.Fatal("query result not actual fixture data")
	}
	params := filepath.Join(f.root, "parameters.json")
	if err := os.WriteFile(params, []byte(`[9007199254740993,null]`), 0o600); err != nil {
		t.Fatal(err)
	}
	sql = f.sql(t, "SELECT $1::numeric,$2::text")
	doc = f.run(t, 0, "query", "--connection", f.profile.Name, "--file", sql, "--params-file", params)
	row := doc["data"].(map[string]any)["rows"].([]any)[0].([]any)
	if row[0] != "9007199254740993" || row[1] != nil {
		t.Fatal("process parameters lost precision/null")
	}
	for _, tc := range []struct {
		sql  string
		exit int
	}{{"DELETE FROM " + pgx.Identifier{f.schema, "parent"}.Sanitize(), 2}, {"SELECT 1/0", 5}, {"SELECT repeat('x',65537)", 7}, {"SELECT missing_SECRET_column", 5}} {
		sql = f.sql(t, tc.sql)
		f.run(t, tc.exit, "query", "--connection", f.profile.Name, "--file", sql)
	}
	sql = f.sql(t, "SELECT i FROM generate_series(1,3) AS t(i)")
	doc = f.run(t, 0, "query", "--connection", f.profile.Name, "--file", sql, "--max-rows", "2")
	if doc["truncated"] != true || doc["result_complete"] != false {
		t.Fatal("capped process result became complete")
	}
	f.profile.CLIAccess = models.CLIAccessMetadata
	f.save(t, f.profile)
	f.run(t, 4, "query", "--connection", f.profile.Name, "--file", sql)
	var count int
	if f.admin.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{f.schema, "parent"}.Sanitize()).Scan(&count) != nil || count != 1 {
		t.Fatal("query process changed fixture")
	}
}

func TestMachineCLIProcess_Signals(t *testing.T) {
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(signal.String(), func(t *testing.T) {
			f := firstProcess(t, false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := f.command(ctx, "query", "--connection", "missing", "--file", "-")
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = read.Close(); _ = write.Close() }()
			command.Stdin = read
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if command.Start() != nil {
				t.Fatal("signal process start failed")
			}
			defer func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			// Wait for signal handlers, then leave stdin open and silent.
			if !processInputConsumed(read, write, time.Second) {
				t.Fatal("process did not install signal handlers")
			}
			if err := command.Process.Signal(signal); err != nil {
				t.Fatal(err)
			}
			_ = command.Process.Signal(signal)
			start := time.Now()
			exit := processExit(command.Wait())
			if exit != 6 || time.Since(start) > 3*time.Second {
				t.Fatalf("signal exit%d elapsed%s stderr%q", exit, time.Since(start), stderr.String())
			}
			doc := processDocument(t, stdout.Bytes(), stderr.Bytes(), exit)
			if doc["error"].(map[string]any)["code"] != "TIMEOUT" {
				t.Fatal("signal lost timeout JSON")
			}
		})
	}
	for _, signal := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run("backend-"+signal.String(), func(t *testing.T) {
			f := firstProcess(t, true)
			f.save(t, f.profile)
			sql := f.sql(t, "SELECT pg_sleep(20)")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := f.command(ctx, "query", "--connection", f.profile.Name, "--file", sql)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			u, _ := url.Parse(f.profile.DSN)
			role := u.User.Username()
			active := false
			waitUntil := time.Now().Add(time.Second)
			for time.Now().Before(waitUntil) {
				if err := f.admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_stat_activity WHERE usename=$1 AND application_name='pgsavvy-machine' AND state='active' AND query LIKE '%pg_sleep%')", role).Scan(&active); err != nil {
					t.Fatal("signal readiness query failed")
				}
				if active {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !active {
				t.Fatal("query never entered PostgreSQL before signal")
			}
			_ = command.Process.Signal(signal)
			_ = command.Process.Signal(signal)
			start := time.Now()
			exit := processExit(command.Wait())
			if exit != 6 || time.Since(start) > 3*time.Second {
				t.Fatalf("backend signal exit%d elapsed%s", exit, time.Since(start))
			}
			processDocument(t, stdout.Bytes(), stderr.Bytes(), exit)
		})
	}
	t.Run("default-wall-time", func(t *testing.T) {
		f := firstProcess(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		command := f.command(ctx, "query", "--connection", "missing", "--file", "-")
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = read.Close(); _ = write.Close() }()
		command.Stdin = read
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		start := time.Now()
		exit := processExit(command.Run())
		if exit != 6 || time.Since(start) > 35*time.Second {
			t.Fatalf("default deadline exceeded: exit%d elapsed%s", exit, time.Since(start))
		}
		processDocument(t, stdout.Bytes(), stderr.Bytes(), exit)
	})
}

// The worker does not prefetch: consuming a sentinel proves Dispatch entered
// ReadSQL after root signal registration and successful stream acquisition.
func processInputConsumed(read, write *os.File, budget time.Duration) bool {
	if _, err := write.Write([]byte(" ")); err != nil {
		return false
	}
	raw, err := read.SyscallConn()
	if err != nil {
		return false
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		consumed := false
		if err := raw.Control(func(fd uintptr) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(fds, 0)
			consumed = err == nil && n == 0
		}); err != nil {
			return false
		}
		if consumed {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func TestMachineCLIProcess_LegacyRoutes(t *testing.T) {
	f := firstProcess(t, false)
	for _, args := range [][]string{{"--version"}, {"-version"}, {"--log-dir", filepath.Join(f.root, "unused-log"), "--version"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		command := f.command(ctx, args...)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil || !bytes.HasPrefix(output, []byte("pgsavvy ")) || bytes.HasPrefix(output, []byte("{")) {
			t.Fatalf("legacy version route changed: %q (%T)", output, err)
		}
	}
	if !IsLegacyRoute(nil) || !IsLegacyRoute([]string{"update", "--check"}) || IsLegacyRoute([]string{"--version", "unknown"}) {
		t.Fatal("legacy dispatch contract changed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := f.command(ctx, "update", "--bad")
	output, err := command.CombinedOutput()
	if processExit(err) == 0 || !bytes.Contains(output, []byte("does not accept arguments")) || bytes.HasPrefix(output, []byte("{")) {
		t.Fatalf("legacy update route changed: %q", output)
	}
}

// A dedicated wire fixture makes output > pipe capacity available without a
// real database. Only its test-owned loopback listener is contacted.
func processOutputFixture(t *testing.T, f *firstProcessFixture) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var mu sync.Mutex
	var owned net.Conn
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		owned = conn
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		backend := pgproto3.NewBackend(conn, conn)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			return
		}
		backend.Send(&pgproto3.AuthenticationOk{})
		backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if backend.Flush() != nil {
			return
		}
		query := ""
		for {
			message, err := backend.Receive()
			if err != nil {
				return
			}
			switch message := message.(type) {
			case *pgproto3.Parse:
				query = message.Query
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				if strings.Contains(query, "payload") {
					backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 25}}})
				} else {
					backend.Send(&pgproto3.NoData{})
				}
			case *pgproto3.Execute:
				if strings.Contains(query, "payload") {
					for range 10 {
						backend.Send(&pgproto3.DataRow{Values: [][]byte{bytes.Repeat([]byte{'x'}, 50<<10)}})
					}
				}
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 10")})
			case *pgproto3.Sync:
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			case *pgproto3.Terminate:
				return
			default:
				return
			}
			if backend.Flush() != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		if owned != nil {
			_ = owned.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("output fixture not joined")
		}
	})
	f.profile = models.Connection{Name: "pipe", Driver: "postgres", DSN: "postgres://u@" + ln.Addr().String() + "/d?sslmode=disable", CLIAccess: models.CLIAccessRowRead}
	f.save(t, f.profile)
}

func TestMachineCLIProcess_Query_OutputPipes(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken=%v", broken), func(t *testing.T) {
			f := firstProcess(t, false)
			processOutputFixture(t, f)
			sql := f.sql(t, "SELECT 'payload'")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			command := f.command(ctx, "query", "--connection", "pipe", "--file", sql, "--timeout", "1s")
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = read.Close(); _ = write.Close() }()
			if broken {
				_ = read.Close()
			}
			command.Stdout = write
			var stderr bytes.Buffer
			command.Stderr = &stderr
			start := time.Now()
			exit := processExit(command.Run())
			if exit != 7 || time.Since(start) > 2*time.Second {
				t.Fatalf("output pipe not bounded: exit%d elapsed%s stderr%q", exit, time.Since(start), stderr.String())
			}
			if !strings.Contains(stderr.String(), `"exit":7`) {
				t.Fatal("output failure recorded premature success")
			}
		})
	}
}
