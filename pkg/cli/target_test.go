//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type temporaryFake struct {
	t        *testing.T
	atomic   func(context.Context, pg.ValidatedTemporaryScript, [][]byte, pg.TemporaryAtomicOptions, pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error)
	inspect  func(pg.MachineMetadataRequest) (pg.MachineMetadata, error)
	ping     func() error
	closeErr error
	closed   int
}

func (f *temporaryFake) ExecuteTemporaryAtomic(ctx context.Context, s pg.ValidatedTemporaryScript, p [][]byte, o pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
	f.t.Helper()
	if f.atomic == nil {
		f.t.Fatal("unexpected atomic execution")
	}
	return f.atomic(ctx, s, p, o, cb)
}

func (f *temporaryFake) Inspect(_ context.Context, r pg.MachineMetadataRequest) (pg.MachineMetadata, error) {
	f.t.Helper()
	if f.inspect == nil {
		f.t.Fatal("unexpected metadata execution")
	}
	return f.inspect(r)
}

func (f *temporaryFake) Ping(context.Context) error {
	f.t.Helper()
	if f.ping == nil {
		f.t.Fatal("unexpected ping")
	}
	return f.ping()
}
func (f *temporaryFake) Close(context.Context) error { f.closed++; return f.closeErr }

func forbiddenTemporaryService(t *testing.T) InvocationService {
	t.Helper()
	forbidden := func() { t.Helper(); t.Fatal("temporary route accessed saved state, consent, approval or credentials") }
	return InvocationService{
		Load:          func() ([]models.Connection, error) { forbidden(); return nil, nil },
		Approve:       func(context.Context, ApprovalRequest) error { forbidden(); return nil },
		historyLookup: func(context.Context, string) (models.Connection, error) { forbidden(); return models.Connection{}, nil },
		historyAppend: func(context.Context, string, query.HistoryEvent) error { forbidden(); return nil },
		Options: pg.MachineOptions{
			ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { forbidden(); return nil, nil },
			Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { forbidden(); return "", nil }},
		},
	}
}

func temporaryArgs() []string {
	return []string{"--temporary", "--port", "5433", "--database", "dev", "--username", "restricted", "--passfile", "/SECRET-passfile"}
}

func temporaryCommands(s InvocationService) []struct {
	name    string
	command Command
	prefix  []string
} {
	return []struct {
		name    string
		command Command
		prefix  []string
	}{
		{"query", QueryCommand(s), []string{"--file", "-"}},
		{"schema list", SchemaCommand(s), []string{"list"}},
		{"schema tables", SchemaCommand(s), []string{"tables", "--schema", "public"}},
		{"schema describe", SchemaCommand(s), []string{"describe", "--schema", "public", "--table", "items"}},
		{"schema relationships", SchemaCommand(s), []string{"relationships", "--schema", "public", "--table", "items"}},
		{"connections test", ConnectionsCommand(s), []string{"test"}},
	}
}

func TestTemporaryTargetFlagMatrix(t *testing.T) {
	s := forbiddenTemporaryService(t)
	s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
		t.Fatal("invalid flags opened target")
		return nil, nil
	}
	for _, route := range temporaryCommands(s) {
		t.Run(route.name, func(t *testing.T) {
			var cases [][]string
			base := temporaryArgs()
			for _, key := range append(append([]string{}, temporaryTargetFlags...), "temporary") {
				value := "x"
				if key == "temporary" {
					value = ""
				}
				duplicate := []string{"--" + key}
				if value != "" {
					duplicate = append(duplicate, value)
				}
				// Optional fields must first be present to exercise duplicate rejection.
				b := slices.Clone(base)
				if key == "host" {
					b = append(b, "--host", "127.0.0.1")
				}
				if key == "socket-dir" {
					b = append(b, "--socket-dir", "/tmp/opencode")
				}
				cases = append(cases, append(b, duplicate...))
				cases = append(cases, append(slices.Clone(base), "--"+key+"="))
			}
			for _, key := range []string{"port", "database", "username", "passfile"} {
				i := slices.Index(base, "--"+key)
				cases = append(cases, append(slices.Clone(base[:i]), base[i+2:]...))
			}
			for _, extra := range [][]string{
				{"saved"},
				{"--connection", "saved"},
				{"--approve"},
				{"--confirm-connection", "saved"},
				{"--approve", "--confirm-connection", "saved"},
				{"--unknown"},
				{"--password", "SECRET"},
				{"--dsn", "SECRET"},
				{"--host", "127.0.0.1", "--socket-dir", "/tmp/opencode"},
				{"--temporary=true"},
				{"--host", "localhost"},
				{"--host", "192.0.2.1"},
				{"--host", "::1%zone"},
				{"--host", "127.0.0.1,::1"},
				{"--socket-dir", "relative"},
				{"--socket-dir", "/tmp/../tmp"},
				{"--allow-write", "--allow-write"},
			} {
				cases = append(cases, append(slices.Clone(base), extra...))
			}
			for _, value := range []string{"0", "65536", "-1", "+1", "1.5", "1x"} {
				b := slices.Clone(base)
				b[2] = value
				cases = append(cases, b)
			}
			for _, key := range []string{"database", "username"} {
				for _, value := range []string{"bad\nvalue", "bad\x00value", "\xff", strings.Repeat("x", MaxInputBytes+1), strings.Repeat("<", MaxOutputBytes/6)} {
					b := slices.Clone(base)
					b[slices.Index(b, "--"+key)+1] = value
					cases = append(cases, b)
				}
			}
			if route.name != "query" {
				cases = append(cases, append(slices.Clone(base), "--allow-write"))
			}
			// Every temporary field and write opt-in is forbidden without --temporary.
			for _, key := range temporaryTargetFlags {
				cases = append(cases, []string{"--" + key, "x"})
			}
			cases = append(cases, []string{"--allow-write"}, base[1:])
			for i, args := range cases {
				args = append(slices.Clone(route.prefix), args...)
				_, err := route.command(context.Background(), args, IO{In: io.NopCloser(strings.NewReader("SELECT 1"))})
				if err == nil || safeError(err).Code != InvalidInput {
					t.Fatalf("case %d: code=%v", i, err)
				}
			}
		})
	}
}

func TestTemporaryRoutingWithoutSavedAccess(t *testing.T) {
	for _, transport := range []string{"default", "ipv6", "socket"} {
		for _, routeName := range []string{"query", "schema list", "schema tables", "schema describe", "schema relationships", "connections test"} {
			t.Run(transport+"/"+routeName, func(t *testing.T) {
				s := forbiddenTemporaryService(t)
				calls, opened := 0, 0
				f := &temporaryFake{t: t}
				f.atomic = func(ctx context.Context, script pg.ValidatedTemporaryScript, _ [][]byte, _ pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
					calls++
					if script.SQL() != "SELECT 1" || script.AllowsWrites() {
						t.Fatal("query validation changed")
					}
					r := temporarySuccess()
					return r, cb(ctx, r)
				}
				f.inspect = func(r pg.MachineMetadataRequest) (pg.MachineMetadata, error) {
					calls++
					if !strings.HasSuffix(routeName, map[pg.MachineMetadataOperation]string{pg.MachineSchemas: "list", pg.MachineTables: "tables", pg.MachineDescribe: "describe", pg.MachineRelationships: "relationships"}[r.Operation]) {
						t.Fatal("wrong metadata route")
					}
					return pg.MachineMetadata{Schemas: &pg.MachineSchemaPage{Items: []pg.MachineSchema{{Name: "public"}}, RawComplete: true}, Tables: &pg.MachineTablePage{RawComplete: true}, Description: &pg.MachineDescription{}, Relationships: &pg.MachineRelationshipSet{}}, nil
				}
				f.ping = func() error { calls++; return nil }
				s.openTemporary = func(_ context.Context, target session.TemporaryTarget, o pg.MachineOptions) (temporaryMachine, error) {
					opened++
					if target.Database() != "dev" || target.Username() != "restricted" || target.Port() != 5433 || o.CleanupTimeout <= 0 {
						t.Fatal("target changed")
					}
					if transport == "default" && target.Host() != "127.0.0.1" || transport == "ipv6" && target.Host() != "::1" || transport == "socket" && target.SocketDir() != "/tmp/opencode" {
						t.Fatal("transport changed")
					}
					return f, nil
				}
				for _, route := range temporaryCommands(s) {
					if route.name != routeName {
						continue
					}
					args := append(slices.Clone(route.prefix), temporaryArgs()...)
					if transport == "ipv6" {
						args = append(args, "--host", "0:0:0:0:0:0:0:1")
					}
					if transport == "socket" {
						args = append(args, "--socket-dir", "/tmp/opencode")
					}
					e, err := route.command(context.Background(), args, IO{In: io.NopCloser(strings.NewReader("SELECT 1"))})
					if err != nil || e.Target == nil || e.Connection != "" || !e.Target.ReadOnly || calls != 1 || opened != 1 || f.closed != 1 {
						t.Fatalf("route failed: %v calls%d opened%d closed%d", err, calls, opened, f.closed)
					}
				}
			})
		}
	}
}

func cleanTemporaryEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE", "PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT", "PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION", "PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS", "PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION"} {
		t.Setenv(key, "")
	}
}

func TestTemporaryRealOpenerBypassesSavedSources(t *testing.T) {
	cleanTemporaryEnvironment(t)
	path := filepath.Join(t.TempDir(), "SECRET-passfile")
	if err := os.WriteFile(path, []byte("127.0.0.1:5433:dev:restricted:SECRET-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := forbiddenTemporaryService(t)
	dials := 0
	s.Options.Dial = func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("SECRET raw dial error")
	}
	args := temporaryArgs()
	args[len(args)-1] = path
	e, err := ConnectionsCommand(s)(context.Background(), append([]string{"test"}, args...), IO{})
	if err == nil || safeError(err).Code != ConnectionUnavailable || dials != 1 || e.Target == nil {
		t.Fatalf("opener: err=%v dials=%d", err, dials)
	}
	e.Error = safeError(err)
	data, err := encode(e)
	if err != nil || strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "passfile") || strings.Contains(string(data), "connection\"") {
		t.Fatalf("private opener error escaped: %s %v", data, err)
	}
}

func TestTemporaryMetadataErrorsKeepTarget(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		s := forbiddenTemporaryService(t)
		f := &temporaryFake{t: t}
		f.inspect = func(pg.MachineMetadataRequest) (pg.MachineMetadata, error) {
			return pg.MachineMetadata{}, &pg.MachineError{Code: "DATABASE_FAILURE"}
		}
		f.ping = func() error { return &pg.MachineError{Code: "DATABASE_FAILURE"} }
		if cleanupFailure {
			f.closeErr = errors.New("SECRET cleanup")
			f.ping = func() error { return nil }
			f.inspect = func(pg.MachineMetadataRequest) (pg.MachineMetadata, error) { return pg.MachineMetadata{}, nil }
		}
		s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
			return f, nil
		}
		for _, route := range temporaryCommands(s) {
			if route.name == "query" {
				continue
			}
			e, err := route.command(context.Background(), append(slices.Clone(route.prefix), temporaryArgs()...), IO{})
			if err == nil || e.Target == nil || e.Connection != "" || e.TransactionOutcome != "" {
				t.Fatalf("metadata error lost target: %s %v", route.name, err)
			}
			e.Error = safeError(err)
			data, encodeErr := encode(e)
			if encodeErr != nil || !strings.Contains(string(data), `"target":`) || strings.Contains(string(data), "SECRET") {
				t.Fatalf("metadata error encoding: %s %v", data, encodeErr)
			}
		}
		if f.closed != 5 {
			t.Fatalf("metadata error did not clean up all clients: %d", f.closed)
		}
	}
	s := forbiddenTemporaryService(t)
	for _, op := range []string{"list", "show"} {
		if _, err := ConnectionsCommand(s)(context.Background(), append([]string{op}, temporaryArgs()...), IO{}); err == nil {
			t.Fatal("unsupported temporary connection operation accepted")
		}
	}
}
