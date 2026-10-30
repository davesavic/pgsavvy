//go:build linux || darwin

package pg

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/davesavic/pgsavvy/pkg/session"
)

type atomicWireMessage struct {
	sql    string
	simple bool
}

// Unlike the native fixture this models BEGIN/COMMIT/ROLLBACK tags and RFQ
// transaction state, including extended user SQL and rejected terminal commands.
func atomicWire(t *testing.T, respond func(string, *pgproto3.Backend, net.Conn) (byte, bool)) (*MachineClient, func() []atomicWireMessage) {
	t.Helper()
	temporaryMachineEnvironment(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var raw net.Conn
	var messages []atomicWireMessage
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
		query, status := "", byte('I')
		run := func(sql string, simple bool) {
			mu.Lock()
			messages = append(messages, atomicWireMessage{sql, simple})
			mu.Unlock()
			if next, handled := respond(sql, b, conn); handled {
				status = next
				return
			}
			tag := "SET"
			switch sql {
			case "BEGIN", "BEGIN READ ONLY":
				tag, status = "BEGIN", 'T'
			case "COMMIT":
				tag, status = "COMMIT", 'I'
			case "ROLLBACK":
				tag, status = "ROLLBACK", 'I'
			}
			b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
		}
		for {
			msg, err := b.Receive()
			if err != nil {
				return
			}
			switch msg := msg.(type) {
			case *pgproto3.Query:
				run(msg.String, true)
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Parse:
				query = msg.Query
				b.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				b.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				b.Send(&pgproto3.NoData{})
			case *pgproto3.Execute:
				run(query, false)
			case *pgproto3.Sync:
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			case *pgproto3.Terminate:
				return
			default:
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
			t.Error("atomic wire fixture not joined")
		}
	})
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	passfile := filepath.Join(t.TempDir(), "passfile")
	if err := os.WriteFile(passfile, []byte("*:*:*:*:synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := session.NewTemporaryTarget(session.TemporaryTargetFields{Port: port, Database: "d", Username: "u", Passfile: passfile})
	if err != nil {
		t.Fatal(err)
	}
	client, err := OpenTemporaryMachine(context.Background(), target, MachineOptions{CleanupTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client, func() []atomicWireMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]atomicWireMessage(nil), messages...)
	}
}

func atomicScript(t *testing.T, sql string, parameters int, write bool) ValidatedTemporaryScript {
	t.Helper()
	script, err := ValidateTemporaryScript(context.Background(), sql, parameters, write)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func atomicEncoder(_ context.Context, result TemporaryAtomicResult) error {
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 1<<20 {
		return &MachineError{Code: "RESOURCE_FAILURE"}
	}
	return nil
}

func atomicCount(messages []atomicWireMessage, sql string) int {
	n := 0
	for _, message := range messages {
		if message.sql == sql {
			n++
		}
	}
	return n
}

func TestTemporaryAtomicPreflight(t *testing.T) {
	client, trace := atomicWire(t, func(string, *pgproto3.Backend, net.Conn) (byte, bool) { return 0, false })
	valid := atomicScript(t, "SELECT 1", 0, false)
	denied := valid
	denied.writes = true
	for _, tc := range []struct {
		name   string
		script ValidatedTemporaryScript
		params [][]byte
		opts   TemporaryAtomicOptions
		cb     TemporaryPrecommitValidation
	}{
		{name: "zero", cb: atomicEncoder},
		{name: "parameters", script: valid, params: [][]byte{[]byte("1")}, cb: atomicEncoder},
		{name: "policy", script: denied, cb: atomicEncoder},
		{name: "rows", script: valid, opts: TemporaryAtomicOptions{MaxRows: 1001}, cb: atomicEncoder},
		{name: "schema", script: valid, opts: TemporaryAtomicOptions{Schema: "bad\x00"}, cb: atomicEncoder},
		{name: "encoder", script: valid},
		{name: "statements", script: atomicScript(t, strings.Repeat("SELECT 1;", 257), 0, false), cb: atomicEncoder},
		{name: "parameter-nul", script: atomicScript(t, "SELECT $1", 1, false), params: [][]byte{{0}}, cb: atomicEncoder},
		{name: "parameter-utf8", script: atomicScript(t, "SELECT $1", 1, false), params: [][]byte{{0xff}}, cb: atomicEncoder},
		{name: "parameter-bytes", script: atomicScript(t, "SELECT $1", 1, false), params: [][]byte{[]byte(strings.Repeat("x", 1<<20+1))}, cb: atomicEncoder},
		{name: "schema-utf8", script: valid, opts: TemporaryAtomicOptions{Schema: string([]byte{0xff})}, cb: atomicEncoder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(trace())
			result, err := client.ExecuteTemporaryAtomic(context.Background(), tc.script, tc.params, tc.opts, tc.cb)
			if err == nil || result.Outcome != MachineTransactionNotStarted || result.Execution.Status != MachineExecutionNotStarted || len(trace()) != before {
				t.Fatalf("preflight dispatched: %+v %v %v", result, err, trace())
			}
		})
	}
	client.temporary = false
	before := len(trace())
	result, err := client.ExecuteTemporaryAtomic(context.Background(), valid, nil, TemporaryAtomicOptions{}, atomicEncoder)
	if err == nil || err.Error() != "POLICY_DENIED" || result.Outcome != MachineTransactionNotStarted || len(trace()) != before {
		t.Fatal("saved provenance bypass")
	}
}

func TestTemporaryAtomicExactDispatchAndTruncation(t *testing.T) {
	for _, parameters := range []bool{false, true} {
		t.Run(map[bool]string{false: "simple-script", true: "bound-single"}[parameters], func(t *testing.T) {
			sql := "/* original */ INSERT INTO t VALUES (1) RETURNING v; SELECT 2 -- trailing\n"
			var params [][]byte
			if parameters {
				sql, params = "SELECT $1", [][]byte{[]byte("one")}
			}
			client, trace := atomicWire(t, func(query string, b *pgproto3.Backend, _ net.Conn) (byte, bool) {
				if query != sql {
					return 0, false
				}
				if !parameters {
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("INSERT 0 1")})
				}
				machineExecuteRows(b, [][]byte{[]byte("one")}, [][]byte{[]byte("two")})
				return 'T', true
			})
			called := 0
			var cached []byte
			result, err := client.ExecuteTemporaryAtomic(context.Background(), atomicScript(t, sql, len(params), true), params, TemporaryAtomicOptions{MaxRows: 1, Schema: `x";COMMIT;--`}, func(ctx context.Context, result TemporaryAtomicResult) error {
				called++
				if result.Outcome != MachineTransactionCommitted || result.Execution.Status != MachineExecutionCompleted || atomicCount(trace(), "COMMIT") != 0 {
					t.Error("encoder not prospective/precommit")
				}
				var err error
				cached, err = json.Marshal(result)
				return err
			})
			if err != nil || result.Outcome != MachineTransactionCommitted || !result.Truncated || called != 1 {
				t.Fatalf("%+v %v", result, err)
			}
			encoded, _ := json.Marshal(result)
			if string(cached) != string(encoded) {
				t.Fatal("prospective bytes differ")
			}
			messages := trace()
			if atomicCount(messages, sql) != 1 || atomicCount(messages, "BEGIN") != 1 || atomicCount(messages, "COMMIT") != 1 || atomicCount(messages, "ROLLBACK") != 0 {
				t.Fatalf("transaction ownership %v", messages)
			}
			for i, message := range messages {
				if strings.Contains(message.sql, "READ WRITE") {
					t.Fatal("read-only policy overridden")
				}
				if message.sql == sql && (message.simple == parameters || messages[i-2].sql != "BEGIN" || messages[i-3].sql != "SET client_encoding='UTF8'" || messages[i-4].sql != "SET standard_conforming_strings=on") {
					t.Fatalf("original protocol/setup %v", messages)
				}
			}
		})
	}
}

func TestTemporaryAtomicPrecommitFailures(t *testing.T) {
	for _, mode := range []string{"sql", "late", "omitted-cell", "omitted-utf8", "omitted-shape", "metadata", "metadata-utf8", "format", "tag", "tag-utf8", "output", "message", "protocol", "results", "missing-result", "callback", "cancel", "rollback-stall", "rollback-tag", "rollback-disconnect"} {
		t.Run(mode, func(t *testing.T) {
			sql := "SELECT 1"
			if mode == "results" {
				sql = strings.Repeat("SELECT 1;", 256)
			}
			client, trace := atomicWire(t, func(query string, b *pgproto3.Backend, raw net.Conn) (byte, bool) {
				if query == "ROLLBACK" {
					switch mode {
					case "rollback-stall":
						_, _ = io.Copy(io.Discard, raw)
						return 'E', true
					case "rollback-tag":
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
						return 'I', true
					case "rollback-disconnect":
						_ = raw.Close()
						return 'E', true
					}
				}
				if query != sql {
					return 0, false
				}
				state := byte('T')
				switch mode {
				case "sql":
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "22012", Message: "private"})
					state = 'E'
				case "metadata", "metadata-utf8", "format":
					field := pgproto3.FieldDescription{Name: []byte(strings.Repeat("x", 64<<10+1)), DataTypeOID: 25}
					if mode == "metadata-utf8" {
						field.Name = []byte{0xff}
					}
					if mode == "format" {
						field.Name, field.Format = []byte("x"), 1
					}
					b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{field}})
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
				case "omitted-cell", "omitted-utf8", "omitted-shape":
					row := [][]byte{[]byte(strings.Repeat("x", 64<<10+1))}
					if mode == "omitted-utf8" {
						row = [][]byte{{0xff}}
					}
					if mode == "omitted-shape" {
						row = [][]byte{[]byte("a"), []byte("b")}
					}
					machineExecuteRows(b, [][]byte{[]byte("kept")}, row)
				case "output":
					rows := make([][][]byte, 5)
					for i := range rows {
						rows[i] = [][]byte{[]byte(strings.Repeat("\x01", 64<<10))}
					}
					machineExecuteRows(b, rows...)
				case "tag", "tag-utf8":
					tag := []byte("SELECT " + strings.Repeat("1", 64<<10))
					if mode == "tag-utf8" {
						tag = []byte{'S', 0xff}
					}
					b.Send(&pgproto3.CommandComplete{CommandTag: tag})
				case "message":
					_ = b.Flush()
					header := []byte{'D', 0, 0, 0, 0}
					binary.BigEndian.PutUint32(header[1:], machineExecuteMaxBody+5)
					_, _ = raw.Write(header)
				case "protocol":
					b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("unexpected")}})
				case "results":
					for range 257 {
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
					}
				case "missing-result":
				default:
					machineExecuteRows(b, [][]byte{[]byte("one")})
					if mode == "late" {
						b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23503", Message: "late private"})
						state = 'E'
					}
				}
				return state, true
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			maxRows := 1
			if mode == "output" {
				maxRows = 100
			}
			start := time.Now()
			result, err := client.ExecuteTemporaryAtomic(ctx, atomicScript(t, sql, 0, false), nil, TemporaryAtomicOptions{MaxRows: maxRows}, func(context.Context, TemporaryAtomicResult) error {
				if mode == "cancel" {
					cancel()
					return nil
				}
				if mode == "callback" || strings.HasPrefix(mode, "rollback-") {
					return &MachineError{Code: "RESOURCE_FAILURE"}
				}
				return nil
			})
			if err == nil || result.Outcome != MachineTransactionRolledBack && result.Outcome != MachineTransactionDisposed || len(result.Results) != 0 || atomicCount(trace(), "COMMIT") != 0 || atomicCount(trace(), "BEGIN READ ONLY") != 1 {
				t.Fatalf("failure %+v %v trace %v", result, err, trace())
			}
			if time.Since(start) > time.Second {
				t.Fatal("unbounded cleanup")
			}
			if mode == "callback" || mode == "rollback-stall" || mode == "rollback-tag" || mode == "rollback-disconnect" || mode == "sql" || mode == "late" {
				want := MachineTransactionRolledBack
				if strings.HasPrefix(mode, "rollback-") {
					want = MachineTransactionDisposed
					if !client.aborted {
						t.Fatal("failed rollback not disposed")
					}
				}
				if result.Outcome != want {
					t.Fatalf("want %s: %+v", want, result)
				}
			}
		})
	}
}

func TestTemporaryAtomicCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"commit", "rollback-tag", "reject-idle", "reject-open", "disconnect", "stall", "late-error"} {
		t.Run(mode, func(t *testing.T) {
			client, trace := atomicWire(t, func(sql string, b *pgproto3.Backend, raw net.Conn) (byte, bool) {
				if sql == "SELECT 1" {
					machineExecuteRows(b, [][]byte{[]byte("one")})
					return 'T', true
				}
				if sql != "COMMIT" {
					return 0, false
				}
				switch mode {
				case "rollback-tag":
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
				case "reject-idle", "reject-open":
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23505", Message: "private deferred constraint"})
					if mode == "reject-open" {
						return 'E', true
					}
				case "disconnect":
					_ = raw.Close()
				case "stall":
					_, _ = io.Copy(io.Discard, raw)
				case "late-error":
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "after acknowledgement"})
				default:
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
				}
				return 'I', true
			})
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			result, err := client.ExecuteTemporaryAtomic(ctx, atomicScript(t, "SELECT 1", 0, false), nil, TemporaryAtomicOptions{}, atomicEncoder)
			want := MachineTransactionRolledBack
			switch mode {
			case "commit", "late-error":
				want = MachineTransactionCommitted
			case "disconnect", "stall":
				want = MachineTransactionUnknown
			}
			if result.Outcome != want || (err == nil) != (mode == "commit") || atomicCount(trace(), "COMMIT") != 1 {
				t.Fatalf("terminal %+v %v trace %v", result, err, trace())
			}
			if want == MachineTransactionUnknown && result.Execution.Status != MachineExecutionUnknown || !result.Execution.EffectsMayHaveOccurred {
				t.Fatal("terminal execution evidence lost")
			}
			if mode == "reject-open" && atomicCount(trace(), "ROLLBACK") != 1 {
				t.Fatal("rejected commit not rolled back")
			}
			if want == MachineTransactionCommitted {
				cancel()
				if closeErr := client.Close(ctx); closeErr == nil || result.Outcome != MachineTransactionCommitted {
					t.Fatal("cleanup lost committed evidence")
				}
			}
		})
	}
}

func TestTemporaryAtomicEncoderBudgetBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		client, trace := atomicWire(t, func(sql string, b *pgproto3.Backend, _ net.Conn) (byte, bool) {
			if sql != "SELECT 1" {
				return 0, false
			}
			machineExecuteRows(b, [][]byte{[]byte("one")})
			return 'T', true
		})
		result, err := client.ExecuteTemporaryAtomic(context.Background(), atomicScript(t, "SELECT 1", 0, false), nil, TemporaryAtomicOptions{}, func(_ context.Context, result TemporaryAtomicResult) error {
			// Model the exact full T4 envelope (target, execution and padding),
			// rather than assuming the driver's retained-result estimate is exact.
			envelope := struct {
				Result TemporaryAtomicResult `json:"result"`
				Target string                `json:"target"`
			}{Result: result}
			encoded, _ := json.Marshal(envelope)
			envelope.Target = strings.Repeat("x", (1<<20)-len(encoded)+extra)
			encoded, _ = json.Marshal(envelope)
			if len(encoded) != (1<<20)+extra {
				t.Fatal("incorrect boundary fixture")
			}
			if len(encoded) > 1<<20 {
				return errors.New("private encoder error")
			}
			return nil
		})
		if extra == 0 && (err != nil || result.Outcome != MachineTransactionCommitted) || extra == 1 && (err == nil || result.Outcome != MachineTransactionRolledBack || atomicCount(trace(), "COMMIT") != 0) {
			t.Fatalf("budget boundary %+v %v", result, err)
		}
	}
}

func TestTemporaryAtomicResourceBoundaries(t *testing.T) {
	for _, mode := range []string{"256-results", "64k-cell-and-column", "null-and-empty"} {
		t.Run(mode, func(t *testing.T) {
			sql := "SELECT 1"
			if mode == "256-results" {
				sql = strings.Repeat("SELECT 1;", 256)
			}
			client, trace := atomicWire(t, func(query string, b *pgproto3.Backend, _ net.Conn) (byte, bool) {
				if query != sql {
					return 0, false
				}
				switch mode {
				case "256-results":
					for range 256 {
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 0")})
					}
				case "64k-cell-and-column":
					b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte(strings.Repeat("x", 64<<10)), DataTypeOID: 25}}})
					b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(strings.Repeat("x", 64<<10))}})
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				case "null-and-empty":
					b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("x"), DataTypeOID: 25}, {Name: []byte("x"), DataTypeOID: 25}}})
					b.Send(&pgproto3.DataRow{Values: [][]byte{nil, {}}})
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				}
				return 'T', true
			})
			result, err := client.ExecuteTemporaryAtomic(context.Background(), atomicScript(t, sql, 0, false), nil, TemporaryAtomicOptions{}, atomicEncoder)
			if err != nil || result.Outcome != MachineTransactionCommitted || atomicCount(trace(), "COMMIT") != 1 {
				t.Fatalf("boundary %+v %v", result, err)
			}
			if mode == "256-results" && len(result.Results) != 256 {
				t.Fatal("result boundary truncated")
			}
			if mode == "null-and-empty" && (result.Results[0].Rows[0][0] != nil || result.Results[0].Rows[0][1] == nil || *result.Results[0].Rows[0][1] != "") {
				t.Fatal("null/empty distinction lost")
			}
		})
	}
}

func TestTemporaryAtomicCancellationAndSetup(t *testing.T) {
	for _, mode := range []string{"before-begin", "begin-rejected", "begin-wrong-tag", "setup-rejected", "schema-rejected", "user-stall", "server-readonly"} {
		t.Run(mode, func(t *testing.T) {
			client, trace := atomicWire(t, func(sql string, b *pgproto3.Backend, raw net.Conn) (byte, bool) {
				if mode == "setup-rejected" && sql == "SET standard_conforming_strings=on" || mode == "schema-rejected" && strings.HasPrefix(sql, "SET LOCAL search_path") || mode == "begin-rejected" && sql == "BEGIN" || mode == "server-readonly" && sql == "INSERT INTO t VALUES(1)" {
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "25006", Message: "server policy"})
					if mode == "setup-rejected" || mode == "begin-rejected" {
						return 'I', true
					}
					return 'E', true
				}
				if mode == "begin-wrong-tag" && sql == "BEGIN" {
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
					return 'I', true
				}
				if mode == "user-stall" && sql == "INSERT INTO t VALUES(1)" {
					_, _ = io.Copy(io.Discard, raw)
					return 'T', true
				}
				return 0, false
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if mode == "before-begin" {
				cancel()
			}
			before := len(trace())
			result, err := client.ExecuteTemporaryAtomic(ctx, atomicScript(t, "INSERT INTO t VALUES(1)", 0, true), nil, TemporaryAtomicOptions{Schema: "public"}, atomicEncoder)
			if err == nil || atomicCount(trace(), "COMMIT") != 0 {
				t.Fatalf("setup/cancel committed %+v %v", result, err)
			}
			if mode == "before-begin" && (result.Outcome != MachineTransactionNotStarted || len(trace()) != before) {
				t.Fatal("canceled preflight dispatched")
			}
			if mode == "setup-rejected" && (result.Outcome != MachineTransactionNotStarted || atomicCount(trace(), "BEGIN") != 0) {
				t.Fatal("lexical setup failure began transaction")
			}
			if mode != "user-stall" && mode != "server-readonly" && atomicCount(trace(), "INSERT INTO t VALUES(1)") != 0 {
				t.Fatal("setup failure dispatched user SQL")
			}
			if mode == "user-stall" && result.Outcome != MachineTransactionDisposed {
				t.Fatal("canceled user SQL not disposed")
			}
		})
	}
}
