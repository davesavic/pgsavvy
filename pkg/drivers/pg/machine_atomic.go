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
)

type MachineTransactionOutcome string

const (
	MachineTransactionNotStarted MachineTransactionOutcome = "not_started"
	MachineTransactionRolledBack MachineTransactionOutcome = "rolled_back"
	MachineTransactionDisposed   MachineTransactionOutcome = "disposed"
	MachineTransactionCommitted  MachineTransactionOutcome = "committed"
	MachineTransactionUnknown    MachineTransactionOutcome = "unknown"
)

type TemporaryAtomicOptions struct {
	MaxRows int
	Schema  string
}

type TemporaryAtomicResult struct {
	MachineExecuteResult
	Outcome MachineTransactionOutcome `json:"transaction_outcome"`
}

// TemporaryPrecommitValidation must encode and cache the exact full prospective
// success envelope within its output budget, without publishing or mutating the
// result. The cached bytes may be published only after this method succeeds AND
// client cleanup succeeds. It runs once, with prospective committed/completed
// evidence; that evidence is not an acknowledgement until this method returns.
type TemporaryPrecommitValidation func(context.Context, TemporaryAtomicResult) error

// ExecuteTemporaryAtomic never replays SQL. It owns one transaction, preserves
// server read-only policy and requires a validated artifact and a pure encoder.
// Close remains the caller's responsibility, including after committed errors.
func (c *MachineClient) ExecuteTemporaryAtomic(ctx context.Context, script ValidatedTemporaryScript, params [][]byte, options TemporaryAtomicOptions, precommit TemporaryPrecommitValidation) (result TemporaryAtomicResult, err error) {
	result.Outcome = MachineTransactionNotStarted
	result.Execution.Status = MachineExecutionNotStarted
	if !c.temporary {
		return result, &MachineError{Code: "POLICY_DENIED"}
	}
	if !script.validated || len(script.statements) == 0 || script.parameterCount != len(params) || len(params) > 0 && !script.SupportsParameters() || precommit == nil {
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	if script.writes && !script.allowWrite {
		return result, &MachineError{Code: "POLICY_DENIED"}
	}
	if len(script.statements) > 256 {
		return result, &MachineError{Code: "RESOURCE_FAILURE"}
	}
	if options.MaxRows == 0 {
		options.MaxRows = 100
	}
	if options.MaxRows < 1 || options.MaxRows > 1000 || len(params) > 256 || len(options.Schema) > 1<<20 || !utf8.ValidString(options.Schema) || strings.IndexByte(options.Schema, 0) >= 0 {
		return result, &MachineError{Code: "INVALID_INPUT"}
	}
	parameterBytes := 0
	for _, parameter := range params {
		parameterBytes += len(parameter)
		if parameterBytes > 1<<20 || !utf8.Valid(parameter) || strings.IndexByte(string(parameter), 0) >= 0 {
			return result, &MachineError{Code: "INVALID_INPUT"}
		}
	}
	c.operation.Lock()
	defer c.operation.Unlock()
	if ctx.Err() != nil {
		return result, &MachineError{Code: "TIMEOUT"}
	}
	conn := c.conn.PgConn()
	if conn.IsClosed() || conn.TxStatus() != 'I' {
		c.Abort()
		return result, &MachineError{Code: "DATABASE_FAILURE"}
	}
	conn.Frontend().SetMaxBodyLen(machineExecuteMaxBody)
	began, commitAttempted, commitDefinitive := false, false, false
	defer func() {
		if began && result.Outcome != MachineTransactionCommitted && result.Outcome != MachineTransactionRolledBack {
			// A later rollback cannot settle an unacknowledged COMMIT.
			uncertain := commitAttempted && !commitDefinitive
			cleanup, cancel := context.WithTimeout(context.Background(), c.cleanup)
			stop := watchMachine(cleanup, c.Abort)
			tag, rollbackErr := conn.ExecParams(cleanup, "ROLLBACK", nil, nil, nil, nil).Close()
			if !uncertain && rollbackErr == nil && tag.String() == "ROLLBACK" && !conn.IsClosed() && conn.TxStatus() == 'I' {
				result.Outcome = MachineTransactionRolledBack
			} else {
				c.Abort()
				if !commitAttempted {
					result.Outcome = MachineTransactionDisposed
				}
			}
			stop()
			cancel()
		}
		if err != nil {
			result.Results = nil
		}
	}()
	for _, sql := range []string{"SET standard_conforming_strings=on", "SET client_encoding='UTF8'"} {
		if setupErr := c.exec(ctx, sql); setupErr != nil {
			return result, machineFailure(ctx, setupErr, "DATABASE_FAILURE")
		}
	}
	begin := "BEGIN READ ONLY"
	if script.allowWrite {
		begin = "BEGIN"
	}
	began = true
	result.Outcome = MachineTransactionDisposed
	tag, beginErr := conn.ExecParams(ctx, begin, nil, nil, nil, nil).Close()
	if beginErr != nil || tag.String() != "BEGIN" || conn.IsClosed() || conn.TxStatus() != 'T' {
		return result, machineFailure(ctx, beginErr, "DATABASE_FAILURE")
	}
	if options.Schema != "" {
		if setupErr := c.exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{options.Schema}.Sanitize()); setupErr != nil {
			return result, machineFailure(ctx, setupErr, "DATABASE_FAILURE")
		}
	}
	if ctx.Err() != nil {
		return result, &MachineError{Code: "TIMEOUT"}
	}
	result.Results = []MachineStatementResult{}
	result.Execution = MachineExecution{Status: MachineExecutionUnknown, EffectsMayHaveOccurred: true}
	budget, rows := 4096, 0
	consume := func(reader *pgconn.ResultReader) error {
		item, readErr := consumeTemporaryResult(reader, &budget, &rows, options.MaxRows)
		if readErr != nil {
			return readErr
		}
		if len(result.Results) >= 256 || len(result.Results) >= len(script.statements) {
			return &MachineError{Code: "RESOURCE_FAILURE"}
		}
		result.Results = append(result.Results, item)
		if item.Truncated {
			result.Truncated, result.TruncationReason = true, "max_rows"
		}
		return nil
	}
	var executionErr error
	if len(params) == 0 {
		batch := conn.Exec(ctx, script.sql)
		for batch.NextResult() {
			if executionErr = consume(batch.ResultReader()); executionErr != nil {
				// Stop resource/protocol violations rather than drain unchecked data.
				c.Abort()
				break
			}
		}
		terminalErr := batch.Close()
		if executionErr == nil {
			executionErr = terminalErr
		}
	} else {
		reader := conn.ExecParams(ctx, script.sql, params, nil, nil, nil)
		executionErr = consume(reader)
		if executionErr != nil {
			c.Abort()
			_, _ = reader.Close()
		}
	}
	if executionErr != nil || conn.IsClosed() || conn.TxStatus() != 'T' || len(result.Results) != len(script.statements) {
		result.Execution.Status = MachineExecutionFailed
		return result, machineFailure(ctx, executionErr, "DATABASE_FAILURE")
	}
	result.Execution.Status = MachineExecutionCompleted
	prospective := result
	prospective.Execution.Status = MachineExecutionCompleted
	prospective.Outcome = MachineTransactionCommitted
	if ctx.Err() != nil {
		return result, &MachineError{Code: "TIMEOUT"}
	}
	if callbackErr := precommit(ctx, prospective); callbackErr != nil {
		return result, machineFailure(ctx, callbackErr, "RESOURCE_FAILURE")
	}
	if ctx.Err() != nil {
		return result, &MachineError{Code: "TIMEOUT"}
	}
	commitAttempted = true
	result.Outcome = MachineTransactionUnknown
	result.Execution.Status = MachineExecutionUnknown
	tag, commitErr := conn.ExecParams(ctx, "COMMIT", nil, nil, nil, nil).Close()
	if !conn.IsClosed() && conn.TxStatus() == 'I' && tag.String() == "COMMIT" {
		result.Outcome = MachineTransactionCommitted
		result.Execution.Status = MachineExecutionCompleted
		commitDefinitive = true
	} else if !conn.IsClosed() {
		var server *pgconn.PgError
		if tag.String() == "ROLLBACK" && conn.TxStatus() == 'I' || errors.As(commitErr, &server) {
			commitDefinitive = true
			result.Execution.Status = MachineExecutionFailed
			if conn.TxStatus() == 'I' {
				result.Outcome = MachineTransactionRolledBack
			}
		}
	}
	if commitErr != nil || result.Outcome != MachineTransactionCommitted {
		return result, machineFailure(ctx, commitErr, "DATABASE_FAILURE")
	}
	return result, nil
}

func consumeTemporaryResult(reader *pgconn.ResultReader, budget, rows *int, maxRows int) (MachineStatementResult, error) {
	item := MachineStatementResult{Columns: []MachineColumn{}, Rows: [][]*string{}}
	fail := func() (MachineStatementResult, error) { return item, &MachineError{Code: "RESOURCE_FAILURE"} }
	charge := func(value any) bool {
		encoded, err := json.Marshal(value)
		*budget += len(encoded) + 1
		return err == nil && *budget <= 1<<20
	}
	*budget += 128
	fields := reader.FieldDescriptions()
	for _, field := range fields {
		if len(field.Name) > 64<<10 || !utf8.ValidString(field.Name) || field.Format != 0 {
			return fail()
		}
		column := MachineColumn{Name: field.Name, TypeOID: field.DataTypeOID, Encoding: "postgres-text"}
		if !charge(column) {
			return fail()
		}
		item.Columns = append(item.Columns, column)
	}
	for reader.NextRow() {
		values := reader.Values()
		if len(values) != len(fields) {
			return fail()
		}
		for _, value := range values {
			if len(value) > 64<<10 || !utf8.Valid(value) {
				return fail()
			}
		}
		if *rows >= maxRows {
			item.Truncated, item.TruncationReason = true, "max_rows"
			continue
		}
		row := make([]*string, len(values))
		for i, value := range values {
			if value != nil {
				text := string(value)
				row[i] = &text
			}
		}
		if !charge(row) {
			return fail()
		}
		item.Rows = append(item.Rows, row)
		*rows++
	}
	tag, err := reader.Close()
	if err != nil {
		return item, err
	}
	item.CommandTag = tag.String()
	if len(item.CommandTag) > 64<<10 || !utf8.ValidString(item.CommandTag) || !charge(item.CommandTag) {
		return fail()
	}
	if !strings.HasPrefix(item.CommandTag, "SELECT ") && !strings.HasPrefix(item.CommandTag, "INSERT ") && !strings.HasPrefix(item.CommandTag, "UPDATE ") && !strings.HasPrefix(item.CommandTag, "DELETE ") {
		return item, &MachineError{Code: "DATABASE_FAILURE"}
	}
	return item, nil
}
