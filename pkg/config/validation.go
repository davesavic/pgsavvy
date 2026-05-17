package config

import "fmt"

// ValidateUserConfig checks the keybinding labels in cfg by running each
// Sequence entry through ParseKeyLabel. It returns a slice of errors (one
// per offending label); a nil or empty slice means the config is valid.
// Action-ID, scope and color validation are intentionally deferred to the
// owning domains (per D-CFG1).
func ValidateUserConfig(cfg *UserConfig) []error {
	if cfg == nil {
		return []error{fmt.Errorf("config: nil UserConfig")}
	}
	var errs []error
	for i, kb := range cfg.Keybindings {
		for j, label := range kb.Sequence {
			if _, err := ParseKeyLabel(label); err != nil {
				errs = append(errs, fmt.Errorf("keybindings[%d].sequence[%d]: %w", i, j, err))
			}
		}
	}
	return errs
}
