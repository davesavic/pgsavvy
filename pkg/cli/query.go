//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

const queryUsage = "query " + targetUsage + " --file PATH|- [--params-file PATH] [--max-rows 1..1000] [--schema NAME] [--approve --confirm-connection NAME (saved only)] [--allow-write (temporary only)]"

func QueryCommand(service InvocationService) Command {
	return func(ctx context.Context, args []string, streams IO) (envelope Envelope, resultErr error) {
		flags, positional, err := ParseFlags(args, append([]string{"connection", "file", "params-file", "max-rows", "schema", "confirm-connection"}, temporaryTargetFlags...), []string{"help", "approve", "temporary", "allow-write"})
		if err != nil || len(positional) != 0 {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		if _, help := flags["help"]; help {
			if len(flags) != 1 {
				return Envelope{}, &PublicError{Code: InvalidInput}
			}
			return Envelope{Command: "query", Data: map[string]any{"usage": queryUsage, "read_only": false, "policy": queryPolicy()}, ResultComplete: true}, nil
		}
		target, err := selectTemporaryTarget(flags, true)
		if err != nil {
			return Envelope{}, err
		}
		if target != nil {
			_, allowWrite := flags["allow-write"]
			envelope = Envelope{Command: "query", Target: publicTemporaryTarget(*target, !allowWrite), Execution: &executionMetadata{Status: "not_started"}, TransactionOutcome: "not_started"}
		}
		_, approved := flags["approve"]
		_, confirmed := flags["confirm-connection"]
		if approved != confirmed {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		name, file := flags["connection"], flags["file"]
		if target == nil && name == "" || file == "" {
			return envelope, &PublicError{Code: InvalidInput}
		}
		if len(flags["schema"]) > MaxInputBytes || !utf8.ValidString(flags["schema"]) || strings.IndexByte(flags["schema"], 0) >= 0 {
			return envelope, &PublicError{Code: InvalidInput}
		}
		rows := DefaultRows
		if raw, ok := flags["max-rows"]; ok {
			rows, err = strconv.Atoi(raw)
			if err != nil || rows < 1 || rows > MaxRows {
				return envelope, &PublicError{Code: InvalidInput}
			}
		}
		if target != nil {
			return queryTemporary(ctx, service, *target, flags, rows, streams)
		}
		sql, err := ReadSQL(ctx, file, streams.In)
		if err != nil {
			return Envelope{}, err
		}
		started := time.Now()
		validationErr := pg.ValidateMachineQuery(ctx, sql)
		attempt, selected := service.beginQueryHistory(ctx, name, sql, started)
		if attempt != nil {
			defer func() {
				attempt.complete(ctx, resultErr)
				if slot, ok := ctx.Value(commandFinalizationKey{}).(*commandFinalization); ok {
					slot.run = attempt.finalize
				} else {
					cutoff, _ := ctx.Deadline()
					envelope.Warnings = append(envelope.Warnings, attempt.finalize(cutoff)...)
				}
			}()
		}
		if selected == nil {
			profiles, loadErr := service.Profiles()
			profile, resolveErr := ResolveExactProfile(profiles, name)
			if loadErr != nil || resolveErr != nil {
				// Preserve guarded SQL rejection even when consent/profile lookup fails.
				if validationErr != nil {
					return envelope, PublicMachineError(validationErr)
				}
				if loadErr != nil {
					return envelope, loadErr
				}
				return envelope, resolveErr
			}
			selected = &profile
		}
		full := selected.EffectiveCLIAccess() == models.CLIAccessFull && !selected.ReadOnly
		if full {
			envelope.Execution = &executionMetadata{Status: "not_started"}
		}
		if approved && flags["confirm-connection"] != name {
			return envelope, &PublicError{Code: ApprovalMismatch}
		}
		if !full && validationErr != nil {
			return envelope, PublicMachineError(validationErr)
		}
		parameters := [][]byte{}
		if path, ok := flags["params-file"]; ok {
			parameters, err = ReadParameters(ctx, path)
			if err != nil {
				return envelope, err
			}
		}
		request := pg.MachineQuery{SQL: sql, Parameters: parameters, MaxRows: rows, Schema: flags["schema"]}
		required := models.CLIAccessRowRead
		if full {
			required = models.CLIAccessFull
			for _, parameter := range parameters {
				if strings.IndexByte(string(parameter), 0) >= 0 {
					return envelope, &PublicError{Code: InvalidInput}
				}
			}
			analysis, analysisErr := query.AnalyzeCLIStatements(ctx, sql, len(parameters), selected.ConfirmWrites, selected.ConfirmDDL)
			if analysisErr != nil {
				if ctx.Err() != nil {
					return envelope, &PublicError{Code: Timeout}
				}
				code := UnsupportedSQL
				if errors.Is(analysisErr, query.ErrInvalidCLIInput) {
					code = InvalidInput
				}
				return envelope, &PublicError{Code: code}
			}
			if !approved && (analysis.WritesApprovalRequired || analysis.DDLApprovalRequired) {
				if service.Approve == nil {
					return envelope, &PublicError{Code: ApprovalRequired}
				}
				approvalErr := service.Approve(ctx, ApprovalRequest{Connection: selected.Name, SQL: sql, Schema: request.Schema, MaxRows: rows, ParameterCount: len(parameters), StatementCount: len(analysis.Statements), WritesApprovalRequired: analysis.WritesApprovalRequired, DDLApprovalRequired: analysis.DDLApprovalRequired})
				if ctx.Err() != nil {
					return envelope, &PublicError{Code: Timeout}
				}
				if approvalErr != nil {
					code := safeError(approvalErr).Code
					switch code {
					case ApprovalRequired, ApprovalMismatch, ApprovalUnavailable, ApprovalCancelled, Timeout:
					default:
						code = ApprovalUnavailable
					}
					return envelope, &PublicError{Code: code}
				}
			}
		}
		client, profile, err := service.openProfile(ctx, *selected, required)
		if err != nil {
			return envelope, err
		}
		envelope.Command, envelope.Connection = "query", profile.Name
		if full {
			result, queryErr := client.Execute(ctx, request)
			envelope.Execution = &executionMetadata{Status: string(result.Execution.Status), EffectsMayHaveOccurred: result.Execution.EffectsMayHaveOccurred, PreparedTransactionsMayRemain: result.Execution.PreparedTransactionsMayRemain}
			closeErr := CloseMachine(ctx, client)
			if queryErr != nil {
				return envelope, PublicMachineError(queryErr)
			}
			if closeErr != nil {
				return envelope, closeErr
			}
			for i := range result.Results {
				result.Results[i].TruncationReason = publicQueryTruncation(result.Results[i].TruncationReason)
			}
			envelope.Data, envelope.ResultComplete, envelope.Truncated = result, !result.Truncated, result.Truncated
			envelope.TruncationReason = publicQueryTruncation(result.TruncationReason)
			return envelope, nil
		}
		result, queryErr := client.QueryReadOnly(ctx, request)
		closeErr := CloseMachine(ctx, client)
		if queryErr != nil {
			return Envelope{}, PublicMachineError(queryErr)
		}
		if closeErr != nil {
			return Envelope{}, closeErr
		}
		envelope = Envelope{Command: "query", Connection: profile.Name, Data: result, ResultComplete: !result.Truncated, Truncated: result.Truncated}
		if result.Truncated {
			envelope.TruncationReason = "row_limit"
		}
		return envelope, nil
	}
}

func publicQueryTruncation(reason string) string {
	switch reason {
	case "max_rows":
		return "row_limit"
	case "max_results":
		return "result_limit"
	default:
		return reason
	}
}
