//go:build linux || darwin

package cli

import (
	"context"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

var schemaUsage = map[string]string{
	"list":          "schema list --connection NAME [--limit 1..1000] [--after NAME] [--include-system]",
	"tables":        "schema tables --connection NAME --schema NAME [--limit 1..1000] [--after NAME]",
	"describe":      "schema describe --connection NAME --schema NAME --table NAME",
	"relationships": "schema relationships --connection NAME --schema NAME --table NAME",
}

func SchemaCommand(service InvocationService) Command {
	return func(ctx context.Context, args []string, _ IO) (Envelope, error) {
		bad := &PublicError{Code: InvalidInput}
		if len(args) == 1 && args[0] == "--help" {
			return Envelope{Command: "schema", Data: map[string]any{"commands": schemaUsage}, ResultComplete: true}, nil
		}
		if len(args) == 0 {
			return Envelope{}, bad
		}
		operation := args[0]
		usage, known := schemaUsage[operation]
		if !known {
			return Envelope{}, bad
		}
		base := Envelope{Command: "schema " + operation}
		values, switches := []string{"connection"}, []string{"help"}
		switch operation {
		case "list":
			values = append(values, "limit", "after")
			switches = append(switches, "include-system")
		case "tables":
			values = append(values, "schema", "limit", "after")
		default:
			values = append(values, "schema", "table")
		}
		flags, positional, err := ParseFlags(args[1:], values, switches)
		if err != nil || len(positional) != 0 {
			return base, bad
		}
		if _, help := flags["help"]; help {
			if len(flags) != 1 {
				return base, bad
			}
			base.Data = map[string]any{"usage": usage}
			base.ResultComplete = true
			return base, nil
		}
		name := flags["connection"]
		if name == "" {
			return base, bad
		}
		for _, key := range []string{"schema", "table", "after"} {
			if value := flags[key]; !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > 64<<10 {
				return base, bad
			}
		}
		request := pg.MachineMetadataRequest{Schema: flags["schema"], Table: flags["table"], After: flags["after"]}
		switch operation {
		case "list":
			request.Operation = pg.MachineSchemas
		case "tables":
			request.Operation = pg.MachineTables
			if request.Schema == "" {
				return base, bad
			}
		case "describe":
			request.Operation = pg.MachineDescribe
		case "relationships":
			request.Operation = pg.MachineRelationships
		}
		if operation == "describe" || operation == "relationships" {
			if request.Schema == "" || request.Table == "" {
				return base, bad
			}
		} else {
			request.Limit = 100
			if raw, ok := flags["limit"]; ok {
				request.Limit, err = strconv.Atoi(raw)
				if err != nil || request.Limit < 1 || request.Limit > 1000 {
					return base, bad
				}
			}
		}
		client, profile, err := service.Open(ctx, name, models.CLIAccessMetadata)
		if err != nil {
			return base, err
		}
		base.Connection = profile.Name
		result, inspectErr := client.Inspect(ctx, request)
		closeErr := CloseMachine(ctx, client)
		if inspectErr != nil {
			return base, PublicMachineError(inspectErr)
		}
		if closeErr != nil {
			return base, closeErr
		}
		base.ResultComplete = true
		switch operation {
		case "list":
			_, includeSystem := flags["include-system"]
			page, warnings := visibleSchemaPage(*result.Schemas, includeSystem, profile.HiddenSchemas)
			base.Data = page
			base.Warnings = warnings
			base.ResultComplete = page.RawComplete
		case "tables":
			base.Data = result.Tables
			base.ResultComplete = result.Tables.RawComplete
		case "describe":
			base.Data = publicDescription(*result.Description)
		case "relationships":
			base.Data = result.Relationships
		}
		if !base.ResultComplete {
			base.Truncated = true
			base.TruncationReason = "page_limit"
		}
		return base, nil
	}
}

func visibleSchemaPage(raw pg.MachineSchemaPage, includeSystem bool, profilePatterns []string) (pg.MachineSchemaPage, []ErrorCode) {
	patterns := make([]string, 0, len(profilePatterns)+len(pg.BuiltinHiddenSchemas))
	warnings := []ErrorCode{}
	if !includeSystem {
		patterns = append(patterns, pg.BuiltinHiddenSchemas...)
	}
	for _, pattern := range profilePatterns {
		if _, err := path.Match(pattern, ""); err != nil {
			if len(warnings) == 0 {
				warnings = append(warnings, InvalidInput)
			}
			continue
		}
		patterns = append(patterns, pattern)
	}
	visible := make([]pg.MachineSchema, 0, len(raw.Items))
	for _, item := range raw.Items {
		hidden := false
		for _, pattern := range patterns {
			matched, err := path.Match(pattern, item.Name)
			if err == nil && matched {
				hidden = true
				break
			}
		}
		if !hidden {
			visible = append(visible, item)
		}
	}
	raw.Items = visible
	return raw, warnings
}

func publicDescription(raw pg.MachineDescription) pg.MachineDescription {
	result := raw
	result.Constraints = append([]pg.MachineConstraint{}, raw.Constraints...)
	labels := map[string]string{"p": "PRIMARY KEY", "u": "UNIQUE", "f": "FOREIGN KEY", "c": "CHECK", "n": "NOT NULL", "x": "EXCLUDE", "t": "CONSTRAINT TRIGGER"}
	for i := range result.Constraints {
		if label, ok := labels[result.Constraints[i].Kind]; ok {
			result.Constraints[i].Kind = label
		}
	}
	return result
}
