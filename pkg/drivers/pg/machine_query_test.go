//go:build linux || darwin

package pg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/cockroachdb-parser/pkg/sql/parser"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestMachineQuery_Policy(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1", "VALUES (1),(2)", "TABLE exact_name", "WITH a AS (SELECT 1) SELECT * FROM a", "WITH RECURSIVE a(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM a WHERE n<3) SELECT * FROM a",
		"SELECT NULL, 'null', 9223372036854775807::int8, 123.456::numeric, ARRAY[1,2]", "SELECT CASE WHEN true THEN (SELECT 1) ELSE 2 END", "SELECT count(*) FILTER(WHERE true) OVER (PARTITION BY x ORDER BY y ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) FROM t",
		"SELECT * FROM a JOIN (SELECT * FROM b) c ON a.id=c.id", "SELECT 1 UNION SELECT 2 INTERSECT SELECT 3", "SELECT $1::text, $2::numeric",
	} {
		if err := ValidateMachineQuery(context.Background(), sql); err != nil {
			t.Errorf("positive corpus %q: %v", sql, err)
		}
	}
	for _, sql := range []string{
		"", "SELECT 1;SELECT 2", "DELETE FROM x", "INSERT INTO x VALUES(1)", "UPDATE x SET y=1", "SET ROLE admin", "BEGIN", "COMMIT", "ROLLBACK", "SHOW ALL", "EXPLAIN SELECT 1", "COPY x TO STDOUT", "SELECT 1 INTO x", "SELECT * FROM x FOR UPDATE", "SELECT * FROM x FOR SHARE", "SELECT * FROM (SELECT * FROM x FOR KEY SHARE) s",
		"WITH a AS (DELETE FROM x RETURNING *) SELECT * FROM a", "WITH a AS (SELECT 1), b AS (WITH c AS (UPDATE x SET y=1 RETURNING *) SELECT * FROM c) SELECT * FROM a",
		"SELECT * FROM [DELETE FROM x RETURNING *]", "SELECT * FROM a JOIN b ON EXISTS(SELECT 1 FROM x FOR UPDATE)", "SELECT sum(x) OVER (ORDER BY (SELECT y FROM z FOR SHARE)) FROM t", "SELECT * FROM x@some_index", "SELECT broken syntax ???",
	} {
		if err := ValidateMachineQuery(context.Background(), sql); err == nil {
			t.Errorf("negative corpus accepted %q", sql)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ValidateMachineQuery(ctx, "SELECT 1"); err == nil {
		t.Fatal("cancelled AST check succeeded")
	}
	client := &MachineClient{}
	if _, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 1"}); err == nil {
		t.Fatal("metadata/disabled client queried rows")
	}
}

func TestMachineQuery_Policy_NestedAST(t *testing.T) {
	for _, sql := range []string{
		"WITH unused AS (DELETE FROM x RETURNING *) SELECT 1",
		"SELECT * FROM a JOIN b ON EXISTS(SELECT 1 FROM x FOR UPDATE)",
		"SELECT sum(x) OVER (ORDER BY (SELECT y FROM z FOR SHARE)) FROM t",
		"SELECT * FROM (WITH unused AS (INSERT INTO x VALUES(1) RETURNING *) SELECT 1) s",
	} {
		if _, err := parser.ParseOne(sql); err != nil {
			t.Fatalf("negative AST fixture was not parsed: %v", err)
		}
		if err := ValidateMachineQuery(context.Background(), sql); err == nil {
			t.Fatal("nested write/lock AST accepted")
		}
	}
}

type machineQueryScript struct {
	mu                                      sync.Mutex
	fields                                  []pgproto3.FieldDescription
	rows                                    [][][]byte
	terminalError, rollbackError, stallTail bool
	sql                                     string
	parameters                              [][]byte
	queries                                 int
	queryMatch, terminalCode                string
}

func machineQueryWire(t *testing.T, script *machineQueryScript) *MachineClient {
	t.Helper()
	machineTestEnvironment(t)
	if script.fields == nil {
		script.fields = []pgproto3.FieldDescription{{Name: []byte("payload"), DataTypeOID: 25}}
	}
	match := script.queryMatch
	if match == "" {
		match = "payload"
	}
	terminalCode := script.terminalCode
	if terminalCode == "" {
		terminalCode = "22012"
	}
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
		status := byte('I')
		for {
			message, err := backend.Receive()
			if err != nil {
				return
			}
			switch message := message.(type) {
			case *pgproto3.Parse:
				query = message.Query
				if strings.Contains(query, match) {
					script.mu.Lock()
					script.sql = query
					script.queries++
					script.mu.Unlock()
				}
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				if strings.Contains(query, match) {
					script.mu.Lock()
					script.parameters = make([][]byte, len(message.Parameters))
					for i, value := range message.Parameters {
						if value != nil {
							script.parameters[i] = append([]byte{}, value...)
						}
					}
					script.mu.Unlock()
					if len(message.ParameterFormatCodes) > 0 && message.ParameterFormatCodes[0] != 0 {
						t.Error("binary parameters")
					}
					if len(message.ResultFormatCodes) > 0 && message.ResultFormatCodes[0] != 0 {
						t.Error("binary results")
					}
				}
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				if strings.Contains(query, match) {
					backend.Send(&pgproto3.RowDescription{Fields: script.fields})
				} else {
					backend.Send(&pgproto3.NoData{})
				}
			case *pgproto3.Execute:
				if strings.HasPrefix(query, "BEGIN") {
					status = 'T'
				}
				if strings.Contains(query, match) {
					for _, row := range script.rows {
						backend.Send(&pgproto3.DataRow{Values: row})
						if backend.Flush() != nil {
							return
						}
					}
					if script.stallTail {
						_, _ = io.Copy(io.Discard, conn)
						return
					}
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
					if script.terminalError {
						status = 'E'
						backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: terminalCode, Message: "SECRET SQL terminal error"})
					}
				} else if query == "ROLLBACK" && script.rollbackError {
					backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "SECRET rollback"})
					status = 'E'
				} else {
					if query == "ROLLBACK" {
						status = 'I'
					}
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
				}
			case *pgproto3.Sync:
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
		_ = ln.Close()
		mu.Lock()
		if owned != nil {
			_ = owned.Close()
		}
		mu.Unlock()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("query fixture not joined")
		}
	})
	profile := models.Connection{Name: "synthetic", Driver: "postgres", DSN: "postgres://u@" + ln.Addr().String() + "/d?sslmode=disable", Password: "synthetic", CLIAccess: models.CLIAccessRowRead}
	client, err := OpenMachine(context.Background(), profile, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}

func TestMachineQuery_TextValues(t *testing.T) {
	script := &machineQueryScript{fields: []pgproto3.FieldDescription{{Name: []byte("dup"), DataTypeOID: 25}, {Name: []byte("dup"), DataTypeOID: 114}, {Name: []byte("precise"), DataTypeOID: 1700}, {Name: []byte("empty"), DataTypeOID: 25}}, rows: [][][]byte{{nil, []byte("null"), []byte("9007199254740993.123456789"), {}}}}
	client := machineQueryWire(t, script)
	result, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 'payload'"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Columns) != 4 || result.Columns[0].Name != "dup" || result.Columns[1].Name != "dup" || result.Columns[1].TypeOID != 114 || result.Columns[0].Encoding != "postgres-text" || len(result.Rows) != 1 || result.Rows[0][0] != nil || result.Rows[0][1] == nil || *result.Rows[0][1] != "null" || *result.Rows[0][2] != "9007199254740993.123456789" || result.Rows[0][3] == nil || *result.Rows[0][3] != "" {
		t.Fatal("text/null/OID/position changed")
	}
}

func TestMachineQuery_Parameters(t *testing.T) {
	for _, parameters := range [][][]byte{nil, {[]byte("1.2300e+25"), nil, []byte(""), []byte(`{"a":null}`)}} {
		script := &machineQueryScript{rows: [][][]byte{{[]byte("ok")}}}
		client := machineQueryWire(t, script)
		sql := "/* retain original SQL */ SELECT 'payload'"
		if _, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: sql, Parameters: parameters}); err != nil {
			t.Fatal(err)
		}
		script.mu.Lock()
		if script.sql != sql || len(script.parameters) != len(parameters) || script.queries != 1 {
			t.Error("original SQL/one extended execution lost")
		}
		for i, value := range parameters {
			if !bytes.Equal(script.parameters[i], value) || (value == nil) != (script.parameters[i] == nil) {
				t.Error("parameter lexeme/null changed")
			}
		}
		script.mu.Unlock()
	}
}

func TestMachineQuery_Bounds(t *testing.T) {
	for _, tc := range []struct {
		rows, limit, retained int
		truncated             bool
	}{{2, 2, 2, false}, {3, 2, 2, true}, {101, 0, 100, true}, {1000, 1000, 1000, false}, {1001, 1000, 1000, true}} {
		script := &machineQueryScript{}
		for i := 0; i < tc.rows; i++ {
			script.rows = append(script.rows, [][]byte{[]byte(strconv.Itoa(i))})
		}
		client := machineQueryWire(t, script)
		result, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 'payload'", MaxRows: tc.limit})
		if err != nil || len(result.Rows) != tc.retained || result.Truncated != tc.truncated {
			t.Fatalf("rows=%d limit=%d result=%d truncated%v err%v", tc.rows, tc.limit, len(result.Rows), result.Truncated, err)
		}
	}
	for _, tc := range []struct {
		value []byte
		rows  int
		fails bool
	}{{bytes.Repeat([]byte{'x'}, 64<<10), 1, false}, {bytes.Repeat([]byte{'x'}, (64<<10)+1), 1, true}, {[]byte{0xff}, 1, true}, {bytes.Repeat([]byte{'x'}, 64<<10), 17, true}} {
		script := &machineQueryScript{}
		for range tc.rows {
			script.rows = append(script.rows, [][]byte{tc.value})
		}
		client := machineQueryWire(t, script)
		_, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 'payload'"})
		if (err != nil) != tc.fails {
			t.Fatalf("cell/document bounds: %v", err)
		}
		if tc.fails {
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != "RESOURCE_FAILURE" {
				t.Fatalf("resource classification: %v", err)
			}
		}
	}
}

func TestMachineQuery_TerminalErrors(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		script := &machineQueryScript{rows: [][][]byte{{[]byte("prefix")}, {[]byte("omitted")}}, terminalError: !rollback, rollbackError: rollback}
		client := machineQueryWire(t, script)
		result, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 'payload'", MaxRows: 1})
		if err == nil || len(result.Rows) > 0 || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("terminal/rollback error became prefix success: %v", err)
		}
	}
}

func TestMachineQuery_Cancel(t *testing.T) {
	script := &machineQueryScript{rows: [][][]byte{{[]byte("prefix")}, {[]byte("lookahead")}}, stallTail: true}
	client := machineQueryWire(t, script)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, err := client.QueryReadOnly(ctx, MachineQuery{SQL: "SELECT 'payload'", MaxRows: 1})
	cancel()
	cancel()
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "TIMEOUT" || len(result.Rows) > 0 || time.Since(start) > time.Second {
		t.Fatalf("slow tail published prefix: %v", err)
	}
}
