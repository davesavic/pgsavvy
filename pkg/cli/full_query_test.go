//go:build linux || darwin

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func TestMachineFullQuery_ApprovalMatrix(t *testing.T) {
	for _, access := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
		for bits := range 8 {
			for _, sql := range []string{"SELECT 1", "UPDATE example SET x=1", "CREATE TABLE example(x int)", "UPDATE example SET x=1; CREATE TABLE other(x int)", "DO $$ BEGIN NULL; END $$"} {
				t.Run(fmt.Sprintf("%s/%d/%s", access, bits, sql), func(t *testing.T) {
					profile := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: access, ReadOnly: bits&1 != 0, ConfirmWrites: bits&2 != 0, ConfirmDDL: bits&4 != 0, PasswordCommand: "must-not-run"}
					approvals, parses, helpers, dials := 0, 0, 0, 0
					service := historyService(profile, nil)
					service.Approve = func(_ context.Context, request ApprovalRequest) error {
						approvals++
						require.Equal(t, sql, request.SQL)
						return &PublicError{Code: ApprovalCancelled}
					}
					service.Options = pg.MachineOptions{
						ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
							parses++
							return nil, errors.New("private parse failure")
						},
						Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { helpers++; return "private", nil }},
						Dial: func(context.Context, string, string) (net.Conn, error) {
							dials++
							return nil, errors.New("private dial failure")
						},
					}
					exit, output := runHistoryQuery(t, service, sql)
					writable := access == models.CLIAccessFull && !profile.ReadOnly
					wantApproval := writable && (profile.ConfirmWrites && strings.Contains(sql, "UPDATE") || profile.ConfirmDDL && strings.Contains(sql, "CREATE"))
					wantExit, wantParses := ExitInput, 0
					if writable || sql == "SELECT 1" {
						wantExit = ExitPolicy
						if access.AllowsCLIAccess(models.CLIAccessRowRead) {
							wantExit, wantParses = ExitUnavailable, 1
						}
					}
					if wantApproval {
						wantExit, wantParses = ExitPolicy, 0
					}
					require.Equal(t, wantExit, exit, output)
					require.Equal(t, wantParses, parses)
					require.Equal(t, 0, helpers+dials)
					if wantApproval {
						require.Equal(t, 1, approvals)
					} else {
						require.Zero(t, approvals)
					}
					require.NotContains(t, output, "private")
				})
			}
		}
	}
}

func fullWireService(t *testing.T, script *historyWireScript, record func(context.Context, string, query.HistoryEvent) error) InvocationService {
	t.Helper()
	if record == nil {
		record = func(context.Context, string, query.HistoryEvent) error { return nil }
	}
	service := historyWireService(t, script, record)
	lookup := service.historyLookup
	service.historyLookup = func(ctx context.Context, name string) (models.Connection, error) {
		profile, err := lookup(ctx, name)
		profile.CLIAccess = models.CLIAccessFull
		return profile, err
	}
	return service
}

func TestMachineFullQuery_OriginalRoutingAndResults(t *testing.T) {
	for _, fixture := range []string{"native", "explicit", "rows", "results", "tail", "transport", "cleanup", "prepared", "prepared_tail", "prepared_cleanup", "overflow"} {
		t.Run(fixture, func(t *testing.T) {
			script := &historyWireScript{submitted: make(chan string, 2), tags: []string{"UPDATE 1", "SELECT 2"}}
			var events []query.HistoryEvent
			var extra []string
			wantExit, wantStatus, wantOutcome := ExitSuccess, "completed", "succeeded"
			switch fixture {
			case "rows":
				script.rows, extra = []string{"kept", "omitted"}, []string{"--max-rows=1"}
			case "results":
				script.tags = make([]string, 257)
				for i := range script.tags {
					script.tags[i] = "UPDATE 1"
				}
			case "tail":
				script.rows, script.terminalError = []string{"private row"}, true
				wantExit, wantStatus, wantOutcome = ExitDatabase, "failed", "failed"
			case "transport":
				script.disconnect = true
				wantExit, wantStatus, wantOutcome = ExitDatabase, "unknown", "failed"
			case "cleanup":
				script.closeError, extra = true, []string{"--timeout=500ms"}
				wantExit, wantOutcome = ExitResource, "failed"
			case "prepared", "prepared_tail", "prepared_cleanup":
				script.tags = []string{"PREPARE TRANSACTION"}
				if fixture == "prepared_tail" {
					script.terminalError = true
					wantExit, wantStatus, wantOutcome = ExitDatabase, "failed", "failed"
				}
				if fixture == "prepared_cleanup" {
					script.closeError, extra = true, []string{"--timeout=500ms"}
					wantExit, wantOutcome = ExitResource, "failed"
				}
			case "explicit":
				extra = []string{"--approve", "--confirm-connection=dev"}
			case "overflow":
				script.rows = []string{strings.Repeat("x", MaxCellBytes+1)}
				wantExit, wantOutcome = ExitResource, "failed"
			}
			service := fullWireService(t, script, func(_ context.Context, _ string, event query.HistoryEvent) error {
				events = append(events, event)
				return nil
			})
			if fixture == "explicit" {
				lookup := service.historyLookup
				service.historyLookup = func(ctx context.Context, name string) (models.Connection, error) {
					profile, err := lookup(ctx, name)
					profile.ConfirmWrites = true
					return profile, err
				}
				service.Approve = func(context.Context, ApprovalRequest) error { t.Fatal("explicit approval opened terminal"); return nil }
			}
			const original = "/* exact original history_payload */ UPDATE example SET x=1; SELECT 2;"
			exit, output := runHistoryQuery(t, service, original, extra...)
			require.Equal(t, wantExit, exit, output)
			require.Equal(t, original, <-script.submitted)
			require.Empty(t, script.submitted, "SQL dispatched more than once")
			var doc map[string]any
			require.NoError(t, json.Unmarshal([]byte(output), &doc))
			metadata := doc["execution"].(map[string]any)
			require.Equal(t, wantStatus, metadata["status"])
			require.Equal(t, true, metadata["effects_may_have_occurred"])
			require.Len(t, events, 1)
			require.Equal(t, original, events[0].SQL)
			require.Equal(t, wantOutcome, events[0].Outcome)
			require.NotContains(t, output, "backend-secret")
			if strings.HasPrefix(fixture, "prepared") {
				require.Equal(t, true, doc["execution"].(map[string]any)["prepared_transactions_may_remain"])
			}
			if wantExit != ExitSuccess {
				require.Nil(t, doc["data"])
				require.NotContains(t, output, "private row")
				return
			}
			results := doc["data"].(map[string]any)["results"].([]any)
			switch fixture {
			case "rows":
				require.Equal(t, "row_limit", doc["truncation_reason"])
				require.Equal(t, "row_limit", results[0].(map[string]any)["truncation_reason"])
				require.Equal(t, "row_limit", results[1].(map[string]any)["truncation_reason"])
			case "results":
				require.Len(t, results, 256)
				require.Equal(t, "result_limit", doc["truncation_reason"])
			default:
				require.Equal(t, script.tags[0], results[0].(map[string]any)["command_tag"])
			}
		})
	}
}

func TestMachineFullQuery_CapturedSnapshot(t *testing.T) {
	for _, stdin := range []bool{false, true} {
		t.Run(fmt.Sprint(stdin), func(t *testing.T) {
			script := &historyWireScript{submitted: make(chan string, 2), parameters: make(chan [][]byte, 1), tags: []string{"UPDATE 1"}}
			var events []query.HistoryEvent
			service := fullWireService(t, script, func(_ context.Context, _ string, event query.HistoryEvent) error {
				events = append(events, event)
				return nil
			})
			profile, err := service.historyLookup(context.Background(), "dev")
			require.NoError(t, err)
			profile.ConfirmWrites, profile.Tags, profile.HiddenSchemas = true, []string{"captured"}, []string{"private"}
			service.historyLookup = func(context.Context, string) (models.Connection, error) { return profile, nil }
			service.Options.ParseConfig = func(ctx context.Context, got models.Connection) (*pgx.ConnConfig, error) {
				require.Equal(t, models.CLIAccessFull, got.CLIAccess)
				require.Equal(t, []string{"captured"}, got.Tags)
				require.Equal(t, []string{"private"}, got.HiddenSchemas)
				require.True(t, got.CLIHistory)
				return pgx.ParseConfig(got.DSN)
			}
			path, params := filepath.Join(t.TempDir(), "sql"), filepath.Join(t.TempDir(), "params")
			const original = "/* history_payload */ UPDATE example SET x=$1"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			require.NoError(t, os.WriteFile(params, []byte(`["approved parameter",null,""]`), 0o600))
			service.Approve = func(_ context.Context, request ApprovalRequest) error {
				require.Equal(t, ApprovalRequest{Connection: "dev", SQL: original, Schema: `quoted"schema`, MaxRows: 3, ParameterCount: 3, StatementCount: 1, WritesApprovalRequired: true}, request)
				require.NoError(t, os.WriteFile(path, []byte("DELETE FROM changed_target"), 0o600))
				require.NoError(t, os.WriteFile(params, []byte(`["changed parameter"]`), 0o600))
				profile.CLIAccess, profile.DSN, profile.CLIHistory = models.CLIAccessDisabled, "invalid replacement", false
				profile.Tags[0], profile.HiddenSchemas[0] = "changed", "changed"
				service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
					t.Fatal("options reread")
					return nil, nil
				}
				return nil
			}
			file := path
			if stdin {
				file = "-"
			}
			streams, output, _ := testStreams()
			streams.In = io.NopCloser(strings.NewReader(original))
			_, exit := Dispatch(context.Background(), []string{"query", "--connection=dev", "--file", file, "--params-file", params, "--schema", `quoted"schema`, "--max-rows=3"}, streams, map[string]Command{"query": QueryCommand(service)})
			require.Equal(t, ExitSuccess, exit, output.String())
			require.Equal(t, original, <-script.submitted)
			require.Empty(t, script.submitted)
			require.Equal(t, [][]byte{[]byte("approved parameter"), nil, {}}, <-script.parameters)
			require.Len(t, events, 1)
			require.Equal(t, original, events[0].SQL)
			require.NotContains(t, output.String(), "parameter")
		})
	}
}

func TestMachineFullQuery_PublicationPreservesOutcome(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint(overflow), func(t *testing.T) {
			var event query.HistoryEvent
			service := fullWireService(t, &historyWireScript{tags: []string{"PREPARE TRANSACTION"}}, func(_ context.Context, _ string, got query.HistoryEvent) error { event = got; return nil })
			command := QueryCommand(service)
			streams, output, _ := testStreams()
			streams.In = io.NopCloser(strings.NewReader("PREPARE TRANSACTION 'test-owned-wire-only'"))
			if !overflow {
				streams.Out = historyBrokenOutput{}
			}
			_, exit := Dispatch(context.Background(), []string{"query", "--connection=dev", "--file=-"}, streams, map[string]Command{"query": func(ctx context.Context, args []string, streams IO) (Envelope, error) {
				envelope, err := command(ctx, args, streams)
				if overflow && err == nil {
					envelope.Data = strings.Repeat("x", MaxOutputBytes+1)
				}
				return envelope, err
			}})
			require.Equal(t, ExitResource, exit)
			require.Equal(t, "succeeded", event.Outcome)
			if overflow {
				var doc map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &doc))
				assertFullExecution(t, doc, "completed", true)
				require.Nil(t, doc["data"])
			}
		})
	}
}

func TestMachineFullQuery_ReusableConcurrentCommand(t *testing.T) {
	const count = 12
	script := &historyWireScript{submitted: make(chan string, 2), tags: []string{"UPDATE 1"}}
	service := fullWireService(t, script, nil)
	profile, err := service.historyLookup(context.Background(), "dev")
	require.NoError(t, err)
	profile.ConfirmWrites, profile.CLIHistory = true, false
	service.historyLookup = func(context.Context, string) (models.Connection, error) { return profile, nil }
	var approvals, parses atomic.Int32
	const sql = "/* history_payload */ UPDATE example SET x=1"
	service.Approve = func(_ context.Context, request ApprovalRequest) error {
		approvals.Add(1)
		require.Equal(t, "dev", request.Connection)
		require.Equal(t, sql, request.SQL)
		return &PublicError{Code: ApprovalCancelled}
	}
	service.Options.ParseConfig = func(_ context.Context, got models.Connection) (*pgx.ConnConfig, error) {
		parses.Add(1)
		return pgx.ParseConfig(got.DSN)
	}
	command := QueryCommand(service)
	run := func(i int) {
		args := []string{"--connection=dev", "--file=-"}
		if i == 0 {
			args = append(args, "--approve", "--confirm-connection=dev")
		}
		envelope, err := command(context.Background(), args, IO{In: io.NopCloser(strings.NewReader(sql))})
		if i == 0 {
			require.NoError(t, err)
			require.Equal(t, "dev", envelope.Connection)
			require.Equal(t, sql, <-script.submitted)
		} else {
			require.Equal(t, ApprovalCancelled, safeError(err).Code)
			require.Nil(t, envelope.Data)
		}
	}
	// The explicit approval must not authorize later calls for the same profile.
	run(0)
	require.Zero(t, approvals.Load())
	run(1)
	var wg sync.WaitGroup
	for i := 2; i < count; i++ {
		wg.Go(func() { run(i) })
	}
	wg.Wait()
	require.EqualValues(t, count-1, approvals.Load())
	require.EqualValues(t, 1, parses.Load())
	require.Empty(t, script.submitted, "a later invocation dispatched SQL without approval")
}

func TestMachineFullQuery_ExplicitFlagsCannotElevate(t *testing.T) {
	for _, access := range []models.CLIAccess{models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
		for _, readOnly := range []bool{false, true} {
			if access == models.CLIAccessFull && !readOnly {
				continue
			}
			t.Run(fmt.Sprintf("%s/readonly=%t", access, readOnly), func(t *testing.T) {
				script := &historyWireScript{submitted: make(chan string, 1)}
				service := fullWireService(t, script, nil)
				profile, err := service.historyLookup(context.Background(), "dev")
				require.NoError(t, err)
				profile.CLIAccess, profile.ReadOnly, profile.ConfirmWrites = access, readOnly, true
				profile.PasswordCommand = "must-not-run"
				service.historyLookup = func(context.Context, string) (models.Connection, error) { return profile, nil }
				approvals, parses, helpers, dials := 0, 0, 0, 0
				service.Approve = func(context.Context, ApprovalRequest) error { approvals++; return nil }
				service.Options = pg.MachineOptions{
					ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
						parses++
						return nil, errors.New("forbidden parse")
					},
					Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { helpers++; return "", errors.New("forbidden helper") }},
					Dial: func(context.Context, string, string) (net.Conn, error) {
						dials++
						return nil, errors.New("forbidden dial")
					},
				}
				exit, output := runHistoryQuery(t, service, "UPDATE example SET x=1", "--approve", "--confirm-connection=dev")
				require.Equal(t, ExitInput, exit, output)
				require.Contains(t, output, string(UnsupportedSQL))
				require.Zero(t, approvals+parses+helpers+dials)
				require.Empty(t, script.submitted)
			})
		}
	}
}

func TestMachineFullQuery_ApprovalFlagsQueryOnly(t *testing.T) {
	for _, route := range [][]string{
		{"connections", "list"},
		{"connections", "show", "dev"},
		{"connections", "test", "dev"},
		{"schema", "list", "--connection=dev"},
		{"schema", "tables", "--connection=dev", "--schema=public"},
		{"schema", "describe", "--connection=dev", "--schema=public", "--table=example"},
		{"schema", "relationships", "--connection=dev", "--schema=public", "--table=example"},
		{"capabilities"},
	} {
		for _, flags := range [][]string{{"--approve"}, {"--confirm-connection=dev"}, {"--approve", "--confirm-connection=dev"}} {
			t.Run(strings.Join(append(append([]string{}, route...), flags...), " "), func(t *testing.T) {
				forbidden := 0
				service := InvocationService{
					Load: func() ([]models.Connection, error) { forbidden++; return nil, errors.New("offline profile load") },
					Approve: func(context.Context, ApprovalRequest) error {
						forbidden++
						return &PublicError{Code: ApprovalCancelled}
					},
				}
				registry := map[string]Command{"connections": ConnectionsCommand(service), "schema": SchemaCommand(service), "query": QueryCommand(service), "capabilities": CapabilitiesCommand()}
				streams, output, _ := testStreams()
				_, exit := Dispatch(context.Background(), append(append([]string{}, route...), flags...), streams, registry)
				require.Equal(t, ExitInput, exit, output.String())
				require.Contains(t, output.String(), string(InvalidInput))
				require.Zero(t, forbidden, "nonquery flags loaded profiles or acquired approval terminal")
			})
		}
	}
}

func TestMachineFullQuery_ApprovalHistory(t *testing.T) {
	for _, code := range []ErrorCode{ApprovalRequired, ApprovalMismatch, ApprovalUnavailable, ApprovalCancelled, Timeout, ResourceFailure} {
		t.Run(string(code), func(t *testing.T) {
			var events []query.HistoryEvent
			service := historyService(models.Connection{Name: "dev", CLIAccess: models.CLIAccessFull, CLIHistory: true, ConfirmWrites: true}, func(_ context.Context, _ string, event query.HistoryEvent) error {
				events = append(events, event)
				return errors.New("private history path")
			})
			service.Approve = func(context.Context, ApprovalRequest) error {
				time.Sleep(12 * time.Millisecond)
				return &PublicError{Code: code}
			}
			if code == ApprovalRequired {
				service.Approve = nil
			}
			service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
				t.Fatal("approval failure parsed credentials")
				return nil, nil
			}
			exit, output := runHistoryQuery(t, service, "UPDATE example SET x=1")
			wantOutcome, wantExit := "blocked", ExitPolicy
			if code == Timeout {
				wantOutcome, wantExit = "failed", ExitTimeout
			}
			require.Equal(t, wantExit, exit, output)
			require.Len(t, events, 1)
			require.Equal(t, wantOutcome, events[0].Outcome)
			if code != ApprovalRequired {
				require.GreaterOrEqual(t, events[0].DurationMS, int64(10))
			}
			require.Contains(t, output, string(HistoryUnavailable))
			require.NotContains(t, output, "private history")
			require.Contains(t, output, `"status":"not_started"`)
		})
	}
}

func TestMachineFullQuery_ApprovalFlags(t *testing.T) {
	for _, extra := range [][]string{
		{"--approve"},
		{"--confirm-connection=dev"},
		{"--approve=false", "--confirm-connection=dev"},
		{"--approve", "--approve", "--confirm-connection=dev"},
		{"--approve", "--confirm-connection=dev", "--confirm-connection=dev"},
		{"--approve", "--confirm-connection="},
		{"--help", "--approve", "--confirm-connection=dev"},
		{"--", "--approve", "--confirm-connection=dev"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			service := InvocationService{Load: func() ([]models.Connection, error) { t.Fatal("malformed flags loaded profiles"); return nil, nil }, Approve: func(context.Context, ApprovalRequest) error { t.Fatal("malformed flags prompted"); return nil }}
			exit, _ := runHistoryQuery(t, service, "UPDATE example SET x=1", extra...)
			require.Equal(t, ExitInput, exit)
		})
	}
	for _, name := range []string{"dev", "Dev", " dev ", "数据库"} {
		for _, exact := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", name, exact), func(t *testing.T) {
				profile := models.Connection{Name: name, Driver: "postgres", CLIAccess: models.CLIAccessFull, ConfirmWrites: true}
				service := historyService(profile, nil)
				service.Approve = func(context.Context, ApprovalRequest) error { t.Fatal("explicit pair fell back to prompt"); return nil }
				parses := 0
				service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
					parses++
					return nil, errors.New("fixture")
				}
				confirmation := name
				if !exact {
					confirmation += " "
				}
				args := []string{"--connection", name, "--file=-", "--approve", "--confirm-connection", confirmation}
				envelope, err := QueryCommand(service)(context.Background(), args, IO{In: io.NopCloser(strings.NewReader("UPDATE example SET x=1"))})
				if exact {
					require.Equal(t, ConnectionUnavailable, safeError(err).Code)
					require.Equal(t, 1, parses)
				} else {
					require.Equal(t, ApprovalMismatch, safeError(err).Code)
					require.Zero(t, parses)
				}
				require.Nil(t, envelope.Data)
			})
		}
	}
}

func TestMachineFullQuery_Preflight(t *testing.T) {
	for _, sql := range []string{"-- only a comment", "COPY example FROM STDIN", "SELECT 1; COPY example TO STDOUT", "SELECT $1; SELECT 2", "SELECT 'unfinished"} {
		t.Run(sql, func(t *testing.T) {
			service := historyService(models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessFull, ConfirmWrites: true, ConfirmDDL: true}, nil)
			service.Approve = func(context.Context, ApprovalRequest) error { t.Fatal("preflight prompted"); return nil }
			service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
				t.Fatal("preflight opened credentials")
				return nil, nil
			}
			params := filepath.Join(t.TempDir(), "params.json")
			require.NoError(t, os.WriteFile(params, []byte(`["private parameter"]`), 0o600))
			exit, output := runHistoryQuery(t, service, sql, "--params-file", params)
			require.Equal(t, ExitInput, exit, output)
			require.NotContains(t, output, "private parameter")
		})
	}
}

func TestMachineFullQuery_PreflightParameters(t *testing.T) {
	for _, data := range []string{`["\u0000"]`, `{}`, `[[1]]`, `[`, `null`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "params")
			require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
			service := historyService(models.Connection{Name: "dev", CLIAccess: models.CLIAccessFull, ConfirmWrites: true}, nil)
			service.Approve = func(context.Context, ApprovalRequest) error { t.Fatal("invalid parameter prompted"); return nil }
			service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
				t.Fatal("invalid parameter opened credentials")
				return nil, nil
			}
			exit, output := runHistoryQuery(t, service, "UPDATE example SET x=$1", "--params-file", path)
			require.Equal(t, ExitInput, exit, output)
		})
	}
}

func TestMachineFullQuery_ReadOnlyOverride(t *testing.T) {
	for _, sql := range []string{"/* history_payload */ SELECT 1", "UPDATE example SET x=1"} {
		t.Run(sql, func(t *testing.T) {
			script := &historyWireScript{rows: []string{"guarded"}, submitted: make(chan string, 1)}
			service := fullWireService(t, script, nil)
			lookup := service.historyLookup
			service.historyLookup = func(ctx context.Context, name string) (models.Connection, error) {
				profile, err := lookup(ctx, name)
				profile.ReadOnly, profile.ConfirmWrites, profile.ConfirmDDL = true, true, true
				return profile, err
			}
			service.Approve = func(context.Context, ApprovalRequest) error { t.Fatal("guarded query requested approval"); return nil }
			exit, output := runHistoryQuery(t, service, sql, "--approve", "--confirm-connection=dev")
			if strings.Contains(sql, "UPDATE") {
				require.Equal(t, ExitInput, exit)
				require.Empty(t, script.submitted)
			} else {
				require.Equal(t, ExitSuccess, exit, output)
				require.True(t, script.readOnly.Load())
				require.Contains(t, output, `"columns"`)
				require.NotContains(t, output, `"results"`)
			}
			require.NotContains(t, output, `"execution"`)
		})
	}
}

func TestMachineFullQuery_CancelApproval(t *testing.T) {
	for _, signal := range []bool{false, true} {
		t.Run(fmt.Sprint(signal), func(t *testing.T) {
			cancelled := make(chan struct{})
			var event query.HistoryEvent
			service := historyService(models.Connection{Name: "dev", CLIHistory: true, CLIAccess: models.CLIAccessFull, ConfirmWrites: true}, func(_ context.Context, _ string, got query.HistoryEvent) error { event = got; return nil })
			service.Approve = func(ctx context.Context, _ ApprovalRequest) error {
				if signal {
					close(cancelled)
				}
				<-ctx.Done()
				return &PublicError{Code: ApprovalCancelled}
			}
			service.Options.ParseConfig = func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
				t.Fatal("canceled approval opened credentials")
				return nil, nil
			}
			streams, output, _ := testStreams()
			streams.In = io.NopCloser(strings.NewReader("UPDATE example SET x=1"))
			if signal {
				streams.Cancel = cancelled
			}
			_, exit := Dispatch(context.Background(), []string{"query", "--connection=dev", "--file=-", "--timeout=150ms"}, streams, map[string]Command{"query": QueryCommand(service)})
			require.Equal(t, ExitTimeout, exit, output.String())
			require.Equal(t, "failed", event.Outcome)
			require.Contains(t, output.String(), `"status":"not_started"`)
		})
	}
}

func TestMachineFullQuery_OfflineDiscovery(t *testing.T) {
	service := InvocationService{Load: func() ([]models.Connection, error) { t.Fatal("offline discovery loaded profiles"); return nil, nil }, Approve: func(context.Context, ApprovalRequest) error { t.Fatal("offline discovery opened terminal"); return nil }}
	for _, command := range []struct {
		command Command
		args    []string
	}{{QueryCommand(service), []string{"--help"}}, {CapabilitiesCommand(), nil}} {
		envelope, err := command.command(context.Background(), command.args, IO{})
		require.NoError(t, err)
		encoded, err := json.Marshal(envelope.Data)
		require.NoError(t, err)
		for _, text := range []string{"full", "read_only=true", "--approve", "--confirm-connection", "heuristic", "COPY FROM STDIN/TO STDOUT", "parameters only for one statement", "command_tag"} {
			require.Contains(t, string(encoded), text)
		}
	}
}

// This test only connects when the caller supplies the designated disposable
// fixture. Routine unit runs do not establish native PostgreSQL evidence.
func TestMachineFullQuery_DesignatedPostgres(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("designated disposable PostgreSQL fixture not supplied")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("designated fixture connection failed (%T; details withheld)", err)
	}
	defer func() { _ = observer.Close(context.Background()) }()
	schema := fmt.Sprintf("pgsavvy_full_query_%d_%d", os.Getpid(), time.Now().UnixNano())
	id := pgx.Identifier{schema}.Sanitize()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := observer.Exec(cleanup, "DROP SCHEMA IF EXISTS "+id+" CASCADE"); err != nil {
			t.Errorf("test-owned schema cleanup failed (%T; details withheld)", err)
		}
	}()
	for _, sql := range []string{"CREATE SCHEMA " + id, "CREATE TABLE " + id + ".approved(value text, hits int DEFAULT 0)", "CREATE TABLE " + id + ".changed(value text)", "INSERT INTO " + id + ".approved(value) VALUES ('initial')", "INSERT INTO " + id + ".changed VALUES ('untouched')"} {
		if _, err := observer.Exec(ctx, sql); err != nil {
			t.Fatalf("designated fixture setup failed (%T; details withheld)", err)
		}
	}
	for index, stdin := range []bool{false, true} {
		t.Run(fmt.Sprint(stdin), func(t *testing.T) {
			profile := models.Connection{Name: "designated", Driver: "postgres", DSN: dsn, CLIAccess: models.CLIAccessFull, ConfirmWrites: true}
			path, params := filepath.Join(t.TempDir(), "sql"), filepath.Join(t.TempDir(), "params")
			profilesPath := filepath.Join(t.TempDir(), "connections.yml")
			require.NoError(t, config.SaveConnections(afero.NewOsFs(), profilesPath, []models.Connection{profile}))
			original := "UPDATE approved SET value=$1, hits=hits+1 RETURNING value"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			require.NoError(t, os.WriteFile(params, []byte(`["approved native value"]`), 0o600))
			service := InvocationService{Load: func() ([]models.Connection, error) { t.Fatal("captured profile reloaded"); return nil, nil }, historyLookup: func(ctx context.Context, name string) (models.Connection, error) {
				return historyProfileMetadata(ctx, profilesPath, name)
			}, Approve: func(_ context.Context, request ApprovalRequest) error {
				require.Equal(t, original, request.SQL)
				require.Equal(t, schema, request.Schema)
				require.NoError(t, os.WriteFile(path, []byte("UPDATE changed SET value='wrong target'"), 0o600))
				require.NoError(t, os.WriteFile(params, []byte(`["wrong parameter"]`), 0o600))
				profile.DSN, profile.CLIAccess = "invalid changed target", models.CLIAccessDisabled
				require.NoError(t, config.SaveConnections(afero.NewOsFs(), profilesPath, []models.Connection{profile}))
				return nil
			}}
			file := path
			if stdin {
				file = "-"
			}
			streams, output, _ := testStreams()
			streams.In = io.NopCloser(strings.NewReader(original))
			_, exit := Dispatch(ctx, []string{"query", "--connection=designated", "--file", file, "--params-file", params, "--schema", schema}, streams, map[string]Command{"query": QueryCommand(service)})
			require.Equal(t, ExitSuccess, exit, output.String())
			require.Contains(t, output.String(), `"command_tag":"UPDATE 1"`)
			require.Contains(t, output.String(), "approved native value")
			var approved, changed string
			var hits int
			if err := observer.QueryRow(ctx, "SELECT a.value,c.value,a.hits FROM "+id+".approved a CROSS JOIN "+id+".changed c").Scan(&approved, &changed, &hits); err != nil {
				t.Fatalf("fixture observation failed (%T; details withheld)", err)
			}
			require.Equal(t, "approved native value", approved)
			require.Equal(t, "untouched", changed)
			require.Equal(t, index+1, hits, "native SQL was replayed")
		})
	}
	t.Run("native_batch", func(t *testing.T) {
		profile := models.Connection{Name: "designated", Driver: "postgres", DSN: dsn, CLIAccess: models.CLIAccessFull, ConfirmWrites: true}
		service := InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }, Approve: func(context.Context, ApprovalRequest) error { t.Fatal("explicit flags prompted"); return nil }}
		streams, output, _ := testStreams()
		streams.In = io.NopCloser(strings.NewReader("UPDATE approved SET hits=hits+1; SELECT hits FROM approved"))
		_, exit := Dispatch(ctx, []string{"query", "--connection=designated", "--file=-", "--schema", schema, "--approve", "--confirm-connection=designated"}, streams, map[string]Command{"query": QueryCommand(service)})
		require.Equal(t, ExitSuccess, exit, output.String())
		var doc map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &doc))
		results := doc["data"].(map[string]any)["results"].([]any)
		require.Len(t, results, 2)
		require.Equal(t, "UPDATE 1", results[0].(map[string]any)["command_tag"])
		require.Equal(t, []any{[]any{"3"}}, results[1].(map[string]any)["rows"])
	})
}
