package status

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// pendingIndicatorCollapseWidth is the width threshold at which
// BuildPendingIndicator collapses `[N pending]` to `[N]`. Picked so a
// terminal narrower than the collapsing width still shows the count
// without eating the rest of the status line. Z1 passes the actual
// available width when wiring the indicator into the bar.
const pendingIndicatorCollapseWidth = 40

// BuildPendingIndicator returns the status-bar indicator for the staged
// edit set. The return contract:
//
//   - set == nil OR set.IsEmpty()         → ""  (no slot)
//   - set.Count() > 0, availableWidth ≥
//     pendingIndicatorCollapseWidth        → "[N pending]"
//   - set.Count() > 0, availableWidth <
//     pendingIndicatorCollapseWidth        → "[N]"
//
// When conn carries any destructive flag (ConfirmWrites, ReadOnly, or
// ConfirmDDL) AND conn.Color is a recognised palette colour, the
// returned string is wrapped in an ANSI SGR foreground escape so the
// indicator stands out against the rest of the bar. Unrecognised colour
// tokens (hex, blank, unknown name) collapse to the untinted form.
//
// availableWidth is the budget the caller plans to give the indicator
// (typically terminalWidth * 0.6 per the amendment). Pass 0 to force
// the collapsed form unconditionally; pass any large positive number to
// force the expanded form.
func BuildPendingIndicator(set *models.PendingEditSet, conn *models.Connection, availableWidth int) string {
	if set == nil || set.IsEmpty() {
		return ""
	}
	count := set.Count()
	expanded := fmt.Sprintf("[%d pending]", count)
	collapsed := fmt.Sprintf("[%d]", count)

	body := expanded
	if availableWidth < pendingIndicatorCollapseWidth || len(expanded) > availableWidth {
		body = collapsed
	}

	if !connHasDestructiveFlag(conn) {
		return body
	}
	sgr := ansiSGRForColor(conn.Color)
	if sgr == "" {
		return body
	}
	return sgr + body + ansiResetSGR
}

// BuildDisabledEditCellOption returns the options-bar segment shown when
// inline editing is disabled. The output is "[i] edit cell — disabled:
// <reason>" when reason is non-empty, or "[i] edit cell — disabled"
// otherwise. The reason text MUST come from GridView.DisabledReason
// (F2 frozen strings) so the wording stays in lock-step with the
// introspection layer. Z1 wires this into the options slice passed to
// BuildStatusLine.
//
// Returns "" when editable is true — the caller renders no segment in
// that case. ADR-24.
func BuildDisabledEditCellOption(editable bool, reason string) string {
	if editable {
		return ""
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "[i] edit cell — disabled"
	}
	return "[i] edit cell — disabled: " + reason
}

// connHasDestructiveFlag reports whether conn carries any flag that
// signals "writes against this connection deserve user attention" —
// the same set the indicator tints.
func connHasDestructiveFlag(conn *models.Connection) bool {
	if conn == nil {
		return false
	}
	return conn.ConfirmWrites || conn.ReadOnly || conn.ConfirmDDL
}
