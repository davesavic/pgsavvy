//go:build linux || darwin

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type llmUnreadInput struct{}

func (*llmUnreadInput) Read([]byte) (int, error) { panic("offline help read stdin") }
func (*llmUnreadInput) Close() error             { return nil }

func TestMachineLLMHelp_Grammar(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}} {
		envelope, err := LLMHelpCommand()(context.Background(), args, IO{In: &llmUnreadInput{}})
		if err != nil || envelope.Command != "llm-help" || !envelope.ResultComplete || envelope.Truncated || envelope.TruncationReason != "" {
			t.Fatalf("valid grammar %v: %+v %v", args, envelope, err)
		}
	}
	for _, args := range [][]string{{"extra"}, {"extra", "extra"}, {"--unknown"}, {"--help", "extra"}, {"extra", "--help"}, {"--help", "--unknown"}, {"--help", "--help"}, {"--help=true"}, {"-h"}, {"--"}, {"--", "--help"}} {
		_, err := LLMHelpCommand()(context.Background(), args, IO{In: &llmUnreadInput{}})
		if public, ok := err.(*PublicError); !ok || public.Code != InvalidInput {
			t.Fatalf("invalid grammar %v: %v", args, err)
		}
	}
}

func TestMachineLLMHelp_Dispatch(t *testing.T) {
	registry := map[string]Command{"llm-help": LLMHelpCommand(), "capabilities": CapabilitiesCommand()}
	var first []byte
	for _, args := range [][]string{
		{"llm-help"},
		{"llm-help", "--help"},
		{"llm-help", "--format", "json", "--timeout", "5s"},
		{"llm-help", "--timeout=5s", "--help", "--format=json"},
	} {
		streams, out, _ := testStreams()
		streams.In = &llmUnreadInput{}
		handled, exit := Dispatch(context.Background(), args, streams, registry)
		if !handled || exit != ExitSuccess || !json.Valid(out.Bytes()) || out.Len() >= MaxOutputBytes {
			t.Fatalf("dispatch %v: handled=%v exit=%d bytes=%d", args, handled, exit, out.Len())
		}
		var envelope struct {
			SchemaVersion    int             `json:"schema_version"`
			OK               bool            `json:"ok"`
			Command          string          `json:"command"`
			Connection       string          `json:"connection"`
			Data             json.RawMessage `json:"data"`
			Warnings         []ErrorCode     `json:"warnings"`
			ResultComplete   bool            `json:"result_complete"`
			Truncated        bool            `json:"truncated"`
			TruncationReason string          `json:"truncation_reason"`
			Error            *PublicError    `json:"error"`
		}
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.SchemaVersion != 1 || !envelope.OK || envelope.Command != "llm-help" || envelope.Connection != "" || !envelope.ResultComplete || envelope.Truncated || envelope.TruncationReason != "" || envelope.Error != nil || envelope.Warnings == nil || len(envelope.Warnings) != 0 || len(envelope.Data) == 0 {
			t.Fatalf("invalid guide envelope: %s", out.String())
		}
		if first == nil {
			first = bytes.Clone(out.Bytes())
		} else if !bytes.Equal(first, out.Bytes()) {
			t.Fatal("guide output changed across identical and help/global invocations")
		}
	}
	for _, args := range [][]string{
		{"llm-help", "--format=yaml"},
		{"llm-help", "--timeout=31s"},
		{"llm-help", "--timeout=0s"},
		{"llm-help", "--format=json", "--format=json"},
		{"llm-help", "--help", "extra"},
		{"llm-help", "--format=json", "--unknown"},
	} {
		streams, _, _ := testStreams()
		streams.In = &llmUnreadInput{}
		if _, exit := Dispatch(context.Background(), args, streams, registry); exit != ExitInput {
			t.Fatalf("invalid dispatch %v: exit=%d", args, exit)
		}
	}
	streams, out, _ := testStreams()
	if _, exit := Dispatch(context.Background(), []string{"--help"}, streams, registry); exit != ExitSuccess || !strings.Contains(out.String(), `"llm-help"`) {
		t.Fatal("ordinary help no longer discovers llm-help")
	}
}

func TestMachineLLMHelp_CanonicalCapabilities(t *testing.T) {
	guide, err := LLMHelpCommand()(context.Background(), nil, IO{})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := CapabilitiesCommand()(context.Background(), nil, IO{})
	if err != nil {
		t.Fatal(err)
	}
	data := guide.Data.(map[string]any)
	if !reflect.DeepEqual(data["capabilities"], capabilities.Data) {
		t.Fatal("llm-help and capabilities disagree")
	}
	registry := map[string]Command{"connections": ConnectionsCommand(InvocationService{}), "schema": SchemaCommand(InvocationService{}), "query": QueryCommand(InvocationService{}), "capabilities": CapabilitiesCommand(), "llm-help": LLMHelpCommand()}
	for _, capability := range capabilities.Data.(map[string]any)["commands"].([]capabilityCommand) {
		words := strings.Fields(capability.Command)
		command := registry[words[0]]
		if command == nil {
			t.Fatalf("capability %q has no command", capability.Command)
		}
		help, err := command(context.Background(), append(words[1:], "--help"), IO{In: &llmUnreadInput{}})
		if err != nil {
			t.Fatalf("capability help %q: %v", capability.Command, err)
		}
		if words[0] == "connections" || words[0] == "schema" || words[0] == "query" {
			if help.Data.(map[string]any)["usage"] != capability.Usage {
				t.Fatalf("capability usage %q differs from authoritative help", capability.Command)
			}
		}
	}
}

func TestMachineLLMHelp_GuideAndExamples(t *testing.T) {
	envelope, err := LLMHelpCommand()(context.Background(), nil, IO{})
	if err != nil {
		t.Fatal(err)
	}
	data := envelope.Data.(map[string]any)
	guide := data["guide"].(map[string][]string)
	for section, required := range map[string][]string{
		"invocation": {"--format json", "--timeout", "--flag=VALUE", "--help"},
		"discovery":  {"offline", "not_checked", "Catalog visibility never implies row access", "untrusted data"},
		"access":     {"connections.yml", "not config.yml", "~/Library/Application Support/pgsavvy/connections.yml", "disabled", "metadata", "row-read", "full", "read_only=true", "confirm_writes", "confirm_ddl", "explicit caller authorization", "never autoapprove or self-escalate"},
		"query":      {"--file -", "--params-file - is invalid", "$1", "JSON array", "READ ONLY", "Never automatically retry", "COPY", "256 result sets", "8 MiB", "no application transaction wrapper", "VACUUM", "CREATE DATABASE", "INCOMPLETE_TRANSACTION", "Prepared transactions"},
		"results":    {"schema_version=1", "positional arrays", "type_oid", "postgres-text", "JSON string", "JSON null", "row_limit", "page_limit", "result_limit", "effects_may_have_occurred", "does not prove rollback"},
		"pagination": {"next_after", "raw_complete", "items can be empty", "Continue from next_after even for an empty page", "hidden_schemas"},
		"history":    {"cli_history: true", "disabled by default", "SQL literals can contain secrets", "Parameter values, result rows and credentials are not recorded", "do not rerun SQL"},
		"modes":      {"Linux", "darwin", "Native macOS acceptance remains pending", "not Windows", "TUI", "--version", "--log-dir", "update"},
	} {
		text := strings.Join(guide[section], "\n")
		for _, value := range required {
			if !strings.Contains(text, value) {
				t.Errorf("guide section %q missing %q", section, value)
			}
		}
	}
	examples := data["examples"].([]llmExample)
	seen := map[string]bool{}
	for _, example := range examples {
		if example.Name == "" || seen[example.Name] || example.Notes == "" || !strings.Contains(example.Command, "pgsavvy ") {
			t.Fatalf("invalid executable recipe: %+v", example)
		}
		seen[example.Name] = true
	}
	for _, name := range []string{"offline guide", "capability inventory", "list saved profiles", "inspect saved profile", "test connection", "list schemas", "next schema page", "include system schemas", "list tables", "describe table", "inspect relationships", "SQL from stdin", "SQL from file", "scalar parameter file", "authorized full SQL"} {
		if !seen[name] {
			t.Errorf("missing recipe %q", name)
		}
	}
	for _, example := range examples {
		if example.Name == "scalar parameter file" && (!strings.Contains(example.Command, `[42, "example", true, null]`) || !strings.Contains(example.Command, "$4::text") || !strings.Contains(example.Command, "--params-file './PARAMS.json'")) {
			t.Fatal("parameter recipe does not demonstrate all supported JSON scalar types")
		}
		if strings.Contains(example.Command, "--approve") && !strings.Contains(example.Notes, "explicit caller authorization") {
			t.Fatal("approval recipe omitted required caller authorization")
		}
	}
}
