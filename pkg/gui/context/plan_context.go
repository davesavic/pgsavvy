package context

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/grid"
	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query/plandoctor"
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
//     three more columns (actual-time | actual-rows | loops) are
//     appended. Self-basis coloring (neutral→warning), bucketed on
//     EXCLUSIVE self-time (analyzed) or self-cost (estimate-only), is
//     applied to the cost column when theme.IsMonochrome() is false AND
//     the visible-node count is ≥ 4 (degenerate plans skip coloring).
//   - raw (o toggle): plan.RawText is rendered verbatim through
//     grid.SanitizeCellEscapes so server-side ANSI cannot bleed
//     through. AC requires the call site to exist even when the
//     sanitizer is an identity stub today.
//
// PlanContext is NOT goroutine-safe; all mutators (toggle, MoveCursor,
// ExpandAll, CollapseAllButRoot, JumpHeaviest, ToggleRaw) run on the
// MainLoop, matching the other contexts.
type PlanContext struct {
	BaseContext

	deps Deps

	plan      models.Plan
	cursor    int
	collapsed map[*models.PlanNode]bool
	showRaw   bool

	// Plan-doctor findings, computed once at construction over the SAME node
	// pointers VisibleNodes walks (pointer-identity contract from
	// plandoctor.Analyze). findingByNode maps each flagged node to its
	// highest-ranked finding for the inline tree badge.
	findings      []plandoctor.Finding
	findingByNode map[*models.PlanNode]plandoctor.Finding
	// showInsights is the per-tab insights-strip toggle, mirroring showRaw.
	// When true the strip is always rendered (an explicit "no issues detected"
	// empty state when there are no findings) so pressing i is never a silent
	// no-op. j/k/Enter only target the strip when it actually has findings (see
	// InsightsActive), so an empty strip never hijacks tree navigation.
	showInsights  bool
	insightCursor int
	// viewportWidth is the panel's inner width, recorded by the layout pass so
	// the insights strip can wrap long explanations to fit. 0 (tests /
	// pre-layout) disables wrapping.
	viewportWidth int
}

// MinVisibleForColoring is the threshold below which heat coloring is
// suppressed entirely. A single visible node is degenerate — it would
// always land in the top percentile bucket and paint solid red — so the
// floor is 2, the smallest plan where one node can be hotter than another.
const MinVisibleForColoring = 2

// NewPlanContext constructs a PlanContext bound to base's key/view. The
// supplied plan is held by value; a nil plan.Node renders an empty body
// in tree mode (raw mode still works as long as plan.RawText is set).
func NewPlanContext(base BaseContext, deps Deps, plan models.Plan) *PlanContext {
	// Guarantee derived heat fields (SelfCost/SelfTime/…) are populated for
	// every caller, not only the parse path. ComputeDerived is idempotent, so
	// the double-call on parsed plans is harmless.
	plan.ComputeDerived()
	pc := &PlanContext{
		BaseContext: base,
		deps:        deps,
		plan:        plan,
		collapsed:   map[*models.PlanNode]bool{},
	}
	// Analyze over the stored plan so finding NodeRefs share pointer identity
	// with the nodes VisibleNodes/RenderBody walk (plan.Node is the same tree
	// pointer after the value copy). Findings are ranked; the first one seen
	// for a node is its highest-ranked, used for the badge.
	pc.findings = plandoctor.Analyze(&pc.plan)
	pc.findingByNode = make(map[*models.PlanNode]plandoctor.Finding, len(pc.findings))
	for _, f := range pc.findings {
		if _, seen := pc.findingByNode[f.NodeRef]; !seen {
			pc.findingByNode[f.NodeRef] = f
		}
	}
	return pc
}

// Plan returns the underlying plan (read-only view; callers must not
// mutate the returned struct).
func (p *PlanContext) Plan() models.Plan { return p.plan }

// Cursor returns the current cursor index into the visible node list.
func (p *PlanContext) Cursor() int { return p.cursor }

// ShowRaw reports whether the context is in raw-text mode.
func (p *PlanContext) ShowRaw() bool { return p.showRaw }

// Findings returns the ranked plan-doctor findings for this plan (never nil).
func (p *PlanContext) Findings() []plandoctor.Finding { return p.findings }

// ShowInsights reports whether the insights strip is toggled on.
func (p *PlanContext) ShowInsights() bool { return p.showInsights }

// InsightCursor returns the selected index into Findings while the strip is on.
func (p *PlanContext) InsightCursor() int { return p.insightCursor }

// InsightsActive reports whether insights navigation owns j/k/Enter: the strip
// is toggled on AND there is at least one finding to navigate. The controller
// routes on this so an empty strip never steals tree keys.
func (p *PlanContext) InsightsActive() bool {
	return p.showInsights && len(p.findings) > 0
}

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
	idx := max(p.cursor, 0)
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
	next := max(p.cursor+delta, 0)
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

// JumpHeaviest moves the cursor to the heaviest child within the cursor
// node's subtree, ranked by the SAME self-basis the heat map uses
// (SelfTime when the plan is analyzed, else SelfCost; clamped >=0). This
// keeps the H key, heat coloring, and (later) insights agreeing on one
// "real bottleneck" definition. Tie-break: first child encountered in
// depth-first iteration wins. No-op when the cursor is on a leaf.
// H jumps cursor to heaviest child of cursor's subtree
// (DFS tie-break: first encountered).
func (p *PlanContext) JumpHeaviest() {
	root := p.CursorNode()
	if root == nil || len(root.Children) == 0 {
		return
	}
	analyzed := p.plan.Analyzed
	// DFS over the cursor node's subtree (excluding the cursor node
	// itself), tracking the highest self-basis descendant. Tie-break: first
	// encountered (do NOT overwrite on equal basis).
	var heaviest *models.PlanNode
	var heaviestBasis float64
	var walk func(n *models.PlanNode)
	walk = func(n *models.PlanNode) {
		if n == nil {
			return
		}
		if n != root {
			if b := heatBasis(n, analyzed); heaviest == nil || b > heaviestBasis {
				heaviest = n
				heaviestBasis = b
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

// SetViewportWidth records the panel's inner width so the insights strip can
// wrap long explanations to fit. The layout pass calls it each frame; a value
// <= 0 (tests / pre-layout) disables wrapping.
func (p *PlanContext) SetViewportWidth(w int) {
	if w < 0 {
		w = 0
	}
	p.viewportWidth = w
}

// ToggleInsights flips the insights strip on/off, mirroring the idempotent
// showRaw toggle. It always toggles — even with no findings — so the strip can
// surface an explicit empty state rather than letting i do nothing. Navigation
// is still only hijacked when findings exist (see InsightsActive).
func (p *PlanContext) ToggleInsights() {
	p.showInsights = !p.showInsights
}

// MoveInsightCursor advances the strip selection by delta, clamping into
// [0, len(findings)-1]. A move with no findings snaps to 0.
func (p *PlanContext) MoveInsightCursor(delta int) {
	if len(p.findings) == 0 {
		p.insightCursor = 0
		return
	}
	next := max(p.insightCursor+delta, 0)
	if next >= len(p.findings) {
		next = len(p.findings) - 1
	}
	p.insightCursor = next
}

// JumpToSelectedFinding moves the tree cursor onto the selected finding's node,
// expanding every ancestor so it is visible (reusing findPath, root-relative).
// No-op when there are no findings or the finding's node is unreachable.
func (p *PlanContext) JumpToSelectedFinding() {
	if p.insightCursor < 0 || p.insightCursor >= len(p.findings) {
		return
	}
	target := p.findings[p.insightCursor].NodeRef
	path := findPath(p.plan.Node, target)
	if path == nil {
		return
	}
	for _, n := range path {
		if n == target {
			break
		}
		delete(p.collapsed, n)
	}
	vis := p.VisibleNodes()
	for i, v := range vis {
		if v.Node == target {
			p.cursor = i
			return
		}
	}
}

// HandleRender writes the current view body. Routes through the GuiDriver
// using writeView so a nil driver (unit tests / pre-wire) is a silent
// no-op.
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
// needing the GuiDriver path.
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
	// useColor honors NO_COLOR for any coloring (heat + severity glyphs).
	// colorize additionally requires enough nodes for the heat percentiles
	// to be meaningful; severity coloring is not subject to that gate.
	useColor := !theme.IsMonochrome()
	colorize := useColor && len(vis) >= MinVisibleForColoring
	var thresholds [4]float64
	if colorize {
		thresholds = costThresholds(vis, p.plan.Analyzed)
	}
	var b strings.Builder
	if p.showInsights {
		p.renderInsightsStrip(&b, useColor)
	}
	for i, v := range vis {
		glyph := glyphFor(v.Node, p.collapsed[v.Node])
		marker := "  "
		if i == p.cursor {
			marker = "> "
		}
		indent := strings.Repeat("  ", v.Depth)

		// Build the whole row first, then heat-color it as a unit so the
		// glyph, op name, and metrics all carry the node's heat — not just
		// the cost token.
		var row strings.Builder
		fmt.Fprintf(&row, "%s%s%s %s", marker, indent, glyph, v.Node.Op)
		fmt.Fprintf(&row, "  cost=%s rows=%d", formatCost(v.Node.Cost), v.Node.EstRows)
		if p.plan.Analyzed {
			fmt.Fprintf(&row, "  actual_time=%s actual_rows=%d loops=%d",
				formatCost(v.Node.ActualTotalTime), v.Node.ActualRows, v.Node.Loops)
		}
		line := row.String()
		if colorize {
			line = applyHeatColor(line, heatBasis(v.Node, p.plan.Analyzed), thresholds)
		}
		b.WriteString(line)

		// The finding badge keeps its own severity color, appended after the
		// heat-colored row so the two color spans never overlap.
		if f, ok := p.findingByNode[v.Node]; ok {
			fmt.Fprintf(&b, "  %s", badgeFor(f, useColor))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// insightIndent prefixes wrapped explanation lines so they sit under their
// finding's title rather than at the strip's left margin.
const insightIndent = "    "

// renderInsightsStrip writes the ranked findings list to b: a header, then per
// finding a marked title line ("> " on the selected one) followed by the
// explanation and the "Fix: " suggestion, each word-wrapped to the viewport so
// long diagnostics never spill past the panel. With no findings it writes an
// explicit empty state so a toggled-on strip always shows feedback.
func (p *PlanContext) renderInsightsStrip(b *strings.Builder, colorOn bool) {
	if len(p.findings) == 0 {
		b.WriteString("INSIGHTS (0) — no issues detected\n\n")
		return
	}
	fmt.Fprintf(b, "INSIGHTS (%d)\n", len(p.findings))
	// wrapLabel treats a width <= 0 as "do not wrap", which is the pre-layout
	// and unit-test path; otherwise reserve the indent so wrapped lines fit.
	wrapWidth := 0
	if p.viewportWidth > 0 {
		wrapWidth = p.viewportWidth - len(insightIndent)
	}
	for i, f := range p.findings {
		marker := "  "
		if i == p.insightCursor {
			marker = "> "
		}
		// Color the glyph+title by severity; leave the explanation neutral.
		label := severityGlyph(f.Severity) + " " + f.Title
		if colorOn {
			label = applySeverityColor(label, f.Severity)
		}
		fmt.Fprintf(b, "%s%s\n", marker, label)
		for _, line := range wrapLabel(f.Explanation, wrapWidth) {
			fmt.Fprintf(b, "%s%s\n", insightIndent, line)
		}
		if f.SuggestedFix != "" {
			for _, line := range wrapLabel("Fix: "+f.SuggestedFix, wrapWidth) {
				fmt.Fprintf(b, "%s%s\n", insightIndent, line)
			}
		}
	}
	b.WriteByte('\n')
}

// badgeFor renders the inline tree-row badge for a flagged node: a severity
// glyph and the finding title (e.g. "⚠ Bad row estimate"), colored by
// severity when colorOn.
func badgeFor(f plandoctor.Finding, colorOn bool) string {
	badge := severityGlyph(f.Severity) + " " + f.Title
	if colorOn {
		return applySeverityColor(badge, f.Severity)
	}
	return badge
}

// severityColor maps a finding severity to an ANSI SGR foreground escape:
// blocker→red, warn→yellow, info→cyan. Cyan is deliberately distinct from
// the heat palette (yellow/red) so severity reads as its own dimension.
func severityColor(s plandoctor.Severity) string {
	switch s {
	case plandoctor.SeverityBlocker:
		return "\x1b[31m" // red
	case plandoctor.SeverityWarn:
		return "\x1b[33m" // yellow
	default:
		return "\x1b[36m" // cyan (info)
	}
}

// applySeverityColor wraps s in the severity's SGR escape + reset.
func applySeverityColor(s string, sev plandoctor.Severity) string {
	return severityColor(sev) + s + "\x1b[0m"
}

// severityGlyph maps a finding severity to a single-rune marker. Warn and
// Blocker share the warning sign; Info uses the information sign.
func severityGlyph(s plandoctor.Severity) string {
	if s == plandoctor.SeverityInfo {
		return "ℹ"
	}
	return "⚠"
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

// heatBasis returns the EXCLUSIVE (self) magnitude that drives heat coloring
// and JumpHeaviest for a single node: SelfTime when the plan carries ANALYZE
// actuals, else SelfCost. The value is clamped to >=0 — Self* can be negative
// (parallel workers / InitPlan / Append summing child totals past the parent),
// and a negative basis would sort to the bottom of the percentile buckets and
// RE-INVERT the heat map. The stored Self* fields stay raw for T4/T5 honesty;
// only the coloring/ranking basis is clamped.
func heatBasis(n *models.PlanNode, analyzed bool) float64 {
	v := n.SelfCost
	if analyzed {
		v = n.SelfTime
	}
	if v < 0 {
		return 0
	}
	return v
}

// costThresholds returns [P50, P75, P90, P95] of the visible nodes'
// self-basis (SelfTime when analyzed, else SelfCost; clamped >=0 via
// heatBasis). Used by applyHeatColor to bucket each node into a
// neutral→warning gradient. Returns zero thresholds when vis is empty.
func costThresholds(vis []VisibleNode, analyzed bool) [4]float64 {
	if len(vis) == 0 {
		return [4]float64{}
	}
	costs := make([]float64, len(vis))
	for i, v := range vis {
		costs[i] = heatBasis(v.Node, analyzed)
	}
	sort.Float64s(costs)
	pick := func(p float64) float64 {
		idx := max(int(p*float64(len(costs)-1)), 0)
		if idx >= len(costs) {
			idx = len(costs) - 1
		}
		return costs[idx]
	}
	return [4]float64{pick(0.50), pick(0.75), pick(0.90), pick(0.95)}
}

// applyHeatColor wraps s (a whole rendered tree row) in an ANSI SGR escape
// whose color reflects the self-basis percentile bucket (neutral → yellow →
// red → bold red). The mapping is intentionally simple — we lean on the
// existing ANSI 8-color palette so terminals without truecolor still render
// the gradient. The caller is responsible for the IsMonochrome / vis-count
// gate; applyHeatColor itself does not consult theme state.
func applyHeatColor(s string, basis float64, t [4]float64) string {
	const (
		reset   = "\x1b[0m"
		neutral = "" // no escape — default fg
		yellow  = "\x1b[33m"
		red     = "\x1b[31m"
		bred    = "\x1b[1;31m"
	)
	var code string
	switch {
	case basis >= t[3]: // ≥ P95
		code = bred
	case basis >= t[2]: // ≥ P90
		code = red
	case basis >= t[1]: // ≥ P75
		code = yellow
	case basis >= t[0]: // ≥ P50
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
