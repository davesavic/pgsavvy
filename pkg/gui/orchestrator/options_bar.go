package orchestrator

import (
	"sort"

	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// disabledSuffix is appended to the rendered segment of a ShowInBar
// leaf whose Command reports Disabled at frame-build time. The marker
// is deliberately textual (not a glyph) because the options-bar surface
// is a plain []string consumed by status.BuildStatusLine — no styling
// layer exists between CollectOptionsForScope and the final render.
const disabledSuffix = " (disabled)"

// optionsBarMax is the hard cap on options entries shown in the status
// bar. The 9th+ ShowInBar leaf is silently truncated; the trailing
// "?: more" hint that BuildStatusLine appends signals to the user that
// the cheatsheet has the full list.
const optionsBarMax = 8

// CollectOptionsForScope walks the chord trie set and gathers the
// description / key pairs flagged ShowInBar for the focused (mode,
// scope) plus the (mode, GLOBAL) pseudo-scope. Entries are formatted
// as "[key] description", sorted by Tag then by sequence-string label,
// and capped at optionsBarMax.
//
// Returns an empty (non-nil) []string when the trieSet is nil, when
// the relevant tries are absent, or when no leaves carry ShowInBar.
// BuildStatusLine renders only the mode label, connection header,
// and "?: more" terminator in that case.
//
// The tr parameter is accepted for forward-compatibility with future
// i18n needs (e.g. localized separators); it is currently unused.
func CollectOptionsForScope(
	trieSet *keys.TrieSet,
	mode types.Mode,
	scope types.ContextKey,
	tr *i18n.TranslationSet,
	actionFilter func(string) bool,
) []string {
	_ = tr
	if trieSet == nil {
		return []string{}
	}

	type entry struct {
		tag         string
		key         string
		description string
		disabled    bool
	}

	var entries []entry
	collect := func(trie *keys.ChordTrie) {
		if trie == nil {
			return
		}
		trie.Walk(func(seq []keys.Key, leaf keys.LookupResult) {
			if !leaf.ShowInBar || leaf.Action == nil {
				return
			}
			if actionFilter != nil && !actionFilter(leaf.Action.ID) {
				return
			}
			// Probe Disabled with the focused (mode, scope) populated
			// on ExecCtx. Count/Register stay zero — the options bar
			// renders per-frame with no chord-prefix state. Predicates
			// that key off Mode/Scope (e.g. "disable in Insert mode",
			// "only enabled inside QUERY_EDITOR") now see the same
			// dispatch signal the Matcher will use, so the bar's
			// disabled markers stay consistent with actual dispatch.
			_, disabled := leaf.Action.Disabled(commands.ExecCtx{Mode: mode, Scope: scope})
			entries = append(entries, entry{
				tag:         leaf.Action.Tag,
				key:         keys.SequenceString(seq),
				description: leaf.Action.Description,
				disabled:    disabled,
			})
		})
	}

	if trie, ok := trieSet.Get(mode, scope); ok {
		collect(trie)
	}
	// Avoid double-collecting when the focused scope IS GLOBAL.
	if scope != types.GLOBAL {
		if trie, ok := trieSet.Get(mode, types.GLOBAL); ok {
			collect(trie)
		}
	}

	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].tag != entries[j].tag {
			return entries[i].tag < entries[j].tag
		}
		return entries[i].key < entries[j].key
	})

	if len(entries) > optionsBarMax {
		entries = entries[:optionsBarMax]
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		segment := "[" + e.key + "] " + e.description
		if e.disabled {
			segment += disabledSuffix
		}
		out = append(out, segment)
	}
	return out
}
