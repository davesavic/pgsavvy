//go:build linux || darwin

package pg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineExecutePolicy(t *testing.T) {
	for _, access := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, "invalid", models.CLIAccessFull} {
		for _, readOnly := range []bool{false, true} {
			if access == models.CLIAccessFull && !readOnly {
				continue
			}
			client := &MachineClient{access: access, readOnly: readOnly}
			result, err := client.Execute(context.Background(), MachineQuery{SQL: "DELETE FROM protected"})
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "POLICY_DENIED" || result.Execution.Status != MachineExecutionNotStarted || result.Execution.EffectsMayHaveOccurred || len(result.Results) != 0 {
				t.Fatalf("gate %q/%v: %+v %v", access, readOnly, result, err)
			}
		}
	}
}

// machineExecuteWire uses real pgx readers and scripted PostgreSQL messages.
// Setup/cleanup use extended protocol; user batches must use one simple Query.
func machineExecuteWire(t *testing.T, sql string, respond func(*pgproto3.Backend, net.Conn), rollbackFail bool) (*MachineClient, func() []string) {
	t.Helper()
	machineTestEnvironment(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var owned net.Conn
	var queries []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		owned = conn
		mu.Unlock()
		defer conn.Close()
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
		query := ""
		for {
			msg, err := b.Receive()
			if err != nil {
				return
			}
			switch msg := msg.(type) {
			case *pgproto3.Query:
				mu.Lock()
				queries = append(queries, msg.String)
				mu.Unlock()
				if msg.String != sql {
					return
				}
				respond(b, conn)
			case *pgproto3.Parse:
				query = msg.Query
				mu.Lock()
				queries = append(queries, query)
				mu.Unlock()
				b.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				b.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				b.Send(&pgproto3.NoData{})
			case *pgproto3.Execute:
				if query == "ROLLBACK" && rollbackFail && strings.Contains(sql, "stall-rollback") {
					io.Copy(io.Discard, conn)
					return
				}
				if query == "ROLLBACK" && rollbackFail {
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "private cleanup"})
				} else {
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
				}
			case *pgproto3.Sync:
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
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
		ln.Close()
		mu.Lock()
		if owned != nil {
			owned.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("wire fixture not joined")
		}
	})
	profile := models.Connection{Name: "synthetic", Driver: "postgres", DSN: "postgres://u@" + ln.Addr().String() + "/d?sslmode=disable", Password: "synthetic", CLIAccess: models.CLIAccessFull}
	client, err := OpenMachine(context.Background(), profile, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(context.Background()) })
	return client, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), queries...) }
}

func machineExecuteRows(b *pgproto3.Backend, rows ...[][]byte) {
	b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("payload"), DataTypeOID: 25}}})
	for _, row := range rows {
		b.Send(&pgproto3.DataRow{Values: row})
	}
	b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
}

func TestMachineExecuteNativeBatch(t *testing.T) {
	sql := "SELECT 1; COMMIT"
	client, queries := machineExecuteWire(t, sql, func(b *pgproto3.Backend, _ net.Conn) {
		b.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte(""), DataTypeOID: 25}, {Name: []byte("x"), DataTypeOID: 25}, {Name: []byte("x"), DataTypeOID: 25}}})
		b.Send(&pgproto3.DataRow{Values: [][]byte{nil, {}, []byte("null")}})
		b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		b.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	}, false)
	result, err := client.Execute(context.Background(), MachineQuery{SQL: sql, Schema: `weird";ROLLBACK;--`})
	if err != nil || result.Execution.Status != MachineExecutionCompleted || !result.Execution.EffectsMayHaveOccurred || len(result.Results) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	item := result.Results[0]
	if item.CommandTag != "SELECT 1" || len(item.Columns) != 3 || item.Columns[0].Name != "" || item.Columns[1].Name != item.Columns[2].Name || item.Rows[0][0] != nil || *item.Rows[0][1] != "" || *item.Rows[0][2] != "null" {
		t.Fatalf("lossy rows: %+v", item)
	}
	seen := queries()
	if seen[len(seen)-1] != sql || seen[len(seen)-2] != `SET search_path TO "weird"";ROLLBACK;--"` || seen[len(seen)-3] != "SET client_encoding='UTF8'" || seen[len(seen)-4] != "SET standard_conforming_strings=on" {
		t.Fatalf("dispatch/setup: %q", seen)
	}
	count := 0
	for _, query := range seen {
		if query == sql {
			count++
		}
		if strings.HasPrefix(query, "BEGIN") {
			t.Fatal("wrapper")
		}
	}
	if count != 1 {
		t.Fatal("SQL replayed")
	}
}

func TestMachineExecuteDrainLimits(t *testing.T) {
	for _, mode := range []string{"rows", "results", "cell", "invalid_utf8", "bytes", "terminal_error"} {
		t.Run(mode, func(t *testing.T) {
			sql := "SELECT 1; COMMIT"
			client, _ := machineExecuteWire(t, sql, func(b *pgproto3.Backend, _ net.Conn) {
				switch mode {
				case "results":
					for range 260 {
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
					}
					b.Send(&pgproto3.CommandComplete{CommandTag: []byte("PREPARE TRANSACTION")})
				case "cell":
					machineExecuteRows(b, [][]byte{[]byte("kept")}, [][]byte{[]byte(strings.Repeat("x", 64<<10+1))})
				case "invalid_utf8":
					machineExecuteRows(b, [][]byte{{0xff}})
				case "bytes":
					rows := make([][][]byte, 20)
					for i := range rows {
						rows[i] = [][]byte{[]byte(strings.Repeat("\x01", 64<<10))}
					}
					machineExecuteRows(b, rows...)
				default:
					machineExecuteRows(b, [][]byte{[]byte("one")}, [][]byte{[]byte("two")})
					machineExecuteRows(b, [][]byte{[]byte("three")})
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
				if mode == "terminal_error" {
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23503", Message: "private terminal"})
				}
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			}, false)
			maxRows := 1
			if mode == "bytes" {
				maxRows = 100
			}
			result, err := client.Execute(context.Background(), MachineQuery{SQL: sql, MaxRows: maxRows})
			if mode == "rows" || mode == "results" {
				if err != nil || !result.Truncated || result.Execution.Status != MachineExecutionCompleted {
					t.Fatalf("%+v %v", result, err)
				}
				if mode == "rows" && (len(result.Results[0].Rows) != 1 || len(result.Results[1].Rows) != 0 || result.Results[2].CommandTag != "COMMIT") {
					t.Fatal("aggregate rows or final tag")
				}
				if mode == "results" && (len(result.Results) != 256 || !result.Execution.PreparedTransactionsMayRemain) {
					t.Fatal("result bound/prepared tag lost")
				}
			} else {
				var safe *MachineError
				wantCode, status := "RESOURCE_FAILURE", MachineExecutionCompleted
				if mode == "terminal_error" {
					wantCode, status = "DATABASE_FAILURE", MachineExecutionFailed
				}
				if !errors.As(err, &safe) || safe.Code != wantCode || result.Execution.Status != status || len(result.Results) != 0 {
					t.Fatalf("%+v %v", result, err)
				}
			}
		})
	}
}

func TestMachineExecuteTerminalOutcomes(t *testing.T) {
	for _, mode := range []string{"incomplete", "incomplete_E", "cleanup_error", "unknown_cleanup_error", "deferred", "rollback_tag", "disconnect_commit", "cancel_commit", "protocol_limit", "prepared_protocol_limit", "retention_then_sql"} {
		t.Run(mode, func(t *testing.T) {
			sql := "COMMIT"
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, queries := machineExecuteWire(t, sql, func(b *pgproto3.Backend, conn net.Conn) {
				if mode == "unknown_cleanup_error" {
					b.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("unexpected")}})
					b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
					return
				}
				if mode == "protocol_limit" || mode == "prepared_protocol_limit" {
					if mode == "prepared_protocol_limit" {
						b.Send(&pgproto3.CommandComplete{CommandTag: []byte("PREPARE TRANSACTION")})
						b.Flush()
					}
					header := []byte{'D', 0, 0, 0, 0}
					binary.BigEndian.PutUint32(header[1:], machineExecuteMaxBody+5)
					conn.Write(header)
					return
				}
				if mode == "retention_then_sql" {
					machineExecuteRows(b, [][]byte{[]byte(strings.Repeat("x", 64<<10+1))})
				}
				tag := "COMMIT"
				if mode == "rollback_tag" {
					tag = "ROLLBACK"
				}
				if mode == "cleanup_error" {
					tag = "PREPARE TRANSACTION"
				}
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
				switch mode {
				case "deferred", "retention_then_sql":
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23503", Message: "private deferred"})
				case "disconnect_commit":
					b.Flush()
					conn.Close()
					return
				case "cancel_commit":
					b.Flush()
					cancel()
					io.Copy(io.Discard, conn)
					return
				}
				status := byte('I')
				if mode == "incomplete" || mode == "cleanup_error" {
					status = 'T'
				}
				if mode == "incomplete_E" {
					status = 'E'
				}
				b.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			}, mode == "cleanup_error" || mode == "unknown_cleanup_error")
			result, err := client.Execute(ctx, MachineQuery{SQL: sql})
			var safe *MachineError
			if !errors.As(err, &safe) || len(result.Results) != 0 || !result.Execution.EffectsMayHaveOccurred {
				t.Fatalf("%+v %v", result, err)
			}
			want := MachineExecutionFailed
			if mode == "incomplete" || mode == "incomplete_E" || mode == "cleanup_error" {
				if safe.Code != "INCOMPLETE_TRANSACTION" || queries()[len(queries())-1] != "ROLLBACK" {
					t.Fatalf("cleanup missing %v", err)
				}
			}
			if mode == "disconnect_commit" || mode == "cancel_commit" || mode == "protocol_limit" || mode == "prepared_protocol_limit" || mode == "unknown_cleanup_error" {
				want = MachineExecutionUnknown
			}
			if mode == "unknown_cleanup_error" && (safe.Code != "DATABASE_FAILURE" || queries()[len(queries())-1] != "ROLLBACK") {
				t.Fatal("cleanup replaced unknown protocol outcome")
			}
			if result.Execution.Status != want || mode == "cleanup_error" && !result.Execution.PreparedTransactionsMayRemain {
				t.Fatalf("metadata: %+v %v", result, err)
			}
			if mode == "retention_then_sql" && safe.SQLState != "23503" {
				t.Fatal("retention hid SQL failure")
			}
			if mode == "prepared_protocol_limit" && !result.Execution.PreparedTransactionsMayRemain {
				t.Fatal("protocol failure erased prepared flag")
			}
		})
	}
}

func TestMachineExecuteClosePreservesOutcome(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			client, _ := machineExecuteWire(t, "SELECT 1", func(b *pgproto3.Backend, _ net.Conn) {
				b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				if failed {
					b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "23503", Message: "private terminal"})
				}
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			}, false)
			result, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT 1"})
			want := MachineExecutionCompleted
			if failed {
				want = MachineExecutionFailed
			}
			if (err != nil) != failed || result.Execution.Status != want || !result.Execution.EffectsMayHaveOccurred {
				t.Fatalf("execution: %+v %v", result, err)
			}
			before := result.Execution
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var safe *MachineError
			if closeErr := client.Close(ctx); !errors.As(closeErr, &safe) || safe.Code != "RESOURCE_FAILURE" || result.Execution != before {
				t.Fatalf("close changed original outcome: %+v %v", result, closeErr)
			}
		})
	}
}

func TestMachineExecuteOperationOwnership(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client, _ := machineExecuteWire(t, "SELECT 1", func(b *pgproto3.Backend, _ net.Conn) {
		enterOnce.Do(func() { close(entered); <-release })
		b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	}, false)
	done := make(chan error, 2)
	run := func() { _, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT 1"}); done <- err }
	go run()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first operation not dispatched")
	}
	go run()
	select {
	case err := <-done:
		t.Fatalf("operation bypassed owner before draining: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("operation not joined")
		}
	}
}

func TestMachineExecuteSetupFailure(t *testing.T) {
	client, queries := machineExecuteWire(t, "SELECT 1", func(_ *pgproto3.Backend, _ net.Conn) { t.Error("user dispatch after setup failure") }, false)
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT 1"})
	if err == nil || result.Execution.Status != MachineExecutionNotStarted || result.Execution.EffectsMayHaveOccurred {
		t.Fatalf("%+v %v", result, err)
	}
	for _, sql := range queries() {
		if sql == "SELECT 1" {
			t.Fatal("user SQL dispatched")
		}
	}
}

func TestMachineExecuteRollbackBounded(t *testing.T) {
	client, _ := machineExecuteWire(t, "SELECT 'stall-rollback'", func(b *pgproto3.Backend, _ net.Conn) {
		b.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		b.Send(&pgproto3.ReadyForQuery{TxStatus: 'T'})
	}, true)
	client.cleanup = 20 * time.Millisecond
	started := time.Now()
	result, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT 'stall-rollback'"})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "INCOMPLETE_TRANSACTION" || result.Execution.Status != MachineExecutionFailed || !result.Execution.EffectsMayHaveOccurred || len(result.Results) != 0 || time.Since(started) > time.Second {
		t.Fatalf("rollback bound/status: %+v %v", result, err)
	}
}

func TestMachineExecuteParameters(t *testing.T) {
	script := &machineQueryScript{rows: [][][]byte{{[]byte("bound")}}}
	client := machineQueryWire(t, script)
	client.access = models.CLIAccessFull
	result, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT $1::text AS payload", Parameters: [][]byte{[]byte("bound"), nil}})
	if err != nil || result.Execution.Status != MachineExecutionCompleted || *result.Results[0].Rows[0][0] != "bound" {
		t.Fatalf("%+v %v", result, err)
	}
	script.mu.Lock()
	defer script.mu.Unlock()
	if script.queries != 1 || len(script.parameters) != 2 || string(script.parameters[0]) != "bound" || script.parameters[1] != nil {
		t.Fatal("parameter binding/replay")
	}
}

func TestMachineExecuteValidation(t *testing.T) {
	for _, request := range []MachineQuery{
		{}, {SQL: "-- only comment"}, {SQL: "SELECT '\x00'"}, {SQL: "SELECT 1", MaxRows: -1},
		{SQL: "SELECT 1", MaxRows: 1001}, {SQL: "SELECT 1", Schema: "x\x00y"},
		{SQL: "SELECT 1", Schema: strings.Repeat("x", 1<<20+1)},
		{SQL: "SELECT 1", Parameters: make([][]byte, 257)},
		{SQL: "SELECT $1", Parameters: [][]byte{{0xff}}},
		{SQL: "SELECT $1", Parameters: [][]byte{{0}}},
		{SQL: "SELECT $1", Parameters: [][]byte{[]byte(strings.Repeat("x", 1<<20+1))}},
		{SQL: "SELECT 1; SELECT $1", Parameters: [][]byte{[]byte("1")}},
		{SQL: "SELECT 1; COPY x FROM STDIN"}, {SQL: "COPY (SELECT 1) TO STDOUT"},
	} {
		client := &MachineClient{access: models.CLIAccessFull}
		result, err := client.Execute(context.Background(), request)
		if err == nil || result.Execution.Status != MachineExecutionNotStarted || result.Execution.EffectsMayHaveOccurred {
			t.Fatalf("invalid request dispatched: %+v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := (&MachineClient{access: models.CLIAccessFull}).Execute(ctx, MachineQuery{SQL: "SELECT 1"})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "TIMEOUT" || result.Execution.Status != MachineExecutionNotStarted || result.Execution.EffectsMayHaveOccurred {
		t.Fatalf("cancelled preflight: %+v %v", result, err)
	}
}

func TestMachineExecuteRowBounds(t *testing.T) {
	for _, maxRows := range []int{0, 1, 1000} {
		t.Run(fmt.Sprint(maxRows), func(t *testing.T) {
			client, _ := machineExecuteWire(t, "SELECT 1", func(b *pgproto3.Backend, _ net.Conn) {
				rows := make([][][]byte, 1002)
				for i := range rows {
					rows[i] = [][]byte{[]byte("x")}
				}
				machineExecuteRows(b, rows...)
				b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			}, false)
			result, err := client.Execute(context.Background(), MachineQuery{SQL: "SELECT 1", MaxRows: maxRows})
			want := maxRows
			if want == 0 {
				want = 100
			}
			if err != nil || len(result.Results) != 1 || len(result.Results[0].Rows) != want || !result.Results[0].Truncated {
				t.Fatalf("rows: %+v %v", result, err)
			}
		})
	}
}

func TestMachineExecuteInitialization(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "guarded"}[readOnly], func(t *testing.T) {
			machineTestEnvironment(t)
			fixture := newMachineWireFixture(t)
			profile := fixture.profile(t)
			profile.CLIAccess, profile.ReadOnly = models.CLIAccessFull, readOnly
			client, err := OpenMachine(context.Background(), profile, MachineOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close(context.Background())
			fixture.mu.Lock()
			queries := strings.Join(fixture.queries, "\n")
			fixture.mu.Unlock()
			if strings.Contains(queries, "SET default_transaction_read_only=on") != readOnly || strings.Contains(queries, "read_only=off") {
				t.Fatal("initialization changed readonly authority")
			}
			if client.readOnly != readOnly {
				t.Fatal("readonly not captured")
			}
		})
	}
}
