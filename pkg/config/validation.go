package config

import (
	"fmt"
	"strings"
)

// ValidationDeps lets callers inject domain knowledge into
// ValidateUserConfig without pkg/config taking on a pkg/gui/* import.
// Either predicate may be nil; a nil predicate is treated as "always
// returns false" so a zero-value ValidationDeps is acceptable for a
// parse-only validation pass.
type ValidationDeps struct {
	ActionExists func(string) bool
	ScopeExists  func(string) bool
}

// maxKeybindings caps the number of accepted keybinding entries. The
// limit guards against pathological user configs blowing up the trie or
// validation work.
const maxKeybindings = 10000

// allowedModeTokens is the set of mode tokens accepted in
// KeybindingConfig.Mode. The literal multi-char token "<c-v>" is allowed
// for visual-block per the dbsavvy-dlp design.
var allowedModeTokens = map[string]struct{}{
	"n": {}, "i": {}, "v": {}, "V": {}, "<c-v>": {}, "o": {}, "x": {}, "c": {},
}

// ValidateUserConfig validates cfg against the rules in dbsavvy-dlp.3.
//
// It returns two slices: warnings are non-fatal advisories (e.g. missing
// help.cheatsheet binding); errors are hard failures the caller should
// surface. Both slices are nil for a clean validation.
func ValidateUserConfig(cfg *UserConfig, deps ValidationDeps) (warnings []string, errors []error) {
	if cfg == nil {
		return nil, []error{fmt.Errorf("config: nil UserConfig")}
	}
	actionExists := deps.ActionExists
	if actionExists == nil {
		actionExists = func(string) bool { return false }
	}
	scopeExists := deps.ScopeExists
	if scopeExists == nil {
		scopeExists = func(string) bool { return false }
	}

	if len(cfg.Keybindings) > maxKeybindings {
		return nil, []error{fmt.Errorf("config: too many keybindings: %d (max %d)", len(cfg.Keybindings), maxKeybindings)}
	}

	var errs []error
	var warns []string

	type dupKey struct{ mode, scope, key string }
	dupSeen := map[dupKey]int{} // count of non-<nop> bindings at this key
	dupReported := map[dupKey]bool{}
	hasCheatsheet := false
	hasQuit := false

	for i, kb := range cfg.Keybindings {
		origin := formatOrigin(kb)

		// Mode tokens.
		modeTokens := splitTrim(kb.Mode, ",")
		if len(modeTokens) == 0 {
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: mode is empty", i, origin))
		}
		for _, tok := range modeTokens {
			if tok == "" {
				errs = append(errs, fmt.Errorf("keybindings[%d]%s: mode has empty token", i, origin))
				continue
			}
			if _, ok := allowedModeTokens[tok]; !ok {
				errs = append(errs, fmt.Errorf("keybindings[%d]%s: unknown mode token %q", i, origin, tok))
			}
		}

		// Key required.
		if kb.Key == "" {
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: key is empty", i, origin))
		} else if _, err := ParseKeySequence(kb.Key); err != nil {
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: key %q: %w", i, origin, kb.Key, err))
		}

		// Action XOR Command.
		switch {
		case kb.Action == "" && kb.Command == "":
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: must set exactly one of action or command", i, origin))
		case kb.Action != "" && kb.Command != "":
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: action and command are mutually exclusive", i, origin))
		case kb.Action != "":
			if kb.Action != "<nop>" && !actionExists(kb.Action) {
				errs = append(errs, fmt.Errorf("keybindings[%d]%s: unknown action %q", i, origin, kb.Action))
			}
		}

		// Scope.
		if !scopeExists(kb.Scope) {
			errs = append(errs, fmt.Errorf("keybindings[%d]%s: unknown scope %q", i, origin, kb.Scope))
		}

		// Cross-binding bookkeeping.
		if kb.Action == "help.cheatsheet" {
			hasCheatsheet = true
		}
		if kb.Action == "app.quit" {
			hasQuit = true
		}

		// Duplicate detection: count non-<nop> entries per (mode, scope, key).
		// One <nop> + one non-<nop> on the same key is NOT a duplicate.
		if kb.Action != "<nop>" && kb.Key != "" {
			for _, m := range modeTokens {
				k := dupKey{mode: m, scope: kb.Scope, key: kb.Key}
				dupSeen[k]++
				if dupSeen[k] > 1 && !dupReported[k] {
					errs = append(errs, fmt.Errorf("keybindings: duplicate binding for (mode=%s, scope=%s, key=%s)", m, kb.Scope, kb.Key))
					dupReported[k] = true
				}
			}
		}
	}

	if !hasCheatsheet {
		warns = append(warns, "no binding for help.cheatsheet")
	}
	if !hasQuit {
		warns = append(warns, "no binding for app.quit")
	}

	// Leader: a single bare digit 0..9 is invalid (would clash with vim
	// count prefixes).
	if cfg.Leader != "" {
		if lbl, err := ParseKeyLabel(cfg.Leader); err == nil {
			if len(lbl.Mods) == 0 && len(lbl.Key) == 1 {
				r := lbl.Key[0]
				if r >= '0' && r <= '9' {
					errs = append(errs, fmt.Errorf("config: leader %q is a digit; digits are reserved for vim count prefixes", cfg.Leader))
				}
			}
		}
	}

	return warns, errs
}

func splitTrim(s, sep string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func formatOrigin(kb KeybindingConfig) string {
	if kb.OriginFile == "" && kb.OriginLine == 0 {
		return ""
	}
	if kb.OriginLine == 0 {
		return fmt.Sprintf(" (%s)", kb.OriginFile)
	}
	return fmt.Sprintf(" (%s:%d)", kb.OriginFile, kb.OriginLine)
}
