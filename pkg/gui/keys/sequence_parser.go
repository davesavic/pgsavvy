package keys

import (
	"fmt"
	"unicode/utf8"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// SequenceFromShorthand parses s into a []Key WITHOUT expanding
// `<leader>` or `<localleader>` — those tokens become
// Key{Special: KeyLeader} / Key{Special: KeyLocalLeader}.
//
// Callers wiring controller default bindings use this directly; the
// Build pipeline later substitutes the configured leader/localleader
// runes before inserting into the trie.
//
// Errors propagate from config.ParseKeySequence (empty input, malformed
// labels, unterminated brackets, length cap exceeded).
func SequenceFromShorthand(s string) ([]Key, error) {
	labels, err := config.ParseKeySequence(s)
	if err != nil {
		return nil, err
	}
	keys := make([]Key, 0, len(labels))
	for _, lbl := range labels {
		k, err := keyFromLabel(lbl)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// keyFromLabel converts one parsed config.KeyLabel into a Key. Modifier
// strings are mapped to the bitmask; the Key field is interpreted as a
// SpecialKey name first, then as a bare rune.
func keyFromLabel(lbl config.KeyLabel) (Key, error) {
	var k Key
	for _, m := range lbl.Mods {
		switch m {
		case "ctrl":
			k.Mod |= ModCtrl
		case "alt":
			k.Mod |= ModAlt
		case "shift":
			k.Mod |= ModShift
		case "meta":
			k.Mod |= ModMeta
		default:
			return Key{}, fmt.Errorf("keys: unknown modifier %q", m)
		}
	}

	if sp, ok := specialKeyByName[lbl.Key]; ok {
		k.Special = sp
		return k, nil
	}

	// Bare rune.
	r, n := utf8.DecodeRuneInString(lbl.Key)
	if r == utf8.RuneError {
		return Key{}, fmt.Errorf("keys: invalid utf-8 in label %q", lbl.Key)
	}
	if n != len(lbl.Key) {
		return Key{}, fmt.Errorf("keys: multi-rune bare label %q", lbl.Key)
	}
	k.Code = r
	return k, nil
}

// specialKeyByName maps the lowercase special names produced by
// config.ParseKeyLabel into the SpecialKey enum.
var specialKeyByName = map[string]SpecialKey{
	"leader":      KeyLeader,
	"localleader": KeyLocalLeader,
	"esc":         KeyEsc,
	"cr":          KeyEnter,
	"enter":       KeyEnter,
	"tab":         KeyTab,
	"bs":          KeyBs,
	"backspace":   KeyBs,
	"space":       KeySpace,
	"up":          KeyUp,
	"down":        KeyDown,
	"left":        KeyLeft,
	"right":       KeyRight,
	"home":        KeyHome,
	"end":         KeyEnd,
	"pgup":        KeyPgUp,
	"pgdn":        KeyPgDn,
	"ins":         KeyIns,
	"del":         KeyDel,
	"f1":          KeyF1,
	"f2":          KeyF2,
	"f3":          KeyF3,
	"f4":          KeyF4,
	"f5":          KeyF5,
	"f6":          KeyF6,
	"f7":          KeyF7,
	"f8":          KeyF8,
	"f9":          KeyF9,
	"f10":         KeyF10,
	"f11":         KeyF11,
	"f12":         KeyF12,
}

// expandLeaderTokens replaces every KeyLeader / KeyLocalLeader in seq
// with the configured rune, preserving the original modifier bitmask
// (modifiers on `<leader>` are technically possible — e.g. `<c-leader>`
// — and are forwarded onto the expanded Key).
//
// Returns a fresh slice; seq is not mutated.
func expandLeaderTokens(seq []Key, leader, localLeader rune) []Key {
	out := make([]Key, len(seq))
	for i, k := range seq {
		switch k.Special {
		case KeyLeader:
			k.Special = KeyNone
			k.Code = leader
		case KeyLocalLeader:
			k.Special = KeyNone
			k.Code = localLeader
		}
		out[i] = k
	}
	return out
}

// modeBitsFromTokens maps the comma-separated `mode:` tokens from a
// KeybindingConfig into a slice of single-bit types.Mode values. dlp.3
// validation rejects unknown tokens; Build therefore can rely on the
// input being well-formed but still falls back to skipping unknown
// tokens defensively (returns nil + non-nil err so the caller can emit a
// Warning rather than crash).
//
// Mapping (per design + validation.allowedModeTokens):
//
//	"n"     → ModeNormal
//	"i"     → ModeInsert
//	"v"     → ModeVisual
//	"x"     → ModeVisual          (vim's "visual-without-select"; we have
//	                                no Select mode, so it aliases "v")
//	"V"     → ModeVisualLine
//	"<c-v>" → ModeVisualBlock
//	"o"     → ModeOperatorPending
//	"c"     → ModeCommand
func modeBitsFromTokens(tokens []string) ([]types.Mode, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("keys: empty mode token list")
	}
	out := make([]types.Mode, 0, len(tokens))
	for _, tok := range tokens {
		m, ok := modeByToken[tok]
		if !ok {
			return nil, fmt.Errorf("keys: unknown mode token %q", tok)
		}
		out = append(out, m)
	}
	return out, nil
}

var modeByToken = map[string]types.Mode{
	"n":     types.ModeNormal,
	"i":     types.ModeInsert,
	"v":     types.ModeVisual,
	"x":     types.ModeVisual,
	"V":     types.ModeVisualLine,
	"<c-v>": types.ModeVisualBlock,
	"o":     types.ModeOperatorPending,
	"c":     types.ModeCommand,
}
