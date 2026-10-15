package models

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMachineAccess_Defaults(t *testing.T) {
	for _, policy := range []CLIAccess{"", CLIAccessDisabled, "invalid"} {
		if got := (Connection{CLIAccess: policy}).EffectiveCLIAccess(); got != CLIAccessDisabled {
			t.Fatalf("policy %q grants %q", policy, got)
		}
	}
	for _, policy := range []CLIAccess{CLIAccessMetadata, CLIAccessRowRead} {
		if got := (Connection{CLIAccess: policy}).EffectiveCLIAccess(); got != policy {
			t.Fatalf("policy %q became %q", policy, got)
		}
	}
}

func TestMachineAccess_Validation(t *testing.T) {
	for _, value := range []string{`""`, "disabled", "metadata", "row-read", "null", "~"} {
		var c Connection
		if err := yaml.Unmarshal([]byte("cli_access: "+value), &c); err != nil {
			t.Errorf("valid %s: %v", value, err)
		}
	}
	for _, value := range []string{"true", "1", "[]", "{}", "ROW-READ", "read-write", `" metadata"`} {
		var c Connection
		if err := yaml.Unmarshal([]byte("cli_access: "+value), &c); err == nil {
			t.Errorf("invalid %s accepted", value)
		}
	}
	if err := CLIAccess("invalid").Validate(); err == nil {
		t.Fatal("invalid in-memory policy accepted")
	}
}
