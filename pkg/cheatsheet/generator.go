// Package cheatsheet generates and renders the auto-populated keybinding
// reference popup (`?` in normal mode). The popup is sourced from the live
// TrieSet — controllers register chord bindings, the cheatsheet groups them
// by (Mode, Scope, Tag), and Render emits a deterministic text dump.
//
// The package is read-only over the TrieSet: Generate never mutates the
// trie and Walk is concurrent-safe (the TrieSet is immutable after Build).
//
// See DESIGN.md §10.11.
package cheatsheet

import (
	"slices"
	"sort"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// Source glyph mapping (per DESIGN.md §10.11):
//
//	ShippedDefault → '·'
//	UserOverride   → '✱'
//	CustomCmd      → '★'
const (
	GlyphDefault = '·'
	// GlyphOverride: U+2731. Legend (pkg/i18n/english.go CheatsheetLegend)
	// and binding rows both emit this exact rune via glyphFor → Row.Glyph;
	// there is no source-level divergence. If a terminal renders the
	// legend's ✱ but shows '*' on binding rows (or vice versa), that is
	// font fallback in the host terminal, not a code bug — the rune is
	// identical at the byte level.
	GlyphOverride = '✱'
	GlyphCustom   = '★'
)

// Row is one binding line in the cheatsheet: the key sequence label, the
// human description, the tag (section header), the source layer that
// produced it, and the rendered glyph for that source.
type Row struct {
	Key         string
	Description string
	Tag         string
	Source      types.Source
	Glyph       rune
}

// Section groups Rows with a shared Tag. Sections are sorted by Tag
// (empty Tag sorts last); Rows within a Section are sorted by Key.
type Section struct {
	Tag  string
	Rows []Row
}

// ModeView is the cheatsheet view for one Mode within a scope partition
// (CurrentScope or Global). Sections is empty only for a Mode that has
// no leaves — Generate filters those out per AC D13 (vacuous-truth
// avoidance) so ModeView always carries at least one Section.
type ModeView struct {
	Mode     types.Mode
	Sections []Section
}

// Output is the structured cheatsheet result. CurrentScope holds the
// per-mode views for the focused scope (the Scope passed via
// GenerateInput); Global holds the per-mode views for the GLOBAL
// pseudo-scope.
//
// Both slices contain ONLY modes that carry ≥1 leaf in the input
// TrieSet — the completeness invariant test consumes this
// shape directly.
type Output struct {
	CurrentScope []ModeView
	Global       []ModeView
}

// GenerateInput bundles the read-only inputs Generate consumes.
//
// Trie is the live TrieSet snapshot (read via Matcher.TrieSet); a nil
// Trie collapses Generate to a zero Output.
//
// Scope is the focused ContextKey at the moment the cheatsheet was
// requested. Used to partition CurrentScope vs Global.
//
// Tr is unused by Generate but threaded through so callers can hand the
// same struct to Render without re-plumbing.
type GenerateInput struct {
	Trie  *keys.TrieSet
	Scope types.ContextKey
	Tr    *i18n.TranslationSet
}

// Generate enumerates every leaf in in.Trie, builds Row entries from the
// resolved *commands.Command on each leaf, and partitions the result
// into CurrentScope (Scope == in.Scope) and Global (Scope ==
// types.GLOBAL). Other scopes are ignored — the cheatsheet only ever
// shows the focused scope and the global tier.
//
// Empty modes are filtered out per AC D13 (populated-mode-only invariant).
// A nil or empty Trie returns the zero Output.
func Generate(in GenerateInput) Output {
	if in.Trie == nil {
		return Output{}
	}

	currentByMode := map[types.Mode][]Row{}
	globalByMode := map[types.Mode][]Row{}

	// Reverse-map the runtime leader/localleader runes back to their raw
	// `<leader>` / `<localleader>` token form when rendering the Key
	// column. Defaults match leaderRunes' fallback so a zero-value
	// TrieSet (test fixtures via NewTrieSet) still gets sensible output.
	leader := in.Trie.Leader
	if leader == 0 {
		leader = ' '
	}
	localLeader := in.Trie.LocalLeader
	if localLeader == 0 {
		localLeader = ','
	}

	in.Trie.Walk(func(key keys.TrieSetKey, trie *keys.ChordTrie) {
		var bucket map[types.Mode][]Row
		switch key.Scope {
		case in.Scope:
			bucket = currentByMode
		case types.GLOBAL:
			bucket = globalByMode
		default:
			return
		}
		trie.Walk(func(seq []keys.Key, leaf keys.LookupResult) {
			row := rowFromLeaf(seq, leaf, leader, localLeader)
			bucket[key.Mode] = append(bucket[key.Mode], row)
		})
	})

	return Output{
		CurrentScope: buildModeViews(currentByMode),
		Global:       buildModeViews(globalByMode),
	}
}

// rowFromLeaf builds a Row from a Walk callback's (sequence, leaf) pair.
// Description and Tag are read directly from leaf.Action; the trie node
// holds the resolved *commands.Command so no Registry lookup is needed.
// A nil Action would only occur if Walk is changed to visit interior
// nodes, which it does not today; we still guard defensively.
//
// leader / localLeader are the configured runes for the live TrieSet;
// keyLabel reverse-maps them back to the raw `<leader>` / `<localleader>`
// token form so the cheatsheet shows the stable shorthand the user wrote
// (e.g. `<leader>q`) rather than the post-expanded rune sequence (`Space q`).
func rowFromLeaf(seq []keys.Key, leaf keys.LookupResult, leader, localLeader rune) Row {
	row := Row{
		Key:    keyLabel(seq, leader, localLeader),
		Source: leaf.Source,
		Glyph:  glyphFor(leaf.Source),
	}
	if leaf.Action != nil {
		row.Description = leaf.Action.Description
		row.Tag = leaf.Action.Tag
	}
	if row.Description == "" && leaf.Action != nil {
		row.Description = leaf.Action.ID
	}
	return row
}

// keyLabel formats seq as the human-readable key column for the
// cheatsheet, reverse-mapping each occurrence of the leader/localleader
// rune to its `<leader>` / `<localleader>` token form. Runes are matched
// only when they carry no modifiers and are not a SpecialKey — that
// guards against false-positives like `<c-space>` being misread as
// `<c-leader>` when leader=Space.
//
// Note: this is a lossy operation by design — if the user binds a raw
// rune that happens to equal the leader (e.g. binding `qq` with
// leader=q), both runes will render as `<leader>`. That matches vim
// semantics where the two are indistinguishable post-expansion and is
// the price of keeping the cheatsheet stable across leader reconfigs.
func keyLabel(seq []keys.Key, leader, localLeader rune) string {
	out := make([]keys.Key, len(seq))
	for i, k := range seq {
		switch {
		case k.Special == keys.KeyNone && k.Mod == 0 && k.Code == leader:
			out[i] = keys.Key{Special: keys.KeyLeader}
		case k.Special == keys.KeyNone && k.Mod == 0 && k.Code == localLeader:
			out[i] = keys.Key{Special: keys.KeyLocalLeader}
		default:
			out[i] = k
		}
	}
	return keys.SequenceString(out)
}

func glyphFor(s types.Source) rune {
	switch s {
	case types.UserOverride:
		return GlyphOverride
	case types.CustomCmd:
		return GlyphCustom
	default:
		return GlyphDefault
	}
}

// buildModeViews partitions byMode into ModeViews sorted by Mode (the
// uint32 ordering: Normal=0, Insert=2, Visual=4, …). Empty modes (no
// rows) are dropped before construction.
func buildModeViews(byMode map[types.Mode][]Row) []ModeView {
	if len(byMode) == 0 {
		return nil
	}
	modes := make([]types.Mode, 0, len(byMode))
	for m, rows := range byMode {
		if len(rows) == 0 {
			continue
		}
		modes = append(modes, m)
	}
	slices.Sort(modes)
	out := make([]ModeView, 0, len(modes))
	for _, m := range modes {
		out = append(out, ModeView{
			Mode:     m,
			Sections: buildSections(byMode[m]),
		})
	}
	return out
}

// buildSections groups rows by Tag, sorts the rows within each section
// by Key, and orders sections by Tag with the empty Tag sorting LAST
// (per AC).
func buildSections(rows []Row) []Section {
	byTag := map[string][]Row{}
	for _, r := range rows {
		byTag[r.Tag] = append(byTag[r.Tag], r)
	}
	tags := make([]string, 0, len(byTag))
	for t := range byTag {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		// Empty tag sorts after every non-empty tag.
		if tags[i] == "" {
			return false
		}
		if tags[j] == "" {
			return true
		}
		return tags[i] < tags[j]
	})
	out := make([]Section, 0, len(tags))
	for _, t := range tags {
		sectRows := byTag[t]
		sort.Slice(sectRows, func(i, j int) bool { return sectRows[i].Key < sectRows[j].Key })
		out = append(out, Section{Tag: t, Rows: sectRows})
	}
	return out
}

// modeLabel returns a stable short label for m used in cheatsheet
// section headings. Mirrors types.Mode.String() but capitalised for
// header display ("Normal" rather than "normal").
func modeLabel(m types.Mode) string {
	switch m {
	case types.ModeNormal:
		return "Normal"
	case types.ModeInsert:
		return "Insert"
	case types.ModeVisual:
		return "Visual"
	case types.ModeVisualLine:
		return "Visual-Line"
	case types.ModeVisualBlock:
		return "Visual-Block"
	case types.ModeOperatorPending:
		return "Operator"
	case types.ModeCommand:
		return "Command"
	case types.ModeReplace:
		return "Replace"
	default:
		return m.String()
	}
}
