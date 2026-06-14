package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGetDefaultConfigQueryDefaultStatementTimeout pins the default: the
// default statement timeout is 0, i.e. OFF on a fresh install. 0 means
// "no ceiling" — the streaming run path passes the caller's context
// through unchanged.
func TestGetDefaultConfigQueryDefaultStatementTimeout(t *testing.T) {
	cfg := GetDefaultConfig()
	if cfg.Query.DefaultStatementTimeout != 0 {
		t.Errorf("Query.DefaultStatementTimeout = %v, want 0 (off by default)", cfg.Query.DefaultStatementTimeout)
	}
}

// TestParseYAML_QueryDefaultStatementTimeout asserts the top-level
// `query.default_statement_timeout` YAML path decodes onto
// UserConfig.Query.DefaultStatementTimeout as a Go duration.
func TestParseYAML_QueryDefaultStatementTimeout(t *testing.T) {
	src := []byte("query:\n  default_statement_timeout: 2s\n")
	var cfg UserConfig
	if err := yaml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if cfg.Query.DefaultStatementTimeout.Seconds() != 2 {
		t.Errorf("Query.DefaultStatementTimeout = %v after parsing %q; want 2s",
			cfg.Query.DefaultStatementTimeout, string(src))
	}
}

// TestValidateUserConfig_NegativeStatementTimeout rejects a negative
// default statement timeout. 0 (off) and any positive duration are valid;
// a negative value is a config error.
func TestValidateUserConfig_NegativeStatementTimeout(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Query.DefaultStatementTimeout = -1
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if !containsErrSubstr(errs, "query.default_statement_timeout") {
		t.Fatalf("expected query.default_statement_timeout error for negative value, got %v", errs)
	}
}

// TestValidateUserConfig_ZeroStatementTimeoutValid confirms the off
// sentinel (0) passes validation.
func TestValidateUserConfig_ZeroStatementTimeoutValid(t *testing.T) {
	cfg := GetDefaultConfig()
	cfg.Query.DefaultStatementTimeout = 0
	_, errs := ValidateUserConfig(cfg, fullDeps())
	if containsErrSubstr(errs, "query.default_statement_timeout") {
		t.Fatalf("zero statement timeout must be valid (off), got %v", errs)
	}
}
