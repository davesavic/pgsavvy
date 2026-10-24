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
	for _, policy := range []CLIAccess{CLIAccessMetadata, CLIAccessRowRead, "full"} {
		if got := (Connection{CLIAccess: policy}).EffectiveCLIAccess(); got != policy {
			t.Fatalf("policy %q became %q", policy, got)
		}
	}
}

func TestMachineAccess_Validation(t *testing.T) {
	for _, value := range []string{`""`, "disabled", "metadata", "row-read", "full", "null", "~"} {
		var c Connection
		if err := yaml.Unmarshal([]byte("cli_access: "+value), &c); err != nil {
			t.Errorf("valid %s: %v", value, err)
		}
	}
	for _, value := range []string{"true", "1", "[]", "{}", "ROW-READ", "FULL", "read-write", `" metadata"`} {
		var c Connection
		if err := yaml.Unmarshal([]byte("cli_access: "+value), &c); err == nil {
			t.Errorf("invalid %s accepted", value)
		}
	}
	if err := CLIAccess("invalid").Validate(); err == nil {
		t.Fatal("invalid in-memory policy accepted")
	}
}

func TestMachineAccess_Capabilities(t *testing.T) {
	if CLIAccessFull != "full" {
		t.Fatal("persisted full policy changed")
	}
	requirements := []CLIAccess{"", CLIAccessDisabled, CLIAccessMetadata, CLIAccessRowRead, CLIAccessFull, "invalid", "FULL"}
	for _, tc := range []struct {
		access  CLIAccess
		allowed [7]bool
	}{
		{"", [7]bool{}},
		{CLIAccessDisabled, [7]bool{}},
		{CLIAccessMetadata, [7]bool{true, false, true}},
		{CLIAccessRowRead, [7]bool{true, false, true, true}},
		{CLIAccessFull, [7]bool{true, false, true, true, true}},
		{"invalid", [7]bool{}},
		{"FULL", [7]bool{}},
	} {
		for index, required := range requirements {
			if got := tc.access.AllowsCLIAccess(required); got != tc.allowed[index] {
				t.Errorf("access %q required %q: got %v want %v", tc.access, required, got, tc.allowed[index])
			}
		}
	}
}
