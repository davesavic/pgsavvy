package context

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// PlanContext renders a parsed EXPLAIN plan tree (models.Plan) inside a
// result-tab view. Each plan tab owns its own PlanContext instance — the
// collapsed map, cursor, and showRaw flag are per-tab state that lives
// only as long as the tab is open.
//
// Rendering modes:
//
//   - tree (default): a depth-first walk skips children of collapsed
//     nodes; every visible node renders one line prefixed by a tree
//     glyph (▼ expanded, ▶ collapsed, ─ leaf) and indented by depth.
//     Columns: name | est-cost | est-rows. When plan.Analyzed is true,
//     three more columns (actual-cost | actual-rows | loops) are
//     appended. Cost-percentile coloring (neutral→warning) is applied
//     to the cost column when theme.IsMonochrome() is false AND the
//     visible-node count is ≥ 4 (degenerate plans skip coloring).
//   - raw (o toggle): plan.RawText is rendered verbatim through
//     grid.SanitizeCellEscapes so server-side ANSI cannot bleed
//     through. AC requires the call site to exist even when the
//     sanitizer is an identity stub today (dbsavvy-uv0.8 AD-16).
//
// PlanContext is NOT goroutine-safe; all mutators (toggle, MoveCursor,
// ExpandAll, CollapseAllButRoot, JumpHeaviest, ToggleRaw) run on the
// MainLoop, matching the other contexts. dbsavvy-uv0.8.
type PlanContext struct {
	BaseContext

	deps Deps

	plan      models.Plan
	cursor    int
	collapsed map[*models.PlanNode]bool
	showRaw   bool
}

// MinVisibleForColoring is the AC-documented threshold below which cost-
// percentile coloring is suppressed entirely. Plans with fewer than 4
// visible nodes are too degenerate to bucket meaningfully. dbsavvy-uv0.8.
const MinVisibleForColoring = 4

// NewPlanContext constructs a PlanContext bound to base's key/view. The
// supplied plan is held by value; a nil plan.Node renders an empty body
// in tree mode (raw mode still works as long as plan.RawText is set).
func NewPlanContext(base BaseContext, deps Deps, plan models.Plan) *PlanContext {
	return &PlanContext{
		BaseContext: base,
		deps:        deps,
		plan:        plan,
		collapsed:   map[*models.PlanNode]bool{},
	}
}

// Plan returns the underlying plan (read-only view; callers must not
// mutate the returned struct).
func (p *PlanContext) Plan() models.Plan { return p.plan }

// Cursor returns the current cursor index into the visible node list.
func (p *PlanContext) Cursor() int { return p.cursor }

// ShowRaw reports whether the context is in raw-text mode.
func (p *PlanContext) ShowRaw() bool { return p.showRaw }

// CollapsedCount returns the number of currently-collapsed nodes — a
// test-friendly accessor (the map itself stays private).
func (p *PlanContext) CollapsedCount() int { return len(p.collapsed) }

// IsCollapsed reports whether n is in the collapsed set.
func (p *PlanContext) IsCollapsed(n *models.PlanNode) bool {
	if n == nil {
		return false
	}
	return p.collapsed[n]
}

// VisibleNodes returns the flattened depth-first walk that
// HandleRender consumes. Exposed so the controller can resolve
// cursor index → *models.PlanNode without duplicating the walk
// logic.
func (p *PlanContext) VisibleNodes() []VisibleNode {
	if p.plan.Node == nil {
		return nil
	}
	var out []VisibleNode
	p.walkVisible(p.plan.Node, 0, &out)
	return out
}

// VisibleNode is one element in the flattened expanded-node list.
type VisibleNode struct {
	Node  *models.PlanNode
	Depth int
}

func (p *PlanContext) walkVisible(n *models.PlanNode, depth int, out *[]VisibleNode) {
	if n == nil {
		return
	}
	*out = append(*out, VisibleNode{Node: n, Depth: depth})
	if p.collapsed[n] {
		return
	}
	for _, c := range n.Children {
		p.walkVisible(c, depth+1, out)
	}
}

// CursorNode returns the node under the cursor in the current visible
// list, or nil when the list is empty.
func (p *PlanContext) CursorNode() *models.PlanNode {
	vis := p.VisibleNodes()
	if len(vis) == 0 {
		return nil
	}
	idx := p.cursor
	if idx < 0 {
		idx = 0
	}
	if idx >= len(vis) {
		idx = len(vis) - 1
	}
	return vis[idx].Node
}

// MoveCursor advances the cursor by delta, clamping into
// [0, len(visible)-1]. A move on an empty list snaps to 0.
func (p *PlanContext) MoveCursor(delta int) {
	vis := p.VisibleNodes()
	if len(vis) == 0 {
		p.cursor = 0
		return
	}
	next := p.cursor + delta
	if next < 0 {
		next = 0
	}
	if next >= len(vis) {
		next = len(vis) - 1
	}
	p.cursor = next
}

// Toggle flips the collapsed state of the cursor node (no-op on a leaf
// or empty list). The cursor stays put — AC rule "cursor on collapsed
// subtree's root: <CR> expands; cursor stays put".
func (p *PlanContext) Toggle() {
	n := p.CursorNode()
	if n == nil || len(n.Children) == 0 {
		return
	}
	if p.collapsed[n] {
		delete(p.collapsed, n)
	} else {
		p.collapsed[n] = true
	}
	// Clamp cursor — collapsing may shorten the visible list.
	vis := p.VisibleNodes()
	if p.cursor >= len(vis) {
		p.cursor = len(vis) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// ExpandAll empties the collapsed set. AC rule: "<C-a> expands all".
func (p *PlanContext) ExpandAll() {
	p.collapsed = map[*models.PlanNode]bool{}
}

// CollapseAllButRoot marks every interior node (other than the root)
// collapsed. AC rule: "<C-x> collapses every node except root". Single-
// node plans are a no-op (root only, nothing to collapse).
func (p *PlanContext) CollapseAllButRoot() {
	if p.plan.Node == nil {
		return
	}
	p.collapsed = map[*models.PlanNode]bool{}
	// Walk every node OTHER than the root and mark it collapsed if it has
	// children. Iterative DFS so we don't blow the stack on deep plans.
	stack := append([]*models.PlanNode(nil), p.plan.Node.Children...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if len(n.Children) > 0 {
			p.collapsed[n] = true
		}
		stack = append(stack, n.Children...)
	}
	// Cursor may now be out of range — clamp.
	vis := p.VisibleNodes()
	if p.cursor >= len(vis) {
		p.cursor = len(vis) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// JumpHeaviest moves the cursor to the heaviest child (by Cost) within
// the cursor node's subtree. Tie-break: first child encountered in
// depth-first iteration wins. No-op when the cursor is on a leaf.
// dbsavvy-uv0.8 AC: "H jumps cursor to heaviest child of cursor's
// subtree (DFS tie-break: first encountered)".
func (p *PlanContext) JumpHeaviest() {
	root := p.CursorNode()
	if root == nil || len(root.Children) == 0 {
		return
	}
	// DFS over the cursor node's subtree (excluding the cursor node
	// itself), tracking the highest-cost descendant. Tie-break: first
	// encountered (do NOT overwrite on equal cost).
	var heaviest *models.PlanNode
	var heaviestCost float64
	var walk func(n *models.PlanNode)
	walk = func(n *models.PlanNode) {
		if n == nil {
			return
		}
		if n != root {
			if heaviest == nil || n.Cost > heaviestCost {
				heaviest = n
				heaviestCost = n.Cost
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	if heaviest == nil {
		return
	}
	// To make the heaviest visible, expand every ancestor between root
	// (inclusive) and the heaviest's parent (inclusive).
	path := findPath(p.plan.Node, heaviest)
	for _, n := range path {
		if n == heaviest {
			break
		}
		delete(p.collapsed, n)
	}
	// Locate heaviest in the (newly) flattened visible list.
	vis := p.VisibleNodes()
	for i, v := range vis {
		if v.Node == heaviest {
			p.cursor = i
			return
		}
	}
}

// ToggleRaw flips between tree and raw modes.
func (p *PlanContext) ToggleRaw() {
	p.showRaw = !p.showRaw
}

// HandleRender writes the current view body. Routes through the GuiDriver
// using writeView so a nil driver (unit tests / pre-wire) is a silent
// no-op. dbsavvy-uv0.8.
func (p *PlanContext) HandleRender() error {
	deps := p.deps
	viewName := p.GetViewName()
	body := p.RenderBody()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// RenderBody produces the rendered body string for the current state.
// Exposed so the result-tabs LayoutPaint can invoke rendering without
// needing the GuiDriver path. dbsavvy-uv0.8.
func (p *PlanContext) RenderBody() string {
	if p.showRaw {
		// AC sanitization rule: raw text routes through SanitizeCellEscapes
		// even though the function is an identity stub today (T9 finalises
		// the impl).
		return grid.SanitizeCellEscapes(p.plan.RawText)
	}
	if p.plan.Node == nil {
		// AC edge case: nil Node renders empty body (no crash).
		return ""
	}
	vis := p.VisibleNodes()
	if len(vis) == 0 {
		return ""
	}
	colorize := !theme.IsMonochrome() && len(vis) >= MinVisibleForColoring
	var thresholds [4]float64
	if colorize {
		thresholds = costThresholds(vis)
	}
	var b strings.Builder
	for i, v := range vis {
		glyph := glyphFor(v.Node, p.collapsed[v.Node])
		marker := "  "
		if i == p.cursor {
			marker = "> "
		}
		indent := strings.Repeat("  ", v.Depth)
		costStr := formatCost(v.Node.Cost)
		if colorize {
			costStr = applyCostColor(costStr, v.Node.Cost, thresholds)
		}
		fmt.Fprintf(&b, "%s%s%s %s", marker, indent, glyph, v.Node.Op)
		// est-cost / est-rows columns.
		fmt.Fprintf(&b, "  cost=%s rows=%d", costStr, v.Node.EstRows)
		if p.plan.Analyzed {
			fmt.Fprintf(&b, "  actual_cost=%s actual_rows=%d loops=%d",
				formatCost(v.Node.ActualCost), v.Node.ActualRows, v.Node.Loops)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// glyphFor returns the tree glyph appropriate for n's collapse state +
// whether it has children.
func glyphFor(n *models.PlanNode, collapsed bool) string {
	if n == nil {
		return presentation.GlyphLeaf
	}
	if len(n.Children) == 0 {
		return presentation.GlyphLeaf
	}
	if collapsed {
		return presentation.GlyphCollapsed
	}
	return presentation.GlyphExpanded
}

// formatCost stringifies a cost float with one decimal place — enough
// precision to distinguish small differences without overflowing the
// column. Negative / NaN costs collapse to "0.0".
func formatCost(c float64) string {
	if c < 0 || c != c { // NaN check via self-inequality
		return "0.0"
	}
	return fmt.Sprintf("%.1f", c)
}

// costThresholds returns [P50, P75, P90, P95] of the visible nodes'
// Cost fields. Used by applyCostColor to bucket each cost into a
// neutral→warning gradient. Returns zero thresholds when vis is empty.
func costThresholds(vis []VisibleNode) [4]float64 {
	if len(vis) == 0 {
		return [4]float64{}
	}
	costs := make([]float64, len(vis))
	for i, v := range vis {
		costs[i] = v.Node.Cost
	}
	sort.Float64s(costs)
	pick := func(p float64) float64 {
		idx := int(p * float64(len(costs)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(costs) {
			idx = len(costs) - 1
		}
		return costs[idx]
	}
	return [4]float64{pick(0.50), pick(0.75), pick(0.90), pick(0.95)}
}

// applyCostColor wraps s in an ANSI SGR escape whose color reflects the
// cost-percentile bucket (neutral → yellow → red → bold red). The
// mapping is intentionally simple — we lean on the existing ANSI 8-color
// palette so terminals without truecolor still render the gradient.
// The caller is responsible for the IsMonochrome / vis-count gate;
// applyCostColor itself does not consult theme state.
func applyCostColor(s string, cost float64, t [4]float64) string {
	const (
		reset   = "\x1b[0m"
		neutral = "" // no escape — default fg
		yellow  = "\x1b[33m"
		red     = "\x1b[31m"
		bred    = "\x1b[1;31m"
	)
	var code string
	switch {
	case cost >= t[3]: // ≥ P95
		code = bred
	case cost >= t[2]: // ≥ P90
		code = red
	case cost >= t[1]: // ≥ P75
		code = yellow
	case cost >= t[0]: // ≥ P50
		code = yellow
	default:
		code = neutral
	}
	if code == "" {
		return s
	}
	return code + s + reset
}

// findPath returns the chain of nodes from root to target inclusive,
// or nil when target is not reachable from root. Used by JumpHeaviest
// to expand ancestor nodes before placing the cursor.
func findPath(root, target *models.PlanNode) []*models.PlanNode {
	if root == nil || target == nil {
		return nil
	}
	if root == target {
		return []*models.PlanNode{root}
	}
	for _, c := range root.Children {
		if path := findPath(c, target); path != nil {
			return append([]*models.PlanNode{root}, path...)
		}
	}
	return nil
}
