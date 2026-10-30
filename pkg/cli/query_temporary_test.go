//go:build linux || darwin

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func temporarySuccess() pg.TemporaryAtomicResult {
	value := "1"
	return pg.TemporaryAtomicResult{MachineExecuteResult: pg.MachineExecuteResult{
		Results:   []pg.MachineStatementResult{{CommandTag: "SELECT 1", Columns: []pg.MachineColumn{{Name: "one", TypeOID: 23, Encoding: "postgres-text"}}, Rows: [][]*string{{&value}}}},
		Execution: pg.MachineExecution{Status: pg.MachineExecutionCompleted, EffectsMayHaveOccurred: true},
	}, Outcome: pg.MachineTransactionCommitted}
}

func TestTemporaryQueryInputCompatibility(t *testing.T) {
	params := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(params, []byte(`["value",null,42]`), 0o600); err != nil {
		t.Fatal(err)
	}
	sqlFile := filepath.Join(t.TempDir(), "input.sql")
	if err := os.WriteFile(sqlFile, []byte("SELECT $1, $2, $3"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"-", sqlFile} {
		t.Run(source, func(t *testing.T) {
			s := forbiddenTemporaryService(t)
			f := &temporaryFake{t: t}
			f.atomic = func(ctx context.Context, script pg.ValidatedTemporaryScript, params [][]byte, o pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
				if script.SQL() != "SELECT $1, $2, $3" || script.ParameterCount() != 3 || !script.SupportsParameters() || !script.AllowsWrites() || script.HasWrites() || len(params) != 3 || string(params[0]) != "value" || params[1] != nil || string(params[2]) != "42" || o.MaxRows != 12 || o.Schema != "quoted schema" {
					t.Fatal("input or options changed")
				}
				r := temporarySuccess()
				return r, cb(ctx, r)
			}
			s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
				return f, nil
			}
			args := append(temporaryArgs(), "--file", source, "--params-file", params, "--max-rows", "12", "--schema", "quoted schema", "--allow-write")
			e, err := QueryCommand(s)(context.Background(), args, IO{In: io.NopCloser(strings.NewReader("SELECT $1, $2, $3"))})
			if err != nil || e.Target.ReadOnly || len(e.cachedJSON) == 0 || f.closed != 1 {
				t.Fatalf("query input: %v", err)
			}
		})
	}
	for _, tc := range []struct {
		sql   string
		extra []string
		code  ErrorCode
	}{
		{"DELETE FROM items", nil, PolicyDenied},
		{"SELECT 1; CREATE TABLE bad(id int)", []string{"--allow-write"}, UnsupportedSQL},
		{"SELECT $1; SELECT 2", []string{"--params-file", params}, UnsupportedSQL},
		{"SELECT 1", []string{"--params-file", "-"}, InvalidInput},
		{"SELECT 1", []string{"--schema", "\x00"}, InvalidInput},
	} {
		s := forbiddenTemporaryService(t)
		s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
			t.Fatal("invalid input reached opener")
			return nil, nil
		}
		args := append(temporaryArgs(), "--file", "-")
		args = append(args, tc.extra...)
		e, err := QueryCommand(s)(context.Background(), args, IO{In: io.NopCloser(strings.NewReader(tc.sql))})
		if err == nil || safeError(err).Code != tc.code || e.Target == nil || e.TransactionOutcome != "not_started" {
			t.Fatalf("input %q: %v want %s", tc.sql, err, tc.code)
		}
	}
}

func TestTemporaryQueryCachedPublication(t *testing.T) {
	streams, out, _ := testStreams()
	streams.In = io.NopCloser(strings.NewReader("SELECT 1"))
	s := forbiddenTemporaryService(t)
	f := &temporaryFake{t: t}
	f.atomic = func(ctx context.Context, _ pg.ValidatedTemporaryScript, _ [][]byte, _ pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
		r := temporarySuccess()
		r.Truncated = true
		r.TruncationReason = "max_rows"
		r.Results[0].Truncated = true
		r.Results[0].TruncationReason = "max_rows"
		if err := cb(ctx, r); err != nil {
			return r, err
		}
		if out.Len() != 0 {
			t.Fatal("precommit encoder published output")
		}
		return r, nil
	}
	s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
		return f, nil
	}
	var cached []byte
	command := func(ctx context.Context, args []string, streams IO) (Envelope, error) {
		e, err := QueryCommand(s)(ctx, args, streams)
		cached = slices.Clone(e.cachedJSON)
		// Re-encoding this would fail; dispatch must publish exactly the cached envelope.
		e.Data = make(chan int)
		return e, err
	}
	_, exit := Dispatch(context.Background(), append([]string{"query"}, append(temporaryArgs(), "--file", "-")...), streams, map[string]Command{"query": command})
	if exit != ExitSuccess || !bytes.Equal(out.Bytes(), cached) || !bytes.HasSuffix(cached, []byte{'\n'}) || f.closed != 1 {
		t.Fatalf("cached publication exit%d close%d", exit, f.closed)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(cached, &wire); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"transaction_outcome":"committed"`, `"status":"completed"`, `"truncated":true`, `"truncation_reason":"row_limit"`, `"result_complete":false`} {
		if !strings.Contains(out.String(), required) {
			t.Fatalf("missing evidence %s: %s", required, out.String())
		}
	}
	if _, ok := wire["connection"]; ok {
		t.Fatal("temporary payload included saved identity")
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(wire["data"], &data); err != nil || len(data) != 1 || data["results"] == nil {
		t.Fatal("data must contain ordered results only")
	}
}

func TestTemporaryQueryErrorEvidence(t *testing.T) {
	for _, mode := range []string{"not_started", "rolled_back", "disposed", "unknown", "close", "encoding", "size", "cancel", "precommit_cancel", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			streams, out, diagnostic := testStreams()
			streams.In = io.NopCloser(strings.NewReader("SELECT 1"))
			cancel := make(chan struct{})
			streams.Cancel = cancel
			s := forbiddenTemporaryService(t)
			f := &temporaryFake{t: t}
			wantExit, wantOutcome := ExitDatabase, mode
			f.atomic = func(ctx context.Context, _ pg.ValidatedTemporaryScript, _ [][]byte, _ pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
				r := temporarySuccess()
				switch mode {
				case "precommit_cancel":
					close(cancel)
					<-ctx.Done()
					cbErr := cb(ctx, r)
					if cbErr == nil || out.Len() != 0 {
						t.Fatal("cancelled precommit encoder succeeded or published")
					}
					r.Outcome = pg.MachineTransactionRolledBack
					return r, cbErr
				case "encoding", "size":
					value := "\xff"
					if mode == "size" {
						value = strings.Repeat("x", MaxOutputBytes)
					}
					r.Results[0].Rows[0][0] = &value
					cbErr := cb(ctx, r)
					if cbErr == nil || out.Len() != 0 {
						t.Fatal("invalid prospective envelope passed or was published")
					}
					r.Outcome = pg.MachineTransactionRolledBack
					return r, cbErr
				case "close", "cancel", "fallback":
					if err := cb(ctx, r); err != nil {
						return r, err
					}
					if mode == "cancel" {
						close(cancel)
					}
					return r, nil
				default:
					r.Outcome = pg.MachineTransactionOutcome(mode)
					r.Execution.Status = pg.MachineExecutionFailed
					return r, &pg.MachineError{Code: "DATABASE_FAILURE", SQLState: "23505"}
				}
			}
			switch mode {
			case "close":
				f.closeErr = errors.New("SECRET cleanup error")
				wantExit, wantOutcome = ExitResource, "committed"
			case "encoding", "size":
				wantExit, wantOutcome = ExitResource, "rolled_back"
			case "cancel":
				wantExit, wantOutcome = ExitTimeout, "committed"
			case "precommit_cancel":
				wantExit, wantOutcome = ExitTimeout, "rolled_back"
			case "fallback":
				wantExit, wantOutcome = ExitResource, "committed"
			}
			s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
				return f, nil
			}
			command := func(ctx context.Context, args []string, streams IO) (Envelope, error) {
				e, err := QueryCommand(s)(ctx, args, streams)
				if mode == "fallback" {
					e.cachedJSON = nil
					e.Data = make(chan int)
				}
				return e, err
			}
			_, exit := Dispatch(context.Background(), append([]string{"query"}, append(temporaryArgs(), "--file", "-")...), streams, map[string]Command{"query": command})
			text := out.String()
			if exit != wantExit || f.closed != 1 || !strings.Contains(text, `"ok":false`) || !strings.Contains(text, `"transaction_outcome":"`+wantOutcome+`"`) || !strings.Contains(text, `"execution":`) || !strings.Contains(text, `"target":`) || strings.Contains(text, `"data":`) || strings.Contains(text+diagnostic.String(), "SECRET") {
				t.Fatalf("mode%s exit%d want%d closed%d: %s", mode, exit, wantExit, f.closed, text)
			}
		})
	}
}

type temporaryFailedWriter struct {
	writes    int
	short     bool
	attempted []byte
}

func (w *temporaryFailedWriter) Write(p []byte) (int, error) {
	w.writes++
	w.attempted = slices.Clone(p)
	if w.short {
		return len(p) - 1, nil
	}
	return 0, errors.New("SECRET writer")
}
func (*temporaryFailedWriter) Close() error { return nil }

func TestTemporaryQueryNoPublicationReplay(t *testing.T) {
	for _, short := range []bool{false, true} {
		streams, _, diagnostic := testStreams()
		streams.In = io.NopCloser(strings.NewReader("SELECT 1"))
		writer := &temporaryFailedWriter{short: short}
		streams.Out = writer
		s := forbiddenTemporaryService(t)
		f := &temporaryFake{t: t}
		executions := 0
		f.atomic = func(ctx context.Context, _ pg.ValidatedTemporaryScript, _ [][]byte, _ pg.TemporaryAtomicOptions, cb pg.TemporaryPrecommitValidation) (pg.TemporaryAtomicResult, error) {
			executions++
			r := temporarySuccess()
			return r, cb(ctx, r)
		}
		s.openTemporary = func(context.Context, session.TemporaryTarget, pg.MachineOptions) (temporaryMachine, error) {
			return f, nil
		}
		_, exit := Dispatch(context.Background(), append([]string{"query"}, append(temporaryArgs(), "--file", "-")...), streams, map[string]Command{"query": QueryCommand(s)})
		if exit != ExitResource || writer.writes != 1 || executions != 1 || f.closed != 1 || !strings.Contains(string(writer.attempted), `"transaction_outcome":"committed"`) || strings.Contains(diagnostic.String(), "SECRET") {
			t.Fatalf("writer replay: exit%d writes%d executions%d", exit, writer.writes, executions)
		}
	}
}

func TestTemporaryTargetOutputAllowlist(t *testing.T) {
	target, err := session.NewTemporaryTarget(session.TemporaryTargetFields{Host: "::ffff:127.0.0.1", Port: "05433", Database: "dev", Username: "restricted", Passfile: "/SECRET-passfile"})
	if err != nil {
		t.Fatal(err)
	}
	metadata := publicTemporaryTarget(target, true)
	e := Envelope{OK: true, Command: "query", Target: metadata, TransactionOutcome: "committed"}
	data, err := encode(e)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Target map[string]any `json:"target"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Target) != 7 || wire.Target["host"] != "127.0.0.1" || wire.Target["port"] != float64(5433) || strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "passfile") {
		t.Fatalf("target allowlist: %s", data)
	}
	for _, mutate := range []func(*temporaryTargetMetadata){
		func(t *temporaryTargetMetadata) { t.Source = "saved" }, func(t *temporaryTargetMetadata) { t.Transport = "ssh" },
		func(t *temporaryTargetMetadata) { t.Host = "localhost" }, func(t *temporaryTargetMetadata) { t.Host = "192.0.2.1" }, func(t *temporaryTargetMetadata) { t.Host = "::ffff:127.0.0.1" },
		func(t *temporaryTargetMetadata) { t.SocketDir = "/tmp/opencode" }, func(t *temporaryTargetMetadata) { t.Port = 0 },
		func(t *temporaryTargetMetadata) { t.Database = "bad\n" }, func(t *temporaryTargetMetadata) { t.Username = "\xff" }, func(t *temporaryTargetMetadata) { t.Username = strings.Repeat("x", MaxInputBytes+1) },
		func(t *temporaryTargetMetadata) { t.Transport = "unix"; t.Host = ""; t.SocketDir = "relative" },
	} {
		m := *metadata
		mutate(&m)
		e.Target = &m
		if _, err := encode(e); err == nil {
			t.Fatalf("invalid output selector accepted: %+v", m)
		}
	}
	e.Target = metadata
	e.Connection = "saved"
	if _, err := encode(e); err == nil {
		t.Fatal("mixed identity accepted")
	}
	e.Connection = ""
	e.TransactionOutcome = "SECRET"
	if _, err := encode(e); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	e.Target = nil
	e.TransactionOutcome = "committed"
	if _, err := encode(e); err == nil {
		t.Fatal("saved envelope gained temporary outcome")
	}
}

func TestTemporaryQueryWireDispatch(t *testing.T) {
	cleanTemporaryEnvironment(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const sql = "INSERT INTO items(id) VALUES(1); SELECT id FROM items"
	var mu sync.Mutex
	var raw net.Conn
	var submitted []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		raw = conn
		mu.Unlock()
		defer func() { _ = conn.Close() }()
		b := pgproto3.NewBackend(conn, conn)
		if _, err := b.ReceiveStartupMessage(); err != nil {
			return
		}
		b.Send(&pgproto3.AuthenticationOk{})
		b.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		b.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if b.Flush() != nil {
			return
		}
		statement, status := "", byte('I')
		run := func(statement string, simple bool) {
			mu.Lock()
			submitted = append(submitted, statement)
			mu.Unlock()
			switch statement {
			case sql:
				if !simple {
					t.Error("original batch was not sent in one simple-query message")
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("INSERT 0 1")})
				b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("id"), DataTypeOID: 23, DataTypeSize: 4}}})
				b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			case "BEGIN":
				status = 'T'
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
			case "COMMIT":
				status = 'I'
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
			default:
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
			}
		}
		for {
			message, err := b.Receive()
			if err != nil {
				return
			}
			switch message := message.(type) {
			case *pgproto3.Query:
				run(message.String, true)
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Parse:
				statement = message.Query
				b.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				b.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				b.Send(&pgproto3.NoData{})
			case *pgproto3.Execute:
				run(statement, false)
			case *pgproto3.Sync:
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Terminate:
				return
			default:
				t.Errorf("unexpected frontend message %T", message)
				return
			}
			if b.Flush() != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		if raw != nil {
			_ = raw.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("wire fixture did not join")
		}
	})
	passfile := filepath.Join(t.TempDir(), "SECRET-passfile")
	if err := os.WriteFile(passfile, []byte("*:*:*:*:SECRET-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	args := temporaryArgs()
	args[2] = port
	args[len(args)-1] = passfile
	args = append([]string{"query"}, append(args, "--file", "-", "--allow-write")...)
	streams, out, _ := testStreams()
	streams.In = io.NopCloser(strings.NewReader(sql))
	_, exit := Dispatch(context.Background(), args, streams, map[string]Command{"query": QueryCommand(forbiddenTemporaryService(t))})
	if exit != ExitSuccess || !strings.Contains(out.String(), `"transaction_outcome":"committed"`) || !strings.Contains(out.String(), `"read_only":false`) || !strings.Contains(out.String(), `"command_tag":"INSERT 0 1"`) || strings.Contains(out.String(), "SECRET") {
		t.Fatalf("wire dispatch exit%d: %s", exit, out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	batches := 0
	for _, statement := range submitted {
		if statement == sql {
			batches++
		}
	}
	if slices.Index(submitted, "BEGIN") < 0 || slices.Index(submitted, "COMMIT") < 0 || slices.Index(submitted, "ROLLBACK") >= 0 || batches != 1 {
		t.Fatalf("wire sequence: %q", submitted)
	}
}
