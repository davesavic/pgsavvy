//go:build linux || darwin

package cli

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func QueryCommand(service InvocationService) Command {
	return func(ctx context.Context, args []string, streams IO) (envelope Envelope, resultErr error) {
		flags, positional, err := ParseFlags(args, []string{"connection", "file", "params-file", "max-rows", "schema"}, []string{"help"})
		if err != nil || len(positional) != 0 {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		if _, help := flags["help"]; help {
			if len(flags) != 1 {
				return Envelope{}, &PublicError{Code: InvalidInput}
			}
			return Envelope{Command: "query", Data: map[string]any{"usage": "query --connection NAME --file PATH|- [--params-file PATH] [--max-rows 1..1000] [--schema NAME]", "read_only": true}, ResultComplete: true}, nil
		}
		name, file := flags["connection"], flags["file"]
		if name == "" || file == "" {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		if !utf8.ValidString(flags["schema"]) {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		rows := DefaultRows
		if raw, ok := flags["max-rows"]; ok {
			rows, err = strconv.Atoi(raw)
			if err != nil || rows < 1 || rows > MaxRows {
				return Envelope{}, &PublicError{Code: InvalidInput}
			}
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
		if validationErr != nil {
			return Envelope{}, PublicMachineError(validationErr)
		}
		parameters := [][]byte{}
		if path, ok := flags["params-file"]; ok {
			parameters, err = ReadParameters(ctx, path)
			if err != nil {
				return Envelope{}, err
			}
		}
		var client *pg.MachineClient
		var profile models.Connection
		if selected != nil {
			client, profile, err = service.openProfile(ctx, *selected, models.CLIAccessRowRead)
		} else {
			client, profile, err = service.Open(ctx, name, models.CLIAccessRowRead)
		}
		if err != nil {
			return Envelope{}, err
		}
		result, queryErr := client.QueryReadOnly(ctx, pg.MachineQuery{SQL: sql, Parameters: parameters, MaxRows: rows, Schema: flags["schema"]})
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
