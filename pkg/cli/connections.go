//go:build linux || darwin

package cli

import (
	"context"
	"runtime"
	"unicode/utf8"

	"github.com/davesavic/pgsavvy/pkg/models"
)

var connectionUsage = map[string]string{"list": "connections list", "show": "connections show NAME", "test": "connections test NAME"}

func ConnectionsCommand(service InvocationService) Command {
	return func(ctx context.Context, args []string, _ IO) (Envelope, error) {
		bad := &PublicError{Code: InvalidInput}
		if len(args) == 1 && args[0] == "--help" {
			return Envelope{Command: "connections", Data: map[string]any{"commands": connectionUsage}, ResultComplete: true}, nil
		}
		if len(args) == 0 {
			return Envelope{}, bad
		}
		operation := args[0]
		usage, ok := connectionUsage[operation]
		if !ok {
			return Envelope{}, bad
		}
		base := Envelope{Command: "connections " + operation}
		flags, positional, err := ParseFlags(args[1:], nil, []string{"help"})
		if err != nil {
			return base, bad
		}
		if _, help := flags["help"]; help {
			if len(flags) != 1 || len(positional) != 0 {
				return base, bad
			}
			base.Data = map[string]any{"usage": usage}
			base.ResultComplete = true
			return base, nil
		}
		if operation == "list" {
			if len(positional) != 0 {
				return base, bad
			}
			profiles, err := service.Profiles()
			if err != nil {
				return base, err
			}
			items := make([]PublicProfile, 0, len(profiles))
			for _, profile := range profiles {
				items = append(items, PublicConnection(profile))
			}
			base.Data = items
			base.ResultComplete = true
			return base, nil
		}
		if len(positional) != 1 || positional[0] == "" || len(positional[0]) > MaxProfileBytes || !utf8.ValidString(positional[0]) {
			return base, bad
		}
		name := positional[0]
		if operation == "show" {
			profiles, err := service.Profiles()
			if err != nil {
				return base, err
			}
			profile, err := ResolveExactProfile(profiles, name)
			if err != nil {
				return base, err
			}
			base.Connection = profile.Name
			base.Data = PublicConnection(profile)
			base.ResultComplete = true
			return base, nil
		}
		client, profile, err := service.Open(ctx, name, models.CLIAccessMetadata)
		if err != nil {
			return base, err
		}
		base.Connection = profile.Name
		pingErr := client.Ping(ctx)
		closeErr := CloseMachine(ctx, client)
		if pingErr != nil {
			return base, PublicMachineError(pingErr)
		}
		if closeErr != nil {
			return base, closeErr
		}
		base.Data = map[string]any{"connected": true, "read_only": true}
		base.ResultComplete = true
		return base, nil
	}
}

type capabilityCommand struct {
	Command        string `json:"command"`
	Usage          string `json:"usage"`
	RequiredAccess string `json:"required_cli_access"`
}

func queryPolicy() map[string]any {
	return map[string]any{
		"guarded_access":  "row-read or full with read_only=true: SELECT validation and a READ ONLY transaction; columns/rows payload",
		"full_access":     "full with read_only=false: original SQL sent once, native PostgreSQL batches and ordered results with command_tag/columns/rows; no automatic retry",
		"approval":        "configured confirm_writes/confirm_ddl heuristic requires exact typed connection name and yes on controlling terminal, or paired --approve --confirm-connection NAME (exact case and whitespace); flags never elevate access",
		"approval_limits": "both confirmation settings false means no prompt; heuristic can miss opaque SQL and side effects in functions; explicit flags acknowledge caller intent, not human presence",
		"sql_limits":      "no streaming COPY FROM STDIN/TO STDOUT; server-side COPY allowed; parameters only for one statement; results are bounded and drained; unclosed transactions are rolled back with an error",
		"effects":         "errors, history outcomes and command tags do not prove commit or rollback; prepared transactions may survive and require explicit recovery",
	}
}

func CapabilitiesCommand() Command {
	return func(_ context.Context, args []string, _ IO) (Envelope, error) {
		flags, positional, err := ParseFlags(args, nil, []string{"help"})
		if err != nil || len(positional) != 0 || len(flags) > 1 {
			return Envelope{}, &PublicError{Code: InvalidInput}
		}
		return Envelope{Command: "capabilities", ResultComplete: true, Data: capabilityData()}, nil
	}
}

func capabilityData() map[string]any {
	commands := []capabilityCommand{{"connections list", connectionUsage["list"], "none"}, {"connections show", connectionUsage["show"], "none"}, {"connections test", connectionUsage["test"], "metadata"}}
	for _, operation := range []string{"list", "tables", "describe", "relationships"} {
		commands = append(commands, capabilityCommand{"schema " + operation, schemaUsage[operation], "metadata"})
	}
	commands = append(commands, capabilityCommand{"query", queryUsage, "row-read"}, capabilityCommand{"capabilities", "capabilities", "none"}, capabilityCommand{"llm-help", llmHelpUsage, "none"})
	return map[string]any{
		"platform": runtime.GOOS, "format": "json", "commands": commands, "query_read_only": false, "query_policy": queryPolicy(), "catalog_visibility_implies_row_access": false,
		"global_flags": []string{"--format json", "--timeout DURATION"},
		"limits":       map[string]any{"sql_bytes": MaxInputBytes, "parameter_bytes": MaxInputBytes, "parameters": MaxParameters, "profile_selector_bytes": MaxProfileBytes, "row_default": DefaultRows, "row_max": MaxRows, "cell_bytes": MaxCellBytes, "encoded_output_bytes": MaxOutputBytes, "metadata_list_default": 100, "metadata_section_max": 1000, "invocation_seconds": 30, "timeout_max_ms": MaxInvocationTimeout.Milliseconds(), "connection_seconds": 10, "helper_seconds": 5, "helper_stdout_bytes": MaxHelperBytes, "statement_seconds": 10, "lock_seconds": 1, "cleanup_seconds": 2, "publication_seconds": 2},
		"exit_codes":   map[string]int{"success": ExitSuccess, "input": ExitInput, "unavailable": ExitUnavailable, "policy": ExitPolicy, "database": ExitDatabase, "timeout": ExitTimeout, "resource": ExitResource},
	}
}
