package types

// MainContextPair names the two ContextKeys that occupy the right-hand
// "main" + "secondary" window slots together. The pair is a hint the
// orchestrator uses to keep the two halves of the main pane in sync
// when one half is focused — the unfocused half still renders, and
// keyboard input routes by the focused half's scope.
//
// DESIGN.md §7 motivates the type: the focus
// stack is a single linear stack, but the main pane is logically two
// slots. MainContextPair lets controllers + the orchestrator describe
// "this is the QueryEditor / result-tab pairing" or "Plan view in the
// main slot, QueryEditor below" without bolting a second stack
// dimension onto ContextTree.
//
// Secondary may be a dynamic ContextKey when the multi-tab result
// pane is active — the ResultTabsHelper allocates ContextKey values of
// the form "result_tab_<i>" up to the configured cap, and the active
// tab's key fills the Secondary slot.
type MainContextPair struct {
	Main      ContextKey
	Secondary ContextKey
}

// PairNormal is the default pairing while the user is composing /
// editing SQL: QUERY_EDITOR drives the main slot, the active result
// tab occupies the secondary slot. The Secondary value is the
// sentinel "result_tab_active" — at runtime the ResultTabsHelper
// resolves it to the concrete result_tab_<i> ContextKey of whichever
// tab is currently active.
var PairNormal = MainContextPair{
	Main:      QUERY_EDITOR,
	Secondary: ContextKey("result_tab_active"),
}

// PairPlanFocus is the pairing surfaced when the user expands an
// EXPLAIN plan: PLAN takes the main slot, the QUERY_EDITOR demotes
// to the secondary slot so the user can tweak the statement while
// reading the plan.
var PairPlanFocus = MainContextPair{
	Main:      PLAN,
	Secondary: QUERY_EDITOR,
}

// ResultTabActiveKey is the sentinel ContextKey that PairNormal's
// Secondary slot points at. ResultTabsHelper substitutes the live
// active-tab key for this sentinel when rendering the pair.
const ResultTabActiveKey ContextKey = "result_tab_active"

// ResultTabViewPrefix is the shared prefix of every per-slot result-tab
// ContextKey / gocui view name (result_tab_<slot>). The single source of
// truth so callers can recognise a result-tab view by prefix without
// re-hardcoding the literal.
const ResultTabViewPrefix = "result_tab_"

// ResultTabKey returns the ContextKey associated with slot i (0-based).
// The naming scheme matches the dynamic gocui view name the
// ResultTabsHelper allocates per tab.
func ResultTabKey(i int) ContextKey {
	return ContextKey(ResultTabViewPrefix + itoa(i))
}

// itoa is a tiny base-10 int formatter to avoid an strconv import in
// this minimal types package. Single-digit positive ints cover the
// pgsavvy result-tab cap (default 8).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
