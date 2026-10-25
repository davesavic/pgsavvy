//go:build linux

package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/davesavic/pgsavvy/internal/machineio"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

// Reuse the test executable so these tests run without an integration fixture
// or build step. Consent comes exclusively from saved YAML, not helper flags.
func TestCLIHistoryProcess_Helper(t *testing.T) {
	if os.Getenv("PGSAVVY_HISTORY_PROCESS_HELPER") != "1" {
		return
	}
	os.Exit(runHistoryProcess())
}

func runHistoryProcess() int {
	syscall.Umask(0o022)
	if os.Getenv("PGSAVVY_HISTORY_PROCESS_START") == "1" {
		gate := os.NewFile(3, "test-start")
		_, err := io.ReadFull(gate, make([]byte, 1))
		_ = gate.Close()
		if err != nil {
			return ExitResource
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), InvocationTimeout)
	defer cancel()
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	input, err := machineio.OpenInput(ctx, os.Stdin)
	if err != nil {
		return ExitResource
	}
	defer func() { _ = input.Close() }()
	output, err := machineio.OpenOutput(ctx, os.Stdout)
	if err != nil {
		return ExitResource
	}
	defer func() { _ = output.Close() }()
	diagnostic, err := machineio.OpenOutput(ctx, os.Stderr)
	if err != nil {
		return ExitResource
	}
	defer func() { _ = diagnostic.Close() }()
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return ExitInput
	}
	service := InvocationService{}
	registry := map[string]Command{"query": QueryCommand(service), "connections": ConnectionsCommand(service), "schema": SchemaCommand(service), "capabilities": CapabilitiesCommand()}
	_, exit := Dispatch(ctx, args, IO{In: input, Out: output, Err: diagnostic, Cancel: interrupted.Done()}, registry)
	return exit
}

type historyProcessFixture struct {
	root, profiles, store string
	environment           []string
	consent               *bool
}

func historyConsent(enabled bool) *bool { return &enabled }

func newHistoryProcess(t *testing.T, consent *bool) *historyProcessFixture {
	t.Helper()
	root := t.TempDir()
	f := &historyProcessFixture{root: root, profiles: filepath.Join(root, "config", "pgsavvy", "connections.yml"), store: filepath.Join(root, "state", "pgsavvy", "history.sqlite"), consent: consent}
	require.NoError(t, os.MkdirAll(filepath.Dir(f.profiles), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "home"), 0o700))
	f.environment = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"), "LANG=C.UTF-8",
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_RUNTIME_DIR=" + filepath.Join(root, "runtime"),
		"PGPASSFILE=" + filepath.Join(root, "missing-pass"), "PGSERVICEFILE=" + filepath.Join(root, "missing-service"), "SSH_AUTH_SOCK=" + filepath.Join(root, "missing-agent"),
		"PGSAVVY_HISTORY_PROCESS_HELPER=1",
	}
	f.save(t, models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessRowRead})
	return f
}

func (f *historyProcessFixture) save(t *testing.T, profile models.Connection) {
	t.Helper()
	fields := map[string]any{"name": profile.Name, "driver": profile.Driver, "dsn": profile.DSN, "password": profile.Password, "read_only": false, "cli_access": profile.CLIAccess}
	if f.consent != nil {
		fields["cli_history"] = *f.consent
	}
	data, err := yaml.Marshal(map[string]any{"connections": []any{fields}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(f.profiles, data, 0o600))
}

func (f *historyProcessFixture) sql(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(f.root, "query.sql")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

func (f *historyProcessFixture) command(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	all := append([]string{"-test.run=^TestCLIHistoryProcess_Helper$", "--"}, args...)
	command := exec.CommandContext(ctx, os.Args[0], all...)
	command.Env, command.Dir = f.environment, f.root
	command.Stdin = strings.NewReader("")
	return command
}

func historyProcessExit(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	return exit.ExitCode()
}

func (f *historyProcessFixture) run(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	before, beforeErr := os.ReadFile(f.profiles)
	command := f.command(t, args...)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	output, err := command.Output()
	exit := historyProcessExit(t, err)
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(output))
	require.NoError(t, decoder.Decode(&document), "exit=%d diagnostic=%q", exit, diagnostic.String())
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	require.EqualValues(t, 1, document["schema_version"])
	require.Equal(t, exit == 0, document["ok"])
	after, afterErr := os.ReadFile(f.profiles)
	if beforeErr == nil {
		require.NoError(t, afterErr)
		require.True(t, bytes.Equal(before, after), "recording rewrote profiles")
	} else {
		require.ErrorIs(t, afterErr, os.ErrNotExist)
	}
	return exit, document
}

func readProcessHistory(t *testing.T, path string) []query.HistoryEvent {
	t.Helper()
	_, err := os.Stat(path)
	require.NoError(t, err, "history must already exist; reading cannot create/migrate it")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT executed_at,sql,duration_ms,connection_id,source,
CASE WHEN blocked!=0 THEN 'blocked' WHEN succeeded!=0 THEN 'succeeded' ELSE 'failed' END FROM history ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var events []query.HistoryEvent
	for rows.Next() {
		var event query.HistoryEvent
		require.NoError(t, rows.Scan(&event.ExecutedAt, &event.SQL, &event.DurationMS, &event.ConnectionID, &event.Source, &event.Outcome))
		events = append(events, event)
	}
	require.NoError(t, rows.Err())
	return events
}

func TestCLIHistoryProcess_ExitPersistence(t *testing.T) {
	f := newHistoryProcess(t, historyConsent(true))
	const original = "DELETE FROM never_executed -- original SQL literal\n"
	start := time.Now().UnixMilli()
	exit, doc := f.run(t, "query", "--connection=dev", "--file="+f.sql(t, original))
	require.Equal(t, ExitInput, exit)
	require.Equal(t, string(UnsupportedSQL), doc["error"].(map[string]any)["code"])
	// The very next operation is a read-only reopen. No queue/timer sleep.
	events := readProcessHistory(t, f.store)
	require.Len(t, events, 1)
	require.Equal(t, original, events[0].SQL)
	require.Equal(t, "cli", events[0].Source)
	require.Equal(t, "dev", events[0].ConnectionID)
	require.Equal(t, "blocked", events[0].Outcome)
	require.GreaterOrEqual(t, events[0].ExecutedAt, start)
	require.LessOrEqual(t, events[0].ExecutedAt, time.Now().UnixMilli())
	require.GreaterOrEqual(t, events[0].DurationMS, int64(0))
	f.assertArtifacts(t, true)
}

func (f *historyProcessFixture) assertArtifacts(t *testing.T, history bool) {
	t.Helper()
	for _, dir := range []string{"data", "cache", "runtime"} {
		_, err := os.Stat(filepath.Join(f.root, dir))
		require.ErrorIs(t, err, os.ErrNotExist, "machine startup initialized %s", dir)
	}
	state := filepath.Join(f.root, "state")
	if !history {
		_, err := os.Stat(state)
		require.ErrorIs(t, err, os.ErrNotExist)
		return
	}
	require.NoError(t, filepath.WalkDir(state, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			return nil
		}
		rel, err := filepath.Rel(state, path)
		if err != nil {
			return err
		}
		require.Contains(t, []string{"pgsavvy/history.sqlite", "pgsavvy/history.sqlite-wal", "pgsavvy/history.sqlite-shm", "pgsavvy/history.sqlite-journal"}, rel)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		return nil
	}))
	entries, err := os.ReadDir(filepath.Dir(f.profiles))
	require.NoError(t, err)
	require.Len(t, entries, 1, "metadata lookup created profile locks/configuration")
	require.Equal(t, "connections.yml", entries[0].Name())
}

func TestCLIHistoryProcess_DefaultNoState(t *testing.T) {
	for _, fixture := range []string{"absent", "false", "empty", "unknown", "duplicate", "malformed", "insecure", "help", "flags", "empty_sql", "oversized_sql", "unreadable_sql", "other_command"} {
		t.Run(fixture, func(t *testing.T) {
			consent := historyConsent(true)
			switch fixture {
			case "absent":
				consent = nil
			case "false":
				consent = historyConsent(false)
			}
			f := newHistoryProcess(t, consent)
			text := "DELETE FROM example"
			switch fixture {
			case "empty":
				require.NoError(t, os.WriteFile(f.profiles, []byte("connections: []\n"), 0o600))
			case "unknown":
				f.save(t, models.Connection{Name: "different", Driver: "postgres"})
			case "duplicate":
				require.NoError(t, os.WriteFile(f.profiles, []byte("connections:\n - name: dev\n   cli_history: true\n - name: dev\n   cli_history: true\n"), 0o600))
			case "malformed":
				require.NoError(t, os.WriteFile(f.profiles, []byte("connections: [malformed"), 0o600))
			case "insecure":
				require.NoError(t, os.Chmod(f.profiles, 0o666))
			case "empty_sql":
				text = " \n\t"
			case "oversized_sql":
				text = strings.Repeat("x", MaxInputBytes+1)
			}
			args := []string{"query", "--connection=dev", "--file=" + f.sql(t, text)}
			want := ExitInput
			switch fixture {
			case "help":
				args, want = []string{"query", "--help"}, ExitSuccess
			case "other_command":
				args, want = []string{"connections", "list"}, ExitSuccess
			case "flags":
				args = append(args, "--not-a-flag")
			case "unreadable_sql":
				args, want = []string{"query", "--connection=dev", "--file=" + filepath.Join(f.root, "missing.sql")}, ExitResource
			}
			exit, _ := f.run(t, args...)
			require.Equal(t, want, exit)
			f.assertArtifacts(t, false)
		})
	}
}

func TestCLIHistoryProcess_SQLBoundary(t *testing.T) {
	f := newHistoryProcess(t, historyConsent(true))
	const prefix = "DELETE FROM boundary_example"
	text := prefix + strings.Repeat(" ", MaxInputBytes-len(prefix))
	exit, _ := f.run(t, "query", "--connection=dev", "--file="+f.sql(t, text))
	require.Equal(t, ExitInput, exit)
	events := readProcessHistory(t, f.store)
	require.Len(t, events, 1)
	require.Equal(t, text, events[0].SQL)
}

func TestCLIHistoryProcess_PrivacyAndOutput(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("backend_failure_%t", failure), func(t *testing.T) {
			f := newHistoryProcess(t, historyConsent(true))
			service := historyWireService(t, &historyWireScript{rows: []string{"RESULT_SENTINEL"}, terminalError: failure}, nil)
			profile, err := service.historyLookup(context.Background(), "dev")
			require.NoError(t, err)
			profile.Password = "PASSWORD_SENTINEL"
			f.save(t, profile)
			require.NoError(t, os.MkdirAll(filepath.Dir(f.store), 0o700))
			h, err := query.New(f.store)
			require.NoError(t, err)
			defer func() { _ = h.Close() }()
			// Pin a reader so the child's WAL/SHM remain inspectable after exit.
			db, err := sql.Open("sqlite", f.store)
			require.NoError(t, err)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			_, err = db.Exec(`BEGIN; SELECT COUNT(*) FROM history`)
			require.NoError(t, err)
			defer func() { _, _ = db.Exec(`ROLLBACK`) }()
			params := filepath.Join(f.root, "parameters.json")
			require.NoError(t, os.WriteFile(params, []byte(`["PARAMETER_SENTINEL"]`), 0o600))
			const original = "/* SQL_SENTINEL */ SELECT $1::text AS history_payload"
			exit, _ := f.run(t, "query", "--connection=dev", "--file="+f.sql(t, original), "--params-file="+params)
			want, outcome := ExitSuccess, "succeeded"
			if failure {
				want, outcome = ExitDatabase, "failed"
			}
			require.Equal(t, want, exit)
			events := readProcessHistory(t, f.store)
			require.Len(t, events, 1)
			require.Equal(t, original, events[0].SQL)
			require.Equal(t, outcome, events[0].Outcome)
			rows, err := h.SearchByPrefix(context.Background(), "SQL_SENTINEL", 10)
			require.NoError(t, err)
			require.Equal(t, []string{original}, rows)
			var bytesRead int
			var sqlSeen bool
			for _, suffix := range []string{"", "-wal", "-shm"} {
				data, err := os.ReadFile(f.store + suffix)
				require.NoError(t, err, "live sidecar %s missing", suffix)
				bytesRead += len(data)
				sqlSeen = sqlSeen || bytes.Contains(data, []byte("SQL_SENTINEL"))
				for _, secret := range []string{"PARAMETER_SENTINEL", "RESULT_SENTINEL", "PASSWORD_SENTINEL", "backend-secret"} {
					require.NotContains(t, string(data), secret)
				}
			}
			require.Positive(t, bytesRead)
			require.True(t, sqlSeen, "original SQL sentinel is not present in the SQLite artifacts")
			f.assertArtifacts(t, true)
		})
	}
}

func (f *historyProcessFixture) installV1(t *testing.T) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(f.store), 0o700))
	require.NoError(t, os.WriteFile(f.store, nil, 0o600))
	db, err := sql.Open("sqlite", f.store)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ddl, err := os.ReadFile(filepath.Join("..", "query", "sql", "history_001.sql"))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE _migrations(version INTEGER PRIMARY KEY); INSERT INTO _migrations VALUES (1);` + string(ddl))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO history(id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id) VALUES(42,1234,'SELECT legacy_process',7,9,1,'old-profile')`)
	require.NoError(t, err)
}

func TestCLIHistoryProcess_ConcurrentStartup(t *testing.T) {
	for _, version := range []string{"fresh", "v1", "current", "locked"} {
		t.Run(version, func(t *testing.T) {
			f := newHistoryProcess(t, historyConsent(true))
			seeded := 0
			switch version {
			case "v1":
				f.installV1(t)
				seeded = 1
			case "current", "locked":
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				require.NoError(t, query.AppendHistory(ctx, f.store, query.HistoryEvent{SQL: "SELECT existing_process", Source: "tui", Outcome: "succeeded"}))
				seeded = 1
			}
			if version == "locked" {
				db, err := sql.Open("sqlite", f.store)
				require.NoError(t, err)
				db.SetMaxOpenConns(1)
				defer func() { _, _ = db.Exec(`ROLLBACK`); _ = db.Close() }()
				_, err = db.Exec(`BEGIN IMMEDIATE`)
				require.NoError(t, err)
			}
			before, err := os.ReadFile(f.profiles)
			require.NoError(t, err)
			var commands []*exec.Cmd
			var starts []*os.File
			var outputs []*bytes.Buffer
			var statements []string
			for i := range 4 {
				statement := fmt.Sprintf("DELETE FROM concurrently_blocked -- process %d", i)
				file := filepath.Join(f.root, fmt.Sprintf("query-%d.sql", i))
				require.NoError(t, os.WriteFile(file, []byte(statement), 0o600))
				statements = append(statements, statement)
				read, write, err := os.Pipe()
				require.NoError(t, err)
				command := f.command(t, "query", "--connection=dev", "--file="+file)
				command.Env = append(append([]string{}, f.environment...), "PGSAVVY_HISTORY_PROCESS_START=1")
				command.ExtraFiles = []*os.File{read}
				output := &bytes.Buffer{}
				command.Stdout, command.Stderr = output, io.Discard
				require.NoError(t, command.Start())
				require.NoError(t, read.Close())
				t.Cleanup(func() {
					_ = write.Close()
					if command.ProcessState == nil {
						_ = command.Process.Kill()
						_ = command.Wait()
					}
				})
				commands, starts, outputs = append(commands, command), append(starts, write), append(outputs, output)
			}
			for _, gate := range starts {
				_, err := gate.Write([]byte{1})
				require.NoError(t, err)
				require.NoError(t, gate.Close())
			}
			for i, command := range commands {
				require.Equal(t, ExitInput, historyProcessExit(t, command.Wait()), outputs[i].String())
			}
			events := readProcessHistory(t, f.store)
			bySQL := map[string]int{}
			var cliCount int
			for _, event := range events {
				if event.Source == "cli" {
					cliCount++
					require.Contains(t, statements, event.SQL)
					bySQL[event.SQL]++
					require.Equal(t, "dev", event.ConnectionID)
					require.Equal(t, "blocked", event.Outcome)
				}
			}
			require.Len(t, events, seeded+cliCount)
			for i, output := range outputs {
				var envelope Envelope
				decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
				require.NoError(t, decoder.Decode(&envelope))
				require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
				require.Equal(t, 1, envelope.SchemaVersion)
				require.False(t, envelope.OK)
				require.NotNil(t, envelope.Error)
				require.Equal(t, UnsupportedSQL, envelope.Error.Code)
				if version == "locked" {
					require.Equal(t, []ErrorCode{HistoryUnavailable}, envelope.Warnings)
					require.Zero(t, bySQL[statements[i]])
				} else if len(envelope.Warnings) == 0 {
					require.Equal(t, 1, bySQL[statements[i]], "unwarned append must be committed at exit: %s", output)
				} else {
					// Contention may lose best-effort history, never silently or with duplicates.
					require.Len(t, envelope.Warnings, 1)
					allowed := []ErrorCode{HistoryUnavailable}
					if version == "v1" {
						allowed = append(allowed, HistoryUpgradeRequired)
					}
					require.Contains(t, allowed, envelope.Warnings[0])
					require.LessOrEqual(t, bySQL[statements[i]], 1)
				}
			}
			if version == "v1" {
				require.Equal(t, query.HistoryEvent{ExecutedAt: 1234, SQL: "SELECT legacy_process", DurationMS: 7, ConnectionID: "old-profile", Source: "tui", Outcome: "succeeded"}, events[0])
			} else if seeded != 0 {
				require.Equal(t, query.HistoryEvent{SQL: "SELECT existing_process", Source: "tui", Outcome: "succeeded"}, events[0])
			}
			after, err := os.ReadFile(f.profiles)
			require.NoError(t, err)
			require.True(t, bytes.Equal(before, after), "recording rewrote profiles")
			f.assertArtifacts(t, true)
		})
	}
}

func TestCLIHistoryProcess_StoreFailures(t *testing.T) {
	for _, fixture := range []string{"locked", "corrupt", "broad", "read_only"} {
		t.Run(fixture, func(t *testing.T) {
			if fixture == "read_only" && os.Geteuid() == 0 {
				t.Skip("native permission-denial fixture requires an unprivileged process")
			}
			f := newHistoryProcess(t, historyConsent(true))
			h, err := query.New(f.store)
			require.NoError(t, err)
			require.NoError(t, h.Close())
			var unlock func()
			switch fixture {
			case "locked":
				db, err := sql.Open("sqlite", f.store)
				require.NoError(t, err)
				db.SetMaxOpenConns(1)
				_, err = db.Exec(`BEGIN IMMEDIATE`)
				require.NoError(t, err)
				unlock = func() { _, _ = db.Exec(`ROLLBACK`); _ = db.Close() }
				defer unlock()
			case "corrupt":
				require.NoError(t, os.WriteFile(f.store, []byte("CORRUPT_STORE_SENTINEL"), 0o600))
			case "broad":
				require.NoError(t, os.Chmod(f.store, 0o644))
			case "read_only":
				require.NoError(t, os.Chmod(f.store, 0o400))
			}
			before, err := os.ReadFile(f.store)
			require.NoError(t, err)
			start := time.Now()
			exit, doc := f.run(t, "query", "--connection=dev", "--file="+f.sql(t, "DELETE FROM example"), "--timeout=1s")
			require.Less(t, time.Since(start), 3*time.Second, "includes race runtime process-exit overhead")
			require.Equal(t, ExitInput, exit)
			require.Contains(t, doc["warnings"], string(HistoryUnavailable))
			after, err := os.ReadFile(f.store)
			require.NoError(t, err)
			require.Equal(t, before, after)
			if fixture == "broad" {
				info, err := os.Stat(f.store)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "no automatic repair")
				// Explicit stopped-writer remediation, only on this test-owned file.
				require.NoError(t, os.Chmod(f.store, 0o600))
				exit, doc = f.run(t, "query", "--connection=dev", "--file="+f.sql(t, "DELETE FROM example"))
				require.Equal(t, ExitInput, exit)
				require.Empty(t, doc["warnings"])
				require.Len(t, readProcessHistory(t, f.store), 1)
			}
		})
	}
}

func TestCLIHistoryProcess_CancellationAndCleanup(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			f := newHistoryProcess(t, historyConsent(true))
			started := make(chan struct{})
			service := historyWireService(t, &historyWireScript{stallTail: true, executing: started}, nil)
			profile, err := service.historyLookup(context.Background(), "dev")
			require.NoError(t, err)
			f.save(t, profile)
			command := f.command(t, "query", "--connection=dev", "--file="+f.sql(t, "SELECT 'history_payload'"))
			var output, diagnostic bytes.Buffer
			command.Stdout, command.Stderr = &output, &diagnostic
			require.NoError(t, command.Start())
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			})
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal("query never reached its cancellation barrier")
			}
			require.NoError(t, command.Process.Signal(sig))
			require.Equal(t, ExitTimeout, historyProcessExit(t, command.Wait()), output.String())
			events := readProcessHistory(t, f.store)
			require.Len(t, events, 1)
			require.Equal(t, "failed", events[0].Outcome)
		})
	}
	for _, fixture := range []string{"short_deadline", "cleanup_failure", "output_overflow"} {
		t.Run(fixture, func(t *testing.T) {
			f := newHistoryProcess(t, historyConsent(true))
			script := &historyWireScript{rollbackError: fixture == "cleanup_failure", stallTail: fixture == "short_deadline"}
			if fixture == "output_overflow" {
				for range 17 {
					script.rows = append(script.rows, strings.Repeat("x", 64<<10))
				}
			}
			service := historyWireService(t, script, nil)
			profile, err := service.historyLookup(context.Background(), "dev")
			require.NoError(t, err)
			f.save(t, profile)
			args := []string{"query", "--connection=dev", "--file=" + f.sql(t, "SELECT 'history_payload'")}
			want := ExitDatabase
			switch fixture {
			case "short_deadline":
				args, want = append(args, "--timeout=500ms"), ExitTimeout
			case "output_overflow":
				want = ExitResource
			}
			exit, _ := f.run(t, args...)
			require.Equal(t, want, exit)
			events := readProcessHistory(t, f.store)
			require.Len(t, events, 1)
			require.Equal(t, "failed", events[0].Outcome)
		})
	}
}

func TestCLIHistoryProcess_OutputPipes(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken_%t", broken), func(t *testing.T) {
			f := newHistoryProcess(t, historyConsent(true))
			script := &historyWireScript{}
			for range 10 {
				script.rows = append(script.rows, strings.Repeat("x", 50<<10))
			}
			service := historyWireService(t, script, nil)
			profile, err := service.historyLookup(context.Background(), "dev")
			require.NoError(t, err)
			f.save(t, profile)
			read, write, err := os.Pipe()
			require.NoError(t, err)
			defer func() { _ = read.Close(); _ = write.Close() }()
			if broken {
				require.NoError(t, read.Close())
			}
			command := f.command(t, "query", "--connection=dev", "--file="+f.sql(t, "SELECT 'history_payload'"), "--timeout=1s")
			var diagnostic bytes.Buffer
			command.Stdout, command.Stderr = write, &diagnostic
			start := time.Now()
			require.Equal(t, ExitResource, historyProcessExit(t, command.Run()))
			require.Less(t, time.Since(start), 3*time.Second, "unread output was not joined/bounded")
			events := readProcessHistory(t, f.store)
			require.Len(t, events, 1)
			require.Equal(t, "succeeded", events[0].Outcome, "delivery failure rewrote completed query")
			require.Contains(t, diagnostic.String(), `"exit":7`)
		})
	}
}

type legacyHistoryRow struct {
	ID, Timestamp, Duration, Affected, Succeeded int64
	SQL, Profile                                 string
}

func legacyHistoryRows(t *testing.T, db *sql.DB) []legacyHistoryRow {
	t.Helper()
	rows, err := db.Query(`SELECT id,executed_at,sql,duration_ms,rows_affected,succeeded,connection_id FROM history ORDER BY id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var result []legacyHistoryRow
	for rows.Next() {
		var row legacyHistoryRow
		require.NoError(t, rows.Scan(&row.ID, &row.Timestamp, &row.SQL, &row.Duration, &row.Affected, &row.Succeeded, &row.Profile))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

func TestCLIHistory_RollbackRehearsal(t *testing.T) {
	f := newHistoryProcess(t, historyConsent(true))
	exit, _ := f.run(t, "query", "--connection=dev", "--file="+f.sql(t, "DELETE FROM rollback_canary -- ROLLBACK_SQL_SENTINEL"))
	require.Equal(t, ExitInput, exit)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, query.AppendHistory(ctx, f.store, query.HistoryEvent{ExecutedAt: 1234, SQL: "SELECT rollback_tui", DurationMS: 19, ConnectionID: "old-profile", Source: "tui", Outcome: "succeeded"}))
	reader, err := sql.Open("sqlite", f.store)
	require.NoError(t, err)
	reader.SetMaxOpenConns(1)
	_, err = reader.Exec(`BEGIN; SELECT COUNT(*) FROM history`)
	require.NoError(t, err)
	defer func() { _, _ = reader.Exec(`ROLLBACK`); _ = reader.Close() }()
	// The old reader prevents checkpointing this later committed event, even
	// though its writer has stopped. The backup must include the live WAL.
	require.NoError(t, query.AppendHistory(ctx, f.store, query.HistoryEvent{ExecutedAt: 2345, SQL: "UPDATE rollback_wal_canary", DurationMS: 11, ConnectionID: "dev", Source: "cli", Outcome: "blocked"}))
	wal, err := os.Stat(f.store + "-wal")
	require.NoError(t, err)
	require.Positive(t, wal.Size())
	// All appenders have exited/joined. VACUUM INTO includes committed WAL data;
	// never copy just the main file of a running WAL database.
	source, err := sql.Open("sqlite", f.store)
	require.NoError(t, err)
	before := legacyHistoryRows(t, source)
	backup := filepath.Join(f.root, "stopped-writer-backup.sqlite")
	_, err = source.Exec(`VACUUM INTO ?`, backup)
	require.NoError(t, err)
	require.NoError(t, source.Close())
	require.NoError(t, os.Chmod(backup, 0o600))
	db, err := sql.Open("sqlite", backup)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	down, err := os.ReadFile(filepath.Join("..", "query", "sql", "rollback_002.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var version, columns, matches int
	require.NoError(t, db.QueryRow(`SELECT MAX(version) FROM _migrations`).Scan(&version))
	require.Equal(t, 1, version)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('history')`).Scan(&columns))
	require.Equal(t, 7, columns)
	require.Equal(t, before, legacyHistoryRows(t, db))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'rollback'`).Scan(&matches))
	require.Equal(t, 3, matches)
	// Equivalent predecessor parser for this fixture: all its existing keys are
	// known, while the new cli_history key must fail until explicitly removed.
	type predecessorProfile struct {
		Name      string           `yaml:"name"`
		Driver    string           `yaml:"driver"`
		DSN       string           `yaml:"dsn"`
		Password  string           `yaml:"password"`
		ReadOnly  bool             `yaml:"read_only"`
		CLIAccess models.CLIAccess `yaml:"cli_access"`
	}
	decodeOld := func(data []byte) error {
		var wrapper struct {
			Connections []predecessorProfile `yaml:"connections"`
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		return decoder.Decode(&wrapper)
	}
	profiles, err := os.ReadFile(f.profiles)
	require.NoError(t, err)
	require.ErrorContains(t, decodeOld(profiles), "cli_history")
	var document yaml.Node
	require.NoError(t, yaml.Unmarshal(profiles, &document))
	profile := document.Content[0].Content[1].Content[0]
	var retained []*yaml.Node
	for i := 0; i < len(profile.Content); i += 2 {
		if profile.Content[i].Value != "cli_history" {
			retained = append(retained, profile.Content[i], profile.Content[i+1])
		}
	}
	profile.Content = retained
	oldConfiguration, err := yaml.Marshal(&document)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(f.root, "rollback-connections.yml"), oldConfiguration, 0o600))
	require.NoError(t, decodeOld(oldConfiguration))
	_, err = db.Exec(`INSERT INTO history(executed_at,sql,duration_ms,rows_affected,succeeded,connection_id) VALUES(5678,'SELECT predecessor_append',23,29,0,'predecessor')`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM history_fts WHERE history_fts MATCH 'predecessor'`).Scan(&matches))
	require.Equal(t, 1, matches)
	require.NoError(t, db.Close())
	require.NoError(t, query.AppendHistory(ctx, backup, query.HistoryEvent{SQL: "SELECT after_upgrade", Source: "cli", Outcome: "succeeded"}))
	events := readProcessHistory(t, backup)
	require.Len(t, events, 5)
	require.Equal(t, "tui", events[0].Source, "rollback intentionally loses source")
	require.Equal(t, "failed", events[0].Outcome, "rollback intentionally loses blocked distinction")
	require.Equal(t, "succeeded", events[1].Outcome)
	require.Equal(t, "failed", events[2].Outcome)
	require.Equal(t, "UPDATE rollback_wal_canary", events[2].SQL)
	copyDB, err := sql.Open("sqlite", backup)
	require.NoError(t, err)
	defer func() { _ = copyDB.Close() }()
	require.Equal(t, before, legacyHistoryRows(t, copyDB)[:len(before)], "retained fields changed after down/up")
	unchanged, err := os.ReadFile(f.profiles)
	require.NoError(t, err)
	require.True(t, bytes.Equal(profiles, unchanged), "rehearsal altered the source configuration")
}
