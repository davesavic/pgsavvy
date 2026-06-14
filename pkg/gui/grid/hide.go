package grid

// Hide-cols helpers. Lives next to scroll.go so the visibleColumnOrder
// composition (frozen-first → colOffset → hide-filter) stays local to
// the grid package.

// filterHidden returns a new slice of column indices with any index in
// hidden removed, preserving order. A nil / empty hidden map returns
// order unchanged (no allocation in the common no-hide path).
//
// Pure function: no concurrent access concerns; callers serialise the
// snapshot capture upstream.
func filterHidden(order []int, hidden map[int]bool) []int {
	if len(hidden) == 0 {
		return order
	}
	out := make([]int, 0, len(order))
	for _, c := range order {
		if hidden[c] {
			continue
		}
		out = append(out, c)
	}
	return out
}
