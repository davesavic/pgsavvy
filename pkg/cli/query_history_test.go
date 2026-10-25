//go:build linux || darwin

package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

func runHistoryQuery(t *testing.T, service InvocationService, sql string, extra ...string) (int, string) {
	t.Helper()
	streams, out, _ := testStreams()
	streams.In = io.NopCloser(strings.NewReader(sql))
	args := append([]string{"query", "--connection", "dev", "--file", "-"}, extra...)
	_, exit := Dispatch(context.Background(), args, streams, map[string]Command{"query": QueryCommand(service)})
	return exit, out.String()
}

func historyService(profile models.Connection, record func(context.Context, string, query.HistoryEvent) error) InvocationService {
	return InvocationService{
		Load:          func() ([]models.Connection, error) { return nil, errors.New("snapshot reloaded") },
		historyLookup: func(context.Context, string) (models.Connection, error) { return profile, nil },
		historyAppend: record,
		historyPath:   "/unused-test-history.sqlite",
	}
}

func prepareEmptyHistoryFixture(t *testing.T, path string) {
	t.Helper()
	require.NoFileExists(t, path)
	// Cold fixture migrations are setup, not a test of the TUI/CLI's 250 ms budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, query.AppendHistory(ctx, path, query.HistoryEvent{SQL: "SELECT history_fixture_setup", Source: "tui", Outcome: "succeeded"}))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.ExecContext(ctx, `DELETE FROM history`)
	require.NoError(t, err)
}

func TestCLIHistory_MetadataOnlyRejection(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	var lookups, appends, forbidden atomic.Int32
	service := historyService(models.Connection{Name: "dev", CLIHistory: true, PasswordCommand: "must-not-run"}, func(_ context.Context, _ string, event query.HistoryEvent) error {
		appends.Add(1)
		require.Equal(t, "blocked", event.Outcome)
		require.Equal(t, "cli", event.Source)
		require.Equal(t, "dev", event.ConnectionID)
		require.Contains(t, event.SQL, "DELETE")
		return nil
	})
	service.historyLookup = func(context.Context, string) (models.Connection, error) {
		lookups.Add(1)
		return models.Connection{Name: "dev", CLIHistory: true, PasswordCommand: "must-not-run"}, nil
	}
	service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
		forbidden.Add(1)
		return nil, errors.New("must not parse credentials")
	}
	service.Options.Dial = func(context.Context, string, string) (net.Conn, error) {
		forbidden.Add(1)
		return nil, errors.New("must not connect")
	}
	exit, output := runHistoryQuery(t, service, "DELETE FROM private -- original literal")
	require.Equal(t, ExitInput, exit)
	require.Contains(t, output, `"code":"UNSUPPORTED_SQL"`)
	require.EqualValues(t, 1, lookups.Load())
	require.EqualValues(t, 1, appends.Load())
	require.Zero(t, forbidden.Load())
}

func TestCLIHistory_DefaultNoState(t *testing.T) {
	for _, fixture := range []string{"absent", "false", "true", "duplicate", "malformed", "null", "insecure", "oversized", "missing"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			profilesPath := filepath.Join(dir, "connections.yml")
			text := "connections:\n - name: dev\n   driver: postgres\n"
			switch fixture {
			case "false":
				text += "   cli_history: false\n"
			case "true", "insecure":
				text += "   cli_history: true\n"
			case "duplicate":
				text += "   cli_history: true\n - name: dev\n   cli_history: true\n"
			case "null":
				text += "   cli_history: null\n"
			case "malformed":
				text = "connections: [ { bad: SECRET"
			case "oversized":
				text += "   cli_history: true\n#" + strings.Repeat("x", MaxInputBytes)
			}
			if fixture != "missing" {
				require.NoError(t, os.WriteFile(profilesPath, []byte(text), 0o600))
			}
			if fixture == "insecure" {
				require.NoError(t, os.Chmod(profilesPath, 0o666))
			}
			path := filepath.Join(dir, "state", "history.sqlite")
			service := InvocationService{
				historyLookup: func(ctx context.Context, name string) (models.Connection, error) {
					return historyProfileMetadata(ctx, profilesPath, name)
				},
				historyPath: path,
			}
			exit, output := runHistoryQuery(t, service, "UPDATE example SET x=1")
			require.Equal(t, ExitInput, exit)
			require.Contains(t, output, `"code":"UNSUPPORTED_SQL"`)
			if fixture == "true" {
				h, err := query.New(path)
				require.NoError(t, err)
				defer func() { _ = h.Close() }()
				rows, err := h.Recent(context.Background(), 10)
				require.NoError(t, err)
				require.Len(t, rows, 1)
			} else {
				_, err := os.Stat(filepath.Dir(path))
				require.ErrorIs(t, err, os.ErrNotExist, "unresolved consent touched history")
			}
			if fixture != "missing" {
				data, err := os.ReadFile(profilesPath)
				require.NoError(t, err)
				require.Equal(t, text, string(data))
			}
		})
	}
}

func TestCLIHistory_MetadataBoundaries(t *testing.T) {
	for _, fixture := range []string{"exact_limit", "over_limit", "fifo", "directory", "symlink", "hardlink", "canceled", "invalid_utf8", "unsafe_ancestor"} {
		t.Run(fixture, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "connections.yml")
			text := "connections:\n - name: dev\n   cli_history: true\n#"
			switch fixture {
			case "fifo":
				require.NoError(t, syscall.Mkfifo(path, 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink", "hardlink":
				target := filepath.Join(t.TempDir(), "source")
				require.NoError(t, os.WriteFile(target, []byte(text), 0o600))
				if fixture == "symlink" {
					require.NoError(t, os.Symlink(target, path))
				} else {
					require.NoError(t, os.Link(target, path))
				}
			default:
				switch fixture {
				case "exact_limit", "over_limit":
					text += strings.Repeat("x", MaxInputBytes-len(text))
					if fixture == "over_limit" {
						text += "x"
					}
				case "invalid_utf8":
					text += "\xff"
				}
				require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
				if fixture == "unsafe_ancestor" {
					require.NoError(t, os.Chmod(dir, 0o777))
				}
			}
			budget := 250 * time.Millisecond
			if fixture == "exact_limit" {
				// This case proves inclusive byte admission, independently of
				// parser speed. Cancellation has separate strict-budget fixtures.
				budget = 2 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			if fixture == "canceled" {
				cancel()
			}
			start := time.Now()
			profile, err := historyProfileMetadata(ctx, path, "dev")
			require.Less(t, time.Since(start), budget)
			if fixture == "exact_limit" {
				require.NoError(t, err)
				require.True(t, profile.CLIHistory)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCLIHistory_ConsentSnapshotAndExhaustion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connections.yml")
	var count int
	service := InvocationService{
		historyLookup: func(ctx context.Context, name string) (models.Connection, error) {
			return historyProfileMetadata(ctx, path, name)
		},
		historyAppend: func(context.Context, string, query.HistoryEvent) error { count++; return nil },
	}
	for _, enabled := range []bool{true, false} {
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("connections:\n - name: dev\n   cli_history: %t\n", enabled)), 0o600))
		exit, _ := runHistoryQuery(t, service, "DELETE FROM example")
		require.Equal(t, ExitInput, exit)
		require.Equal(t, 1, count, "revocation must affect the next invocation")
	}
	service.historyLookup = func(ctx context.Context, _ string) (models.Connection, error) {
		<-ctx.Done()
		return models.Connection{}, ctx.Err()
	}
	start := time.Now()
	exit, output := runHistoryQuery(t, service, "DELETE FROM example")
	require.Less(t, time.Since(start), 350*time.Millisecond)
	require.Equal(t, ExitInput, exit, "metadata budget cannot change SQL rejection")
	require.Equal(t, 1, count)
	require.Contains(t, output, `"warnings":[]`)
}

func TestCLIHistory_LockedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.sqlite")
	prepareEmptyHistoryFixture(t, path)
	h, err := query.New(path)
	require.NoError(t, err)
	defer func() { _ = h.Close() }()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`BEGIN IMMEDIATE`)
	require.NoError(t, err)
	service := historyService(models.Connection{Name: "dev", CLIHistory: true}, nil)
	service.historyPath = path
	start := time.Now()
	exit, output := runHistoryQuery(t, service, "DELETE FROM example")
	require.Less(t, time.Since(start), 350*time.Millisecond)
	require.Equal(t, ExitInput, exit)
	require.Contains(t, output, `"code":"UNSUPPORTED_SQL"`)
	require.Contains(t, output, string(HistoryUnavailable))
	_, err = db.Exec(`ROLLBACK`)
	require.NoError(t, err)
	exit, output = runHistoryQuery(t, service, "DELETE FROM example")
	require.Equal(t, ExitInput, exit)
	require.NotContains(t, output, string(HistoryUnavailable))
	rows, err := h.Recent(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, rows, 1, "timed-out append must not leave a late/duplicate writer")
}

func TestCLIHistory_ExternalCancellation(t *testing.T) {
	for _, parentCancellation := range []bool{false, true} {
		started := make(chan struct{})
		var events []query.HistoryEvent
		service := historyWireService(t, &historyWireScript{stallTail: true, executing: started}, func(_ context.Context, _ string, e query.HistoryEvent) error { events = append(events, e); return nil })
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		signal, joined := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(joined)
			<-started
			if parentCancellation {
				cancel()
			} else {
				close(signal)
			}
		}()
		streams, out, _ := testStreams()
		streams.In, streams.Cancel = io.NopCloser(strings.NewReader("SELECT 'history_payload'")), signal
		start := time.Now()
		_, exit := Dispatch(ctx, []string{"query", "--connection=dev", "--file=-"}, streams, map[string]Command{"query": QueryCommand(service)})
		<-joined
		require.Less(t, time.Since(start), 500*time.Millisecond)
		require.Equal(t, ExitTimeout, exit, out.String())
		require.Len(t, events, 1)
		require.Equal(t, "failed", events[0].Outcome)
	}
	// Cancellation during history is later than query/DB cleanup. It retains
	// normal signal exit behavior but cannot rewrite that completed outcome.
	signal := make(chan struct{})
	var events []query.HistoryEvent
	service := historyWireService(t, &historyWireScript{}, func(_ context.Context, _ string, e query.HistoryEvent) error {
		events = append(events, e)
		close(signal)
		return nil
	})
	streams, out, _ := testStreams()
	streams.In, streams.Cancel = io.NopCloser(strings.NewReader("SELECT 'history_payload'")), signal
	_, exit := Dispatch(context.Background(), []string{"query", "--connection=dev", "--file=-"}, streams, map[string]Command{"query": QueryCommand(service)})
	require.Equal(t, ExitTimeout, exit, out.String())
	require.Len(t, events, 1)
	require.Equal(t, "succeeded", events[0].Outcome)
}

func TestCLIHistory_QueryOutcomes(t *testing.T) {
	for _, fixture := range []string{"blocked_policy", "parameter", "credential", "connect", "zero_rows", "capped", "tail_error", "cleanup_error", "close_error", "cancel", "long_query"} {
		t.Run(fixture, func(t *testing.T) {
			var events []query.HistoryEvent
			profile := models.Connection{Name: "dev", Driver: "postgres", CLIHistory: true, CLIAccess: models.CLIAccessRowRead, ReadOnly: false}
			service := historyService(profile, func(_ context.Context, _ string, event query.HistoryEvent) error {
				events = append(events, event)
				return nil
			})
			wantOutcome, wantExit := "failed", ExitUnavailable
			var extra []string
			var script *historyWireScript
			switch fixture {
			case "blocked_policy":
				profile.CLIAccess = models.CLIAccessDisabled
				service = historyService(profile, service.historyAppend)
				wantOutcome, wantExit = "blocked", ExitPolicy
			case "parameter":
				extra = []string{"--params-file", "-"}
				wantExit = ExitInput
			case "credential":
				service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
					return nil, &pg.MachineError{Code: "CREDENTIALS_UNAVAILABLE"}
				}
			case "connect":
				profile.Password, profile.DSN = "fixture", "postgres://u@127.0.0.1/d?sslmode=disable"
				service = historyService(profile, service.historyAppend)
				service.Options.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("backend-secret") }
			default:
				script = &historyWireScript{}
				if fixture == "capped" || fixture == "tail_error" {
					script.rows = []string{"result-sentinel", "discarded-result"}
					extra = []string{"--max-rows", "1"}
				}
				script.terminalError = fixture == "tail_error"
				script.rollbackError = fixture == "cleanup_error"
				script.closeError = fixture == "close_error"
				script.stallTail = fixture == "cancel"
				if fixture == "long_query" {
					script.delay = 300 * time.Millisecond
				}
				switch fixture {
				case "cancel":
					extra = []string{"--timeout=100ms"}
				case "close_error":
					extra = []string{"--timeout=500ms"}
				}
				service = historyWireService(t, script, service.historyAppend)
				wantOutcome, wantExit = "succeeded", ExitSuccess
				switch fixture {
				case "tail_error", "cleanup_error":
					wantOutcome, wantExit = "failed", ExitDatabase
				case "close_error":
					wantOutcome, wantExit = "failed", ExitResource
				case "cancel":
					wantOutcome, wantExit = "failed", ExitTimeout
				}
			}
			const sql = "/* original SQL literal */ SELECT 'history_payload'"
			exit, output := runHistoryQuery(t, service, sql, extra...)
			require.Equal(t, wantExit, exit, output)
			require.Len(t, events, 1)
			require.Equal(t, wantOutcome, events[0].Outcome)
			require.Equal(t, sql, events[0].SQL)
			require.Positive(t, events[0].ExecutedAt)
			require.NotContains(t, output, "backend-secret")
			if script != nil {
				require.True(t, script.readOnly.Load(), "BEGIN READ ONLY lost with ReadOnly=false")
			}
			if fixture == "long_query" {
				require.GreaterOrEqual(t, events[0].DurationMS, int64(250))
			}
		})
	}
}

func TestCLIHistory_BudgetLedger(t *testing.T) {
	var allowance time.Duration
	service := historyService(models.Connection{Name: "dev", CLIHistory: true}, func(ctx context.Context, _ string, _ query.HistoryEvent) error {
		deadline, _ := ctx.Deadline()
		allowance = time.Until(deadline)
		return nil
	})
	service.historyLookup = func(context.Context, string) (models.Connection, error) {
		time.Sleep(30 * time.Millisecond)
		return models.Connection{Name: "dev", CLIHistory: true}, nil
	}
	attempt, _ := service.beginQueryHistory(context.Background(), "dev", "SELECT 1", time.Now())
	require.NotNil(t, attempt)
	time.Sleep(270 * time.Millisecond) // Database time does not age remaining work.
	attempt.complete(context.Background(), nil)
	require.Empty(t, attempt.finalize(time.Now().Add(time.Second)))
	require.Positive(t, allowance)
	require.LessOrEqual(t, allowance, 225*time.Millisecond)
	require.Greater(t, allowance, 100*time.Millisecond)
	require.Empty(t, attempt.finalize(time.Now()), "must append at most once")
	for _, remaining := range []time.Duration{0, time.Second} {
		called := false
		a := queryHistoryAttempt{remaining: remaining, append: func(context.Context, string, query.HistoryEvent) error { called = true; return nil }}
		require.Equal(t, []ErrorCode{HistoryUnavailable}, a.finalize(time.Now().Add(-time.Millisecond)))
		require.False(t, called)
	}
	// History runs into the work deadline but not into publication reserve.
	service = historyWireService(t, &historyWireScript{}, func(ctx context.Context, _ string, _ query.HistoryEvent) error {
		<-ctx.Done()
		return ctx.Err()
	})
	exit, output := runHistoryQuery(t, service, "SELECT 'history_payload'", "--timeout=150ms")
	require.Equal(t, ExitSuccess, exit, output)
	require.Contains(t, output, `"HISTORY_UNAVAILABLE"`)
}

func TestCLIHistory_WarningsPreserveExit(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code ErrorCode
	}{{errors.New("private path SQL backend-secret"), HistoryUnavailable}, {query.ErrHistoryUpgradeRequired, HistoryUpgradeRequired}} {
		service := historyWireService(t, &historyWireScript{}, func(context.Context, string, query.HistoryEvent) error { return tc.err })
		exit, output := runHistoryQuery(t, service, "SELECT 'history_payload'")
		require.Equal(t, ExitSuccess, exit, output)
		require.Contains(t, output, string(tc.code))
		require.NotContains(t, output, "backend-secret")
		exit, output = runHistoryQuery(t, historyService(models.Connection{Name: "dev", CLIHistory: true}, service.historyAppend), "DELETE FROM example")
		require.Equal(t, ExitInput, exit)
		require.Contains(t, output, `"code":"UNSUPPORTED_SQL"`)
		require.Contains(t, output, string(tc.code))
	}
	// Only optional warnings may be dropped to preserve a fitting result.
	envelope := Envelope{Command: "query", OK: true, Data: "", ResultComplete: true}
	base, err := encode(envelope)
	require.NoError(t, err)
	envelope.Data = strings.Repeat("x", MaxOutputBytes-len(base))
	data, err := encode(envelope)
	require.NoError(t, err)
	require.Len(t, data, MaxOutputBytes)
	envelope.Warnings = []ErrorCode{HistoryUnavailable}
	data, err = encodeWithOptionalHistoryWarnings(context.Background(), envelope)
	require.NoError(t, err)
	require.Len(t, data, MaxOutputBytes)
	require.NotContains(t, string(data), string(HistoryUnavailable))
}

func TestCLIHistory_FullApprovalFlagsPrivacy(t *testing.T) {
	parameters := filepath.Join(t.TempDir(), "parameters")
	require.NoError(t, os.WriteFile(parameters, []byte(`["private parameter"]`), 0o600))
	var events []query.HistoryEvent
	service := historyService(models.Connection{Name: "dev", CLIAccess: models.CLIAccessFull, CLIHistory: true, ConfirmWrites: true}, func(_ context.Context, _ string, event query.HistoryEvent) error {
		events = append(events, event)
		return query.ErrHistoryUpgradeRequired
	})
	service.Approve = func(context.Context, ApprovalRequest) error {
		t.Fatal("mismatched explicit flags prompted")
		return nil
	}
	service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
		t.Fatal("mismatched explicit flags parsed credentials")
		return nil, nil
	}
	const sql = "UPDATE example SET x=$1 -- original raw literal"
	exit, output := runHistoryQuery(t, service, sql, "--params-file", parameters, "--approve", "--confirm-connection=Dev")
	require.Equal(t, ExitPolicy, exit, output)
	require.Contains(t, output, string(ApprovalMismatch))
	require.Contains(t, output, string(HistoryUpgradeRequired))
	require.NotContains(t, output, "private parameter")
	require.Len(t, events, 1)
	require.Equal(t, sql, events[0].SQL)
	require.Equal(t, "blocked", events[0].Outcome)
}

func TestCLIHistory_FullApprovalCannotEnableConsent(t *testing.T) {
	script := &historyWireScript{submitted: make(chan string, 2), tags: []string{"UPDATE 1"}}
	appends, lookups := 0, 0
	service := fullWireService(t, script, func(context.Context, string, query.HistoryEvent) error { appends++; return nil })
	profile, err := service.historyLookup(context.Background(), "dev")
	require.NoError(t, err)
	profile.CLIHistory, profile.ConfirmWrites = false, true
	service.historyLookup = func(context.Context, string) (models.Connection, error) { lookups++; return profile, nil }
	service.Approve = func(context.Context, ApprovalRequest) error { profile.CLIHistory = true; return nil }
	service.Options.ParseConfig = func(_ context.Context, captured models.Connection) (*pgx.ConnConfig, error) {
		require.False(t, captured.CLIHistory, "approval-time opt-in changed the captured invocation")
		return pgx.ParseConfig(captured.DSN)
	}
	const sql = "/* history_payload */ UPDATE example SET x=1"
	exit, output := runHistoryQuery(t, service, sql)
	require.Equal(t, ExitSuccess, exit, output)
	require.True(t, profile.CLIHistory, "fixture did not enable consent during approval")
	require.Equal(t, 1, lookups)
	require.Zero(t, appends, "later consent authorized earlier SQL history")
	require.Equal(t, sql, <-script.submitted)
	require.Empty(t, script.submitted)
}

func TestCLIHistory_PublicationAndIsolation(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		var events []query.HistoryEvent
		service := historyWireService(t, &historyWireScript{}, func(_ context.Context, _ string, e query.HistoryEvent) error { events = append(events, e); return nil })
		command := QueryCommand(service)
		streams, _, _ := testStreams()
		streams.In = io.NopCloser(strings.NewReader("SELECT 'history_payload'"))
		if !overflow {
			streams.Out = historyBrokenOutput{}
		}
		_, exit := Dispatch(context.Background(), []string{"query", "--connection=dev", "--file=-"}, streams, map[string]Command{"query": func(ctx context.Context, args []string, streams IO) (Envelope, error) {
			envelope, err := command(ctx, args, streams)
			if overflow {
				envelope.Data = strings.Repeat("x", MaxOutputBytes+1)
			}
			return envelope, err
		}})
		require.Equal(t, ExitResource, exit)
		require.Len(t, events, 1)
		require.Equal(t, "succeeded", events[0].Outcome)
	}
	var mu sync.Mutex
	events := map[string]int{}
	service := InvocationService{
		historyLookup: func(_ context.Context, name string) (models.Connection, error) {
			return models.Connection{Name: name, CLIHistory: true}, nil
		},
		historyAppend: func(_ context.Context, _ string, e query.HistoryEvent) error {
			mu.Lock()
			defer mu.Unlock()
			if e.SQL != "DELETE FROM "+e.ConnectionID || e.Outcome != "blocked" {
				return errors.New("cross-invocation contamination")
			}
			events[e.ConnectionID]++
			return nil
		},
	}
	command := QueryCommand(service)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			name := fmt.Sprintf("profile_%d", i)
			streams, _, _ := testStreams()
			streams.In = io.NopCloser(strings.NewReader("DELETE FROM " + name))
			Dispatch(context.Background(), []string{"query", "--connection=" + name, "--file=-"}, streams, map[string]Command{"query": command})
		})
	}
	wg.Wait()
	require.Len(t, events, 20)
	for _, count := range events {
		require.Equal(t, 1, count)
	}
}

type historyBrokenOutput struct{}

func (historyBrokenOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (historyBrokenOutput) Close() error              { return nil }

type historyWireScript struct {
	rows                         []string
	terminalError, rollbackError bool
	stallTail                    bool
	closeError                   bool
	delay                        time.Duration
	readOnly                     atomic.Bool
	blocked                      atomic.Pointer[serviceBlockedWrite]
	executing                    chan struct{}
	submitted                    chan string
	parameters                   chan [][]byte
	tags                         []string
	disconnect                   bool
}

// A test-owned protocol peer, not a PostgreSQL fixture or an executed database.
func historyWireService(t *testing.T, script *historyWireScript, record func(context.Context, string, query.HistoryEvent) error) InvocationService {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var mu sync.Mutex
	var owned net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		owned = server
		mu.Unlock()
		defer func() { _ = server.Close() }()
		backend := pgproto3.NewBackend(server, server)
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
		statement, status := "", byte('I')
		for {
			message, err := backend.Receive()
			if err != nil {
				return
			}
			payload := strings.Contains(statement, "history_payload")
			switch message := message.(type) {
			case *pgproto3.Parse:
				statement = message.Query
				if strings.Contains(statement, "history_payload") && script.submitted != nil {
					script.submitted <- statement
				}
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				if payload && script.parameters != nil {
					parameters := make([][]byte, len(message.Parameters))
					for i, value := range message.Parameters {
						if value != nil {
							parameters[i] = append([]byte{}, value...)
						}
					}
					script.parameters <- parameters
				}
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				if payload {
					backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("payload"), DataTypeOID: 25}}})
				} else {
					backend.Send(&pgproto3.NoData{})
				}
			case *pgproto3.Execute:
				if strings.HasPrefix(statement, "BEGIN") {
					status = 'T'
					if statement == "BEGIN READ ONLY" {
						script.readOnly.Store(true)
					}
				}
				if payload {
					if script.executing != nil {
						close(script.executing)
					}
					if script.delay > 0 {
						time.Sleep(script.delay)
					}
					for _, row := range script.rows {
						backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(row)}})
					}
					if script.stallTail {
						_ = backend.Flush()
						_, _ = io.Copy(io.Discard, server)
						return
					}
					if script.terminalError {
						status = 'E'
						backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "22012", Message: "backend-secret"})
					} else {
						tag := "SELECT 1"
						if len(script.tags) > 0 {
							tag = script.tags[0]
						}
						backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
					}
				} else if statement == "ROLLBACK" && script.rollbackError {
					status = 'E'
					backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "cleanup-secret"})
				} else {
					if statement == "ROLLBACK" {
						status = 'I'
					}
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
				}
			case *pgproto3.Query:
				if script.submitted != nil {
					script.submitted <- message.String
				}
				if script.disconnect {
					return
				}
				if script.executing != nil {
					close(script.executing)
				}
				tags := script.tags
				if len(tags) == 0 {
					tags = []string{"SELECT 1"}
				}
				for _, tag := range tags {
					if len(script.rows) > 0 {
						backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("payload"), DataTypeOID: 25}}})
						for _, row := range script.rows {
							backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(row)}})
						}
					}
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
				}
				if script.terminalError {
					backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "22012", Message: "backend-secret"})
				}
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if script.closeError {
					if blocked := script.blocked.Load(); blocked != nil {
						blocked.block.Store(true)
					}
				}
			case *pgproto3.Sync:
				if statement == "ROLLBACK" && script.closeError {
					if blocked := script.blocked.Load(); blocked != nil {
						blocked.block.Store(true)
					}
				}
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
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
		_ = listener.Close()
		mu.Lock()
		if owned != nil {
			_ = owned.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("history protocol fixture not joined")
		}
	})
	profile := models.Connection{Name: "dev", Driver: "postgres", DSN: "postgres://u@" + listener.Addr().String() + "/d?sslmode=disable&passfile=/tmp/opencode/nonexistent-history-pass", Password: "fixture", CLIAccess: models.CLIAccessRowRead, CLIHistory: true, ReadOnly: false}
	service := historyService(profile, record)
	if script.closeError {
		service.Options.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			blocked := &serviceBlockedWrite{Conn: conn, release: make(chan struct{})}
			script.blocked.Store(blocked)
			return blocked, nil
		}
	}
	return service
}
