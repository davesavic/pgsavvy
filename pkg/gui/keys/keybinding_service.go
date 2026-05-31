package keys

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// ContextKindLookup classifies a ContextKey by its layout role. The
// keybinding service uses it to expand `scope: all` into the set of
// non-popup contexts. Injected (rather than imported) so pkg/gui/keys
// does not take a dependency on pkg/gui/context's ContextTree.
type ContextKindLookup func(types.ContextKey) types.ContextKind

// nonPopupKinds is the closed set of ContextKinds eligible for
// `scope: all` expansion. Popups (PERSISTENT_POPUP, TEMPORARY_POPUP),
// the synthetic GLOBAL_CONTEXT, and STUB placeholders are excluded.
var nonPopupKinds = map[types.ContextKind]struct{}{
	types.SIDE_CONTEXT:    {},
	types.MAIN_CONTEXT:    {},
	types.EXTRAS_CONTEXT:  {},
	types.DISPLAY_CONTEXT: {},
}

// allKnownContexts is the snapshot of ContextKeys the service iterates
// when expanding `scope: all`. It mirrors the ContextKey constants
// declared in pkg/gui/types/context.go.
//
// Keeping this list local (rather than reading from a ContextTree) is
// the simplest dependency-free implementation. New ContextKeys MUST be
// appended here when added to types.context — the dlp.11 completeness
// test will fail loudly if a kindOf-classified non-popup context is
// missing.
var allKnownContexts = []types.ContextKey{
	types.SCHEMAS,
	types.TABLES,
	types.COLUMNS,
	types.INDEXES,
	types.QUERY_EDITOR,
	types.TABLE_DATA_EDITOR,
	types.RESULT_GRID,
	types.PLAN,
	types.MENU,
	types.CONFIRMATION,
	types.PROMPT,
	types.SUGGESTIONS,
	types.COMMAND_LINE,
	types.HISTORY,
	types.WHICH_KEY,
	types.LIMIT,
}

// WarnLevel / InfoLevel classify Warning severity.
const (
	WarnLevel = "warn"
	InfoLevel = "info"
)

// Warning is a non-fatal Build diagnostic. Warnings surface to the
// startup log (and, in dlp.7's `:reload`, to the command-line response
// area) so the user can correct a problematic config without crashing
// the app.
//
// Code is a stable string identifier (e.g. "orphan_action",
// "collision", "ambiguous_prefix"). Origin is the originating
// `file:line` or controller name. Message is a human-readable
// summary suitable for direct display.
type Warning struct {
	Level   string
	Code    string
	Message string
	Origin  string
}

// TrieSetKey indexes a ChordTrie inside a TrieSet by (Mode, Scope).
type TrieSetKey struct {
	Mode  types.Mode
	Scope types.ContextKey
}

// TrieSet aggregates one ChordTrie per (Mode, Scope) pair. It is the
// snapshot the Matcher (dlp.5) consumes — built once at startup and
// atomically swapped on `:reload`.
//
// Leader / LocalLeader carry the configured leader runes that were used
// to expand `<leader>` / `<localleader>` tokens during Build. They are
// preserved on the snapshot so downstream readers (the cheatsheet
// generator) can reverse-map an expanded rune back to its raw token
// form for display — without leaking the runtime leader value into
// static cheatsheet output. Zero values default to ' ' (leader) and
// ',' (localleader), matching leaderRunes' fallback.
//
// All methods on TrieSet are read-only after Build returns; concurrent
// callers need no synchronisation.
type TrieSet struct {
	tries       map[TrieSetKey]*ChordTrie
	Leader      rune
	LocalLeader rune
}

// NewTrieSet returns an empty TrieSet. Production code routes through
// KeybindingService.Build, which produces a fully-populated TrieSet
// from controller defaults; this constructor exists so tests outside
// the keys package (master Editor, orchestrator wiring) can hand the
// Matcher a small synthetic TrieSet without invoking the full Build
// pipeline.
func NewTrieSet() *TrieSet {
	return &TrieSet{tries: map[TrieSetKey]*ChordTrie{}}
}

// Set installs trie at (mode, scope). Intended for the same test
// scenarios NewTrieSet serves; production Build never calls this.
func (s *TrieSet) Set(mode types.Mode, scope types.ContextKey, trie *ChordTrie) {
	if s.tries == nil {
		s.tries = map[TrieSetKey]*ChordTrie{}
	}
	s.tries[TrieSetKey{Mode: mode, Scope: scope}] = trie
}

// Get returns the trie for (mode, scope), or (nil, false) if no
// bindings target that combination.
func (s *TrieSet) Get(mode types.Mode, scope types.ContextKey) (*ChordTrie, bool) {
	if s == nil || s.tries == nil {
		return nil, false
	}
	t, ok := s.tries[TrieSetKey{Mode: mode, Scope: scope}]
	return t, ok
}

// ChildrenAtMerged returns the immediate continuations of prefix for the
// focused (mode, scope) trie UNIONED with the (mode, GLOBAL) trie, mirroring
// the scope→GLOBAL fall-through Dispatch uses so the which-key popup lists
// exactly the keys that would fire. Scope-specific children win on a key
// collision (Dispatch tries the scope trie first). Rows are deduped by Key
// and sorted by Key.String() for determinism.
//
// Returns (nil, false) only when neither trie resolves prefix. Returns
// (empty, true) when prefix resolves but has no continuations. When scope
// IS GLOBAL the global trie is consulted once (no double-collect).
// dbsavvy-81j.
func (s *TrieSet) ChildrenAtMerged(mode types.Mode, scope types.ContextKey, prefix []Key) ([]ChildRow, bool) {
	if s == nil {
		return nil, false
	}

	var (
		merged   []ChildRow
		seen     = map[Key]struct{}{}
		anyFound bool
	)
	add := func(rows []ChildRow) {
		for _, r := range rows {
			if _, dup := seen[r.Key]; dup {
				continue
			}
			seen[r.Key] = struct{}{}
			merged = append(merged, r)
		}
	}

	// Scope first so scope-specific children win on a key collision.
	if scopeTrie, ok := s.Get(mode, scope); ok && scopeTrie != nil {
		if rows, found := scopeTrie.ChildrenAt(prefix); found {
			anyFound = true
			add(rows)
		}
	}
	if scope != types.GLOBAL {
		if globalTrie, ok := s.Get(mode, types.GLOBAL); ok && globalTrie != nil {
			if rows, found := globalTrie.ChildrenAt(prefix); found {
				anyFound = true
				add(rows)
			}
		}
	}

	if !anyFound {
		return nil, false
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Key.String() < merged[j].Key.String()
	})
	return merged, true
}

// Len reports the number of distinct (Mode, Scope) tries.
func (s *TrieSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.tries)
}

// Walk visits every (key, trie) pair in deterministic order (sorted by
// stringified key). Used by the cheatsheet generator and tests.
func (s *TrieSet) Walk(fn func(key TrieSetKey, trie *ChordTrie)) {
	if s == nil || s.tries == nil {
		return
	}
	keys := make([]TrieSetKey, 0, len(s.tries))
	for k := range s.tries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Mode != keys[j].Mode {
			return keys[i].Mode < keys[j].Mode
		}
		return keys[i].Scope < keys[j].Scope
	})
	for _, k := range keys {
		fn(k, s.tries[k])
	}
}

// KeybindingService orchestrates Build. It is stateless today but is a
// type (not a free function) so future epics can attach configuration
// (timeout overrides, per-mode policy) without breaking the API.
type KeybindingService struct{}

// NewKeybindingService constructs the service.
func NewKeybindingService() *KeybindingService { return &KeybindingService{} }

// Build constructs a TrieSet from controller-shipped defaults plus the
// user's on-disk config.
//
// Inputs:
//   - defaults: bindings published by controllers (via
//     AllDefaultBindings in dlp.8c). Source is forced to ShippedDefault.
//     Sequence is expected to already be a []Key from
//     SequenceFromShorthand; KeyLeader / KeyLocalLeader are expanded
//     here using cfg.Leader / cfg.LocalLeader.
//   - cfg: parsed UserConfig. cfg.Keybindings entries are lifted into
//     ChordBindings (Source=UserOverride for `action:`, CustomCmd for
//     `command:`) and inserted on top of defaults.
//   - registry: the command Registry to resolve ActionID against.
//   - kindOf: classifies a ContextKey for `scope: all` expansion.
//
// Outputs:
//   - The resulting TrieSet (never nil; may be empty).
//   - Warnings collected during expansion / insertion (orphan actions,
//     unparseable sequences, collisions, ambiguous prefixes).
//   - A hard error if the inputs themselves are unusable (nil registry
//     or nil cfg). Per D11, individual orphan / parse failures are
//     warnings, not errors.
//
// Build is safe for concurrent invocation — the service holds no state.
func (s *KeybindingService) Build(
	defaults []*ChordBinding,
	cfg *config.UserConfig,
	registry *commands.Registry,
	kindOf ContextKindLookup,
) (*TrieSet, []Warning, error) {
	if registry == nil {
		return nil, nil, fmt.Errorf("keys: Build called with nil registry")
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("keys: Build called with nil cfg")
	}
	if kindOf == nil {
		kindOf = func(types.ContextKey) types.ContextKind { return types.GLOBAL_CONTEXT }
	}

	leader, localLeader := leaderRunes(cfg)

	var warnings []Warning

	// Expand each default into one or more (Mode, Scope) entries and
	// route them to per-(Mode, Scope) builders.
	builders := map[TrieSetKey]*TrieBuilder{}
	getBuilder := func(k TrieSetKey) *TrieBuilder {
		if b, ok := builders[k]; ok {
			return b
		}
		b := NewTrieBuilder()
		builders[k] = b
		return b
	}

	insert := func(cb *ChordBinding, isUser bool) {
		// Resolve action.
		if cb.ActionID == "" {
			warnings = append(warnings, Warning{
				Level:   WarnLevel,
				Code:    "orphan_action",
				Message: fmt.Sprintf("binding %q has empty ActionID", SequenceString(cb.Sequence)),
				Origin:  cb.Origin,
			})
			return
		}
		var cmd *commands.Command
		switch {
		case cb.ActionID == "<nop>":
			cmd = commands.NopCommand
		case cb.Source == CustomCmd:
			// `command:` shorthand. Dispatch machinery ships with E11;
			// dlp.4 records a stub Command so the cheatsheet ★ glyph
			// renderer can find the leaf via Source==CustomCmd. The
			// stub is NOT registered with the Registry — it lives only
			// inside this trie.
			cmd = &commands.Command{
				ID:          cb.ActionID, // "command:<shell-string>"
				Description: cb.Description,
				Tag:         cb.Tag,
				Handler:     commands.NopSentinel,
			}
		default:
			c, ok := registry.Get(cb.ActionID)
			if !ok {
				warnings = append(warnings, Warning{
					Level: WarnLevel,
					Code:  "orphan_action",
					Message: fmt.Sprintf(
						"binding %q references unknown action %q; skipping",
						SequenceString(cb.Sequence), cb.ActionID,
					),
					Origin: cb.Origin,
				})
				return
			}
			cmd = c
		}

		// Expand leader / localleader placeholders.
		expanded := expandLeaderTokens(cb.Sequence, leader, localLeader)

		// Route per (Mode, Scope). cb.Mode is already a single bit by
		// the time we reach here (caller has fanned out tokens).
		key := TrieSetKey{Mode: cb.Mode, Scope: cb.Scope}
		builder := getBuilder(key)
		copy := *cb
		copy.Sequence = expanded
		if isUser {
			builder.InsertUser(&copy, cmd)
		} else {
			builder.InsertDefault(&copy, cmd)
		}
	}

	// Defaults first.
	for _, cb := range defaults {
		expandedBindings, ws := fanOutBinding(cb, kindOf, ShippedDefault)
		warnings = append(warnings, ws...)
		for _, b := range expandedBindings {
			insert(b, false)
		}
	}

	// User bindings on top.
	for i := range cfg.Keybindings {
		kb := &cfg.Keybindings[i]
		expandedBindings, ws := liftKeybindingConfig(kb)
		warnings = append(warnings, ws...)
		for _, b := range expandedBindings {
			fanned, fanWs := fanOutBinding(b, kindOf, b.Source)
			warnings = append(warnings, fanWs...)
			for _, fb := range fanned {
				insert(fb, true)
			}
		}
	}

	// Finalise each per-(Mode, Scope) trie.
	out := &TrieSet{tries: map[TrieSetKey]*ChordTrie{}, Leader: leader, LocalLeader: localLeader}
	for key, b := range builders {
		t, ws := b.Build()
		out.tries[key] = t
		warnings = append(warnings, ws...)
	}

	return out, warnings, nil
}

// leaderRunes extracts the leader / localleader runes from cfg, falling
// back to the documented defaults (" " and ",") when fields are empty
// or contain multi-rune content. Leader expansion only needs a single
// rune today; multi-rune leaders are a v2 concern.
func leaderRunes(cfg *config.UserConfig) (rune, rune) {
	leader := ' '
	localLeader := ','
	if cfg.Leader != "" {
		if r, ok := singleRune(cfg.Leader); ok {
			leader = r
		}
	}
	if cfg.LocalLeader != "" {
		if r, ok := singleRune(cfg.LocalLeader); ok {
			localLeader = r
		}
	}
	return leader, localLeader
}

func singleRune(s string) (rune, bool) {
	if s == "" {
		return 0, false
	}
	rs := []rune(s)
	if len(rs) != 1 {
		return 0, false
	}
	return rs[0], true
}

// fanOutBinding expands a single ChordBinding into one binding per
// (Mode-bit, ContextKey) cell. cb.Mode may already be a single bit
// (controller defaults) or a multi-bit mask; cb.Scope may be a concrete
// ContextKey, the literal "global", or the literal "all" pseudo-scope.
//
// The forced Source ensures the layering rule from D8 (defaults vs.
// user) survives even if the caller passed inconsistent values.
func fanOutBinding(cb *ChordBinding, kindOf ContextKindLookup, force Source) ([]*ChordBinding, []Warning) {
	if cb == nil {
		return nil, nil
	}
	if len(cb.Sequence) == 0 {
		return nil, []Warning{{
			Level:   WarnLevel,
			Code:    "empty_sequence",
			Message: fmt.Sprintf("binding %q has empty Sequence; skipping", cb.ActionID),
			Origin:  cb.Origin,
		}}
	}

	// Mode fan-out: every bit in cb.Mode becomes its own binding. The
	// zero value (ModeNormal) is treated as a single bit too (it IS
	// the trie key for Normal mode).
	var modes []types.Mode
	if cb.Mode == types.ModeNormal {
		modes = []types.Mode{types.ModeNormal}
	} else {
		for bit := types.Mode(1); bit != 0 && bit <= cb.Mode; bit <<= 1 {
			if cb.Mode&bit != 0 {
				modes = append(modes, bit)
			}
		}
	}

	// Scope fan-out.
	var scopes []types.ContextKey
	switch cb.Scope {
	case "all":
		for _, ctx := range allKnownContexts {
			kind := kindOf(ctx)
			if _, ok := nonPopupKinds[kind]; ok {
				scopes = append(scopes, ctx)
			}
		}
		// Also include the synthetic GLOBAL context per design: a
		// `scope: all` binding fires from global too, otherwise typing
		// outside any focused view would lose the binding.
		scopes = append(scopes, types.GLOBAL)
	case "global", "":
		scopes = []types.ContextKey{types.GLOBAL}
	default:
		scopes = []types.ContextKey{cb.Scope}
	}

	out := make([]*ChordBinding, 0, len(modes)*len(scopes))
	for _, m := range modes {
		for _, sc := range scopes {
			copy := *cb
			copy.Mode = m
			copy.Scope = sc
			copy.Source = force
			out = append(out, &copy)
		}
	}
	return out, nil
}

// liftKeybindingConfig parses one config.KeybindingConfig into one or
// more ChordBindings (one per mode-token, before the scope fan-out
// performed by fanOutBinding). Parse errors are emitted as warnings and
// the binding is skipped.
//
// Source is set from the shorthand: Action: → UserOverride;
// Command: → CustomCmd. Validation (dlp.3) already enforces Action XOR
// Command, so this function need not double-check.
func liftKeybindingConfig(kb *config.KeybindingConfig) ([]*ChordBinding, []Warning) {
	origin := fmtOrigin(kb)

	seq, err := SequenceFromShorthand(kb.Key)
	if err != nil {
		return nil, []Warning{{
			Level:   WarnLevel,
			Code:    "parse_sequence",
			Message: fmt.Sprintf("invalid key sequence %q: %v", kb.Key, err),
			Origin:  origin,
		}}
	}

	tokens := splitModeTokens(kb.Mode)
	modes, err := modeBitsFromTokens(tokens)
	if err != nil {
		return nil, []Warning{{
			Level:   WarnLevel,
			Code:    "parse_mode",
			Message: fmt.Sprintf("invalid mode %q: %v", kb.Mode, err),
			Origin:  origin,
		}}
	}

	// Collapse modes into one bitmask; fanOutBinding will fan it back
	// out per-bit. (Round-trip is fine: the bitmask is just the union.)
	var mask types.Mode
	hasNormal := false
	for _, m := range modes {
		if m == types.ModeNormal {
			hasNormal = true
			continue
		}
		mask |= m
	}

	// Determine ActionID + Source.
	var actionID string
	var source Source
	switch {
	case kb.Action != "":
		actionID = kb.Action
		source = UserOverride
	case kb.Command != "":
		// Per epic Non-Goals, CustomCmd handler machinery ships in E11.
		// dlp.4 records the binding so the cheatsheet ★ glyph paints
		// correctly; dispatch will be wired by the later epic.
		actionID = "command:" + kb.Command
		source = CustomCmd
	default:
		return nil, []Warning{{
			Level:   WarnLevel,
			Code:    "missing_action",
			Message: fmt.Sprintf("binding %q has neither action: nor command:", kb.Key),
			Origin:  origin,
		}}
	}

	scope := types.ContextKey(kb.Scope)
	if kb.Scope == "" {
		scope = types.GLOBAL
	}

	var out []*ChordBinding
	if hasNormal {
		out = append(out, &ChordBinding{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       scope,
			ActionID:    actionID,
			Description: kb.Description,
			Tag:         kb.Tag,
			ShowInBar:   kb.ShowInBar,
			OpensMenu:   kb.OpensMenu,
			Source:      source,
			Origin:      origin,
		})
	}
	if mask != types.ModeNormal {
		out = append(out, &ChordBinding{
			Sequence:    seq,
			Mode:        mask,
			Scope:       scope,
			ActionID:    actionID,
			Description: kb.Description,
			Tag:         kb.Tag,
			ShowInBar:   kb.ShowInBar,
			OpensMenu:   kb.OpensMenu,
			Source:      source,
			Origin:      origin,
		})
	}
	return out, nil
}

func splitModeTokens(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fmtOrigin(kb *config.KeybindingConfig) string {
	switch {
	case kb.OriginFile == "" && kb.OriginLine == 0:
		return ""
	case kb.OriginLine == 0:
		return kb.OriginFile
	default:
		return fmt.Sprintf("%s:%d", kb.OriginFile, kb.OriginLine)
	}
}
