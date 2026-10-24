//go:build linux || darwin

package pg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

type MachineExecutionStatus string

const (
	MachineExecutionNotStarted MachineExecutionStatus = "not_started"
	MachineExecutionCompleted  MachineExecutionStatus = "completed"
	MachineExecutionFailed     MachineExecutionStatus = "failed"
	MachineExecutionUnknown    MachineExecutionStatus = "unknown"
	// Bound individual protocol allocations independently of retained JSON.
	machineExecuteMaxBody = 8 << 20
)

type MachineExecution struct {
	Status                 MachineExecutionStatus `json:"status"`
	EffectsMayHaveOccurred bool                   `json:"effects_may_have_occurred"`
	// Observing PREPARE warns of surviving state, not proof that its GID still exists.
	PreparedTransactionsMayRemain bool `json:"prepared_transactions_may_remain,omitempty"`
}

type MachineStatementResult struct {
	CommandTag       string          `json:"command_tag"`
	Columns          []MachineColumn `json:"columns"`
	Rows             [][]*string     `json:"rows"`
	Truncated        bool            `json:"truncated"`
	TruncationReason string          `json:"truncation_reason,omitempty"`
}

type MachineExecuteResult struct {
	Results          []MachineStatementResult `json:"results"`
	Execution        MachineExecution         `json:"-"`
	Truncated        bool                     `json:"-"`
	TruncationReason string                   `json:"-"`
}

// Execute sends the original SQL once, with PostgreSQL's native transaction
// semantics. Errors discard rows, never effects metadata; callers must not retry.
func (c *MachineClient) Execute(ctx context.Context, request MachineQuery) (result MachineExecuteResult, err error) {
	result.Execution.Status = MachineExecutionNotStarted
	if c.access != models.CLIAccessFull || c.readOnly {
		return result, &MachineError{Code: "POLICY_DENIED"}
	}
	analysis, analysisErr := query.AnalyzeCLIStatements(ctx, request.SQL, len(request.Parameters), false, false)
	if analysisErr != nil {
		code := "UNSUPPORTED_SQL"
		if errors.Is(analysisErr, query.ErrInvalidCLIInput) {
			code = "INVALID_INPUT"
		}
		return result, machineFailure(ctx, &MachineError{Code: code}, code)
	}
	if request.MaxRows == 0 {
		request.MaxRows = 100
	}
	if request.MaxRows < 1 || request.MaxRows > 1000 || len(request.Parameters) > 256 || len(request.Schema) > 1<<20 || !utf8.ValidString(request.Schema) || strings.IndexByte(request.Schema, 0) >= 0 {
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	parameterBytes := 0
	for _, parameter := range request.Parameters {
		parameterBytes += len(parameter)
		if parameterBytes > 1<<20 || !utf8.Valid(parameter) || strings.IndexByte(string(parameter), 0) >= 0 {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
	}
	c.operation.Lock()
	defer c.operation.Unlock()
	defer func() {
		if err != nil {
			result.Results = nil
		}
	}()
	conn := c.conn.PgConn()
	conn.Frontend().SetMaxBodyLen(machineExecuteMaxBody)
	// A simple Query is scanned as a whole before any same-batch SET executes.
	for _, sql := range []string{"SET standard_conforming_strings=on", "SET client_encoding='UTF8'"} {
		if setupErr := c.exec(ctx, sql); setupErr != nil {
			return result, machineFailure(ctx, setupErr, "DATABASE_FAILURE")
		}
	}
	if request.Schema != "" {
		if setupErr := c.exec(ctx, "SET search_path TO "+pgx.Identifier{request.Schema}.Sanitize()); setupErr != nil {
			return result, machineFailure(ctx, setupErr, "DATABASE_FAILURE")
		}
	}
	if ctx.Err() != nil {
		return result, &MachineError{Code: "TIMEOUT"}
	}
	result.Results = []MachineStatementResult{}
	result.Execution = MachineExecution{Status: MachineExecutionUnknown, EffectsMayHaveOccurred: true}
	// Reserve envelope and truncation overhead; count escaped JSON, not raw bytes.
	budget, rows, statement := 4096, 0, 0
	var retentionErr, executionErr error
	consume := func(reader *pgconn.ResultReader) {
		item := MachineStatementResult{Columns: []MachineColumn{}, Rows: [][]*string{}}
		retain := len(result.Results) < 256 && retentionErr == nil
		if !retain && retentionErr == nil {
			result.Truncated, result.TruncationReason = true, "max_results"
		}
		failRetention := func() { retentionErr = &MachineError{Code: "RESOURCE_FAILURE"}; retain = false }
		charge := func(value any) {
			encoded, _ := json.Marshal(value)
			budget += len(encoded) + 1
			if budget > 1<<20 {
				failRetention()
			}
		}
		if retain {
			budget += 128
		}
		fields := reader.FieldDescriptions()
		for _, field := range fields {
			if len(field.Name) > 64<<10 || !utf8.ValidString(field.Name) {
				failRetention()
			}
			if retain {
				column := MachineColumn{Name: field.Name, TypeOID: field.DataTypeOID, Encoding: "postgres-text"}
				charge(column)
				if retain {
					item.Columns = append(item.Columns, column)
				}
			}
		}
		for reader.NextRow() {
			values := reader.Values()
			if len(values) != len(fields) {
				failRetention()
			}
			for _, value := range values {
				if len(value) > 64<<10 || !utf8.Valid(value) {
					failRetention()
				}
			}
			if !retain {
				continue
			}
			if rows >= request.MaxRows {
				item.Truncated, item.TruncationReason = true, "max_rows"
				result.Truncated = true
				if result.TruncationReason == "" {
					result.TruncationReason = "max_rows"
				}
				continue
			}
			row := make([]*string, len(values))
			for i, value := range values {
				if value != nil {
					text := string(value)
					row[i] = &text
				}
			}
			charge(row)
			if retain {
				item.Rows = append(item.Rows, row)
				rows++
			}
		}
		tag, closeErr := reader.Close()
		commandTag := tag.String()
		if commandTag == "PREPARE TRANSACTION" {
			result.Execution.PreparedTransactionsMayRemain = true
		}
		if commandTag == "ROLLBACK" && statement < len(analysis.Statements) {
			rangeSQL := analysis.Statements[statement]
			if machineExecuteCommit(request.SQL[rangeSQL.Start:rangeSQL.End]) {
				closeErr = pgx.ErrTxCommitRollback
			}
		}
		statement++
		if closeErr != nil {
			executionErr = closeErr
		}
		if retain {
			if len(commandTag) > 64<<10 || !utf8.ValidString(commandTag) {
				failRetention()
			} else {
				charge(commandTag)
			}
			if retain {
				item.CommandTag = commandTag
				result.Results = append(result.Results, item)
			}
		}
	}
	if len(request.Parameters) == 0 {
		batch := conn.Exec(ctx, request.SQL)
		for batch.NextResult() {
			consume(batch.ResultReader())
		}
		if terminalErr := batch.Close(); terminalErr != nil {
			executionErr = terminalErr
		}
	} else {
		consume(conn.ExecParams(ctx, request.SQL, request.Parameters, nil, nil, nil))
	}
	if executionErr == nil && !conn.IsClosed() {
		result.Execution.Status = MachineExecutionCompleted
	} else {
		var server *pgconn.PgError
		if !conn.IsClosed() && (errors.As(executionErr, &server) || errors.Is(executionErr, pgx.ErrTxCommitRollback)) {
			result.Execution.Status = MachineExecutionFailed
		}
		if executionErr == nil {
			executionErr = &MachineError{Code: "DATABASE_FAILURE"}
		}
	}
	if executionErr != nil {
		err = machineFailure(ctx, executionErr, "DATABASE_FAILURE")
	} else {
		err = retentionErr
	}
	if !conn.IsClosed() && (conn.TxStatus() == 'T' || conn.TxStatus() == 'E') {
		if executionErr == nil {
			result.Execution.Status = MachineExecutionFailed
			err = &MachineError{Code: "INCOMPLETE_TRANSACTION"}
		}
		cleanup := c.freshCleanup()
		if rollbackErr := c.exec(cleanup, "ROLLBACK"); rollbackErr != nil {
			c.Abort()
		}
	}
	return result, err
}

// Only a ROLLBACK tag for a submitted COMMIT/END is an unsuccessful commit.
func machineExecuteCommit(sql string) bool {
	for {
		sql = strings.TrimLeft(sql, " \t\r\n\f\v")
		if strings.HasPrefix(sql, "--") {
			if i := strings.IndexAny(sql, "\r\n"); i >= 0 {
				sql = sql[i+1:]
				continue
			}
			return false
		}
		if strings.HasPrefix(sql, "/*") {
			depth, i := 1, 2
			for i+1 < len(sql) && depth > 0 {
				switch sql[i : i+2] {
				case "/*":
					depth++
					i += 2
				case "*/":
					depth--
					i += 2
				default:
					i++
				}
			}
			if depth != 0 {
				return false
			}
			sql = sql[i:]
			continue
		}
		end := 0
		for end < len(sql) && (sql[end] >= 'a' && sql[end] <= 'z' || sql[end] >= 'A' && sql[end] <= 'Z') {
			end++
		}
		keyword := sql[:end]
		return strings.EqualFold(keyword, "COMMIT") || strings.EqualFold(keyword, "END")
	}
}
