package config

import "testing"

func TestValidateUserConfig_DefaultsValid(t *testing.T) {
	errs := ValidateUserConfig(GetDefaultConfig())
	if len(errs) != 0 {
		t.Errorf("defaults should validate; got %v", errs)
	}
}

func TestValidateUserConfig_InvalidLabel(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "normal", Sequence: []string{"<bogus"}, ActionID: "x"},
	}
	errs := ValidateUserConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected at least one error")
	}
}

func TestValidateUserConfig_NilConfig(t *testing.T) {
	errs := ValidateUserConfig(nil)
	if len(errs) == 0 {
		t.Fatal("expected error for nil config")
	}
}

func TestValidateUserConfig_MultipleErrors(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Keybindings = []KeybindingConfig{
		{Mode: "normal", Sequence: []string{"<bogus", "<f13>"}, ActionID: "x"},
		{Mode: "normal", Sequence: []string{"j"}, ActionID: "y"},
	}
	errs := ValidateUserConfig(cfg)
	if len(errs) != 2 {
		t.Errorf("expected 2 errors, got %d: %v", len(errs), errs)
	}
}
