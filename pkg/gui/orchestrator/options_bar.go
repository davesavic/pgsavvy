package orchestrator

import (
	"sort"

	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

// optionsBarMax is the hard cap on options entries shown in the status
// bar. The 9th+ ShowInBar leaf is silently truncated; the trailing
// "?: more" hint that BuildStatusLine appends signals to the user that
// the cheatsheet has the full list.
const optionsBarMax = 8

// CollectOptionsForScope walks the chord trie set and gathers the
// description / key pairs flagged ShowInBar for the focused (mode,
// scope) plus the (mode, GLOBAL) pseudo-scope. Entries are formatted
// as "description: key", sorted by Tag then by sequence-string label,
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
) []string {
	_ = tr
	if trieSet == nil {
		return []string{}
	}

	type entry struct {
		tag         string
		key         string
		description string
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
			entries = append(entries, entry{
				tag:         leaf.Action.Tag,
				key:         keys.SequenceString(seq),
				description: leaf.Action.Description,
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
		out = append(out, e.description+": "+e.key)
	}
	return out
}
