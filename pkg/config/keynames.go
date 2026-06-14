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
	"leader": {}, "localleader": {}, "esc": {}, "cr": {}, "enter": {}, "tab": {}, "bs": {},
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

// maxSequenceTokens caps the number of tokens a single Key string may
// expand into. The cap (32) is a security guardrail so a hostile config
// can't blow up the trie.
const maxSequenceTokens = 32

// ParseKeySequence splits s into raw key-label tokens and parses each one
// via ParseKeyLabel.
//
// Tokenisation rules:
//   - A "<...>" chunk is one token (greedy until the matching '>').
//   - Outside brackets, each rune is its own token.
//
// Errors:
//   - empty s → "empty sequence"
//   - unterminated '<...' → propagated from ParseKeyLabel
//   - more than maxSequenceTokens tokens → error
func ParseKeySequence(s string) ([]KeyLabel, error) {
	if s == "" {
		return nil, errors.New("keyname: empty sequence")
	}
	var tokens []string
	i := 0
	for i < len(s) {
		if s[i] == '<' {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				return nil, fmt.Errorf("keyname: unterminated bracket in %q", s)
			}
			tokens = append(tokens, s[i:i+end+1])
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			return nil, fmt.Errorf("keyname: invalid utf-8 in %q", s)
		}
		tokens = append(tokens, s[i:i+size])
		i += size
	}
	if len(tokens) > maxSequenceTokens {
		return nil, fmt.Errorf("keyname: sequence %q has %d tokens, max %d", s, len(tokens), maxSequenceTokens)
	}
	out := make([]KeyLabel, 0, len(tokens))
	for _, tok := range tokens {
		lbl, err := ParseKeyLabel(tok)
		if err != nil {
			return nil, err
		}
		out = append(out, lbl)
	}
	return out, nil
}

func sortMods(mods []string) {
	for i := 1; i < len(mods); i++ {
		for j := i; j > 0 && modOrder[mods[j]] < modOrder[mods[j-1]]; j-- {
			mods[j], mods[j-1] = mods[j-1], mods[j]
		}
	}
}
