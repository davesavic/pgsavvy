//go:build linux || darwin

package cli

import (
	"context"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func temporaryQueryEnvelope(base Envelope, result pg.TemporaryAtomicResult) Envelope {
	base.Execution = &executionMetadata{Status: string(result.Execution.Status), EffectsMayHaveOccurred: result.Execution.EffectsMayHaveOccurred}
	base.TransactionOutcome = string(result.Outcome)
	// Execution and aggregate truncation are excluded from the driver's JSON.
	results := append([]pg.MachineStatementResult{}, result.Results...)
	for i := range results {
		results[i].TruncationReason = publicQueryTruncation(results[i].TruncationReason)
	}
	base.Data = struct {
		Results []pg.MachineStatementResult `json:"results"`
	}{results}
	base.ResultComplete, base.Truncated = !result.Truncated, result.Truncated
	base.TruncationReason = publicQueryTruncation(result.TruncationReason)
	return base
}

func queryTemporary(ctx context.Context, service InvocationService, target session.TemporaryTarget, flags map[string]string, rows int, streams IO) (Envelope, error) {
	_, allowWrite := flags["allow-write"]
	base := Envelope{Command: "query", Target: publicTemporaryTarget(target, !allowWrite), Execution: &executionMetadata{Status: "not_started"}, TransactionOutcome: "not_started"}
	sql, err := ReadSQL(ctx, flags["file"], streams.In)
	if err != nil {
		return base, err
	}
	parameters := [][]byte{}
	if path, present := flags["params-file"]; present {
		parameters, err = ReadParameters(ctx, path)
		if err != nil {
			return base, err
		}
	}
	script, err := pg.ValidateTemporaryScript(ctx, sql, len(parameters), allowWrite)
	if err != nil {
		return base, PublicMachineError(err)
	}
	client, err := service.openTemporaryTarget(ctx, target)
	if err != nil {
		return base, err
	}
	var cached []byte
	result, executionErr := client.ExecuteTemporaryAtomic(ctx, script, parameters, pg.TemporaryAtomicOptions{MaxRows: rows, Schema: flags["schema"]}, func(ctx context.Context, prospective pg.TemporaryAtomicResult) error {
		envelope := temporaryQueryEnvelope(base, prospective)
		envelope.OK = true
		var encodeErr error
		cached, encodeErr = encodeContext(ctx, envelope)
		return encodeErr
	})
	base = temporaryQueryEnvelope(base, result)
	closeErr := CloseMachine(ctx, client)
	if executionErr != nil {
		return base, PublicMachineError(executionErr)
	}
	if closeErr != nil {
		return base, closeErr
	}
	if len(cached) == 0 || result.Outcome != pg.MachineTransactionCommitted {
		return base, &PublicError{Code: ResourceFailure}
	}
	base.cachedJSON = cached
	return base, nil
}
