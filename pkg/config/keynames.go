package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// KeyLabel is the parsed form of a single key label (e.g. "j", "<c-a>",
// "<leader>"). Mods are canonicalised modifier names ("ctrl", "alt",
// "shift", "meta") in a stable order. Key is the lowercase special name for
// bracketed labels (e.g. "leader", "esc", "f1") or the literal rune string
// for bare-rune labels.
type KeyLabel struct {
	Mods []string
	Key  string
}

var modAliases = map[string]string{
	"c": "ctrl", "ctrl": "ctrl", "control": "ctrl",
	"a": "alt", "alt": "alt", "opt": "alt", "option": "alt",
	"s": "shift", "shift": "shift",
	"m": "meta", "meta": "meta", "cmd": "meta", "super": "meta", "win": "meta",
}

// modOrder gives a stable ordering for canonical modifier names regardless
// of the order they appear in the label.
var modOrder = map[string]int{"ctrl": 0, "alt": 1, "shift": 2, "meta": 3}

var specialNames = map[string]struct{}{
	"leader": {}, "esc": {}, "cr": {}, "enter": {}, "tab": {}, "bs": {},
	"backspace": {}, "space": {}, "up": {}, "down": {}, "left": {}, "right": {},
	"home": {}, "end": {}, "pgup": {}, "pgdn": {}, "del": {}, "ins": {},
	"f1": {}, "f2": {}, "f3": {}, "f4": {}, "f5": {}, "f6": {},
	"f7": {}, "f8": {}, "f9": {}, "f10": {}, "f11": {}, "f12": {},
}

// ParseKeyLabel parses a single raw key label. It does not interpret
// sequences/chords — that is the keybinding service's responsibility.
func ParseKeyLabel(label string) (KeyLabel, error) {
	if label == "" {
		return KeyLabel{}, errors.New("keyname: empty label")
	}
	if label[0] != '<' {
		if strings.ContainsRune(label, '<') || strings.ContainsRune(label, '>') {
			return KeyLabel{}, fmt.Errorf("keyname: stray angle bracket in %q", label)
		}
		if utf8.RuneCountInString(label) != 1 {
			return KeyLabel{}, fmt.Errorf("keyname: bare label %q must be a single rune", label)
		}
		return KeyLabel{Key: label}, nil
	}
	if !strings.HasSuffix(label, ">") {
		return KeyLabel{}, fmt.Errorf("keyname: unclosed bracket in %q", label)
	}
	inner := label[1 : len(label)-1]
	if inner == "" {
		return KeyLabel{}, fmt.Errorf("keyname: empty bracket in %q", label)
	}
	parts := strings.Split(inner, "-")
	if len(parts) == 1 {
		name := strings.ToLower(parts[0])
		if _, ok := specialNames[name]; !ok {
			return KeyLabel{}, fmt.Errorf("keyname: unknown special %q", label)
		}
		return KeyLabel{Key: name}, nil
	}
	mods := make([]string, 0, len(parts)-1)
	seen := map[string]struct{}{}
	for _, m := range parts[:len(parts)-1] {
		if m == "" {
			return KeyLabel{}, fmt.Errorf("keyname: empty modifier in %q", label)
		}
		canon, ok := modAliases[strings.ToLower(m)]
		if !ok {
			return KeyLabel{}, fmt.Errorf("keyname: unknown modifier %q in %q", m, label)
		}
		if _, dup := seen[canon]; dup {
			continue
		}
		seen[canon] = struct{}{}
		mods = append(mods, canon)
	}
	key := parts[len(parts)-1]
	if key == "" {
		return KeyLabel{}, fmt.Errorf("keyname: modifier without key in %q", label)
	}
	if utf8.RuneCountInString(key) != 1 {
		lower := strings.ToLower(key)
		if _, ok := specialNames[lower]; !ok {
			return KeyLabel{}, fmt.Errorf("keyname: unknown key %q in %q", key, label)
		}
		key = lower
	}
	sortMods(mods)
	return KeyLabel{Mods: mods, Key: key}, nil
}

func sortMods(mods []string) {
	for i := 1; i < len(mods); i++ {
		for j := i; j > 0 && modOrder[mods[j]] < modOrder[mods[j-1]]; j-- {
			mods[j], mods[j-1] = mods[j-1], mods[j]
		}
	}
}
