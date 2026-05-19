package status

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const (
	sectionSep = " | "
	optionSep  = " "
)

// ANSI SGR pair used to tint the connection header (icon + label) with
// the profile's `color:` field. dbsavvy-sgc: the header MUST be visibly
// distinct after connect so the user can tell at a glance which
// connection is active. Recognised colour names map to the standard
// 8-colour ANSI palette; hex / unknown tokens fall through to an
// untinted header (we never emit a malformed escape).
const (
	ansiResetSGR = "\x1b[0m"
)

// BuildStatusLine renders the plain text shown in the status slot.
//
// Layout: "<modeLabel> | <icon> <label> | [RO] <option1> <option2> … | <Tr.OptionsBarMore>".
//
// Each section is omitted when empty:
//   - the mode banner slot is omitted when modeLabel is the empty string
//     (no leading separator);
//   - the connection header slot is omitted when activeConn is nil or the
//     connection has neither an icon nor a label;
//   - the [RO] tag is omitted unless activeConn.ReadOnly == true;
//   - the options list is omitted when options is empty;
//   - the trailing "more" hint is always appended (and is the only
//     guaranteed section), so the returned string is never empty when tr
//     is non-nil.
//
// A nil tr yields the empty string; the rendering layer treats that as
// "skip the status slot entirely".
func BuildStatusLine(modeLabel string, activeConn *models.Connection, options []string, tr *i18n.TranslationSet) string {
	if tr == nil {
		return ""
	}

	var sections []string

	if modeLabel != "" {
		sections = append(sections, modeLabel)
	}

	if header := presentation.HeaderTextFor(activeConn); header != "" {
		sections = append(sections, tintHeaderForConn(header, activeConn))
	}

	var mid []string
	if activeConn != nil && activeConn.ReadOnly && tr.ReadOnlyTag != "" {
		mid = append(mid, tr.ReadOnlyTag)
	}
	mid = append(mid, options...)
	if len(mid) > 0 {
		sections = append(sections, strings.Join(mid, optionSep))
	}

	sections = append(sections, tr.OptionsBarMore)

	return strings.Join(sections, sectionSep)
}

// tintHeaderForConn wraps the header text with an ANSI SGR foreground
// pair keyed off conn.Color when conn carries a recognised colour name.
// Unrecognised values (hex codes, empty, nil conn) collapse to the bare
// header — the rendering layer never emits a malformed escape. Mirrors
// the cell-content approach used by status_render's toast styler.
func tintHeaderForConn(header string, conn *models.Connection) string {
	if conn == nil || conn.Color == "" {
		return header
	}
	sgr := ansiSGRForColor(conn.Color)
	if sgr == "" {
		return header
	}
	return sgr + header + ansiResetSGR
}

// ansiSGRForColor maps an 8-colour name onto its ANSI foreground SGR.
// Unknown tokens (hex codes, names not in the standard palette, empty)
// return "" so callers can fall back to no tinting.
func ansiSGRForColor(s string) string {
	switch strings.ToLower(s) {
	case "black":
		return "\x1b[30m"
	case "red":
		return "\x1b[31m"
	case "green":
		return "\x1b[32m"
	case "yellow":
		return "\x1b[33m"
	case "blue":
		return "\x1b[34m"
	case "magenta":
		return "\x1b[35m"
	case "cyan":
		return "\x1b[36m"
	case "white":
		return "\x1b[37m"
	default:
		return ""
	}
}

// LabelForMode maps a Mode to its i18n banner string. ModeNormal returns
// the empty string by default; pass forceShowNormal=true to receive the
// "-- NORMAL --" label (used when the focused context is editable so the
// banner stays visible while typing). All other modes return their
// tr.Mode* label regardless of forceShowNormal.
//
// A nil tr returns the empty string for every mode.
func LabelForMode(m types.Mode, tr *i18n.TranslationSet, forceShowNormal bool) string {
	if tr == nil {
		return ""
	}
	switch m {
	case types.ModeNormal:
		if forceShowNormal {
			return tr.ModeNormal
		}
		return ""
	case types.ModeInsert:
		return tr.ModeInsert
	case types.ModeVisual:
		return tr.ModeVisual
	case types.ModeVisualLine:
		return tr.ModeVisualLine
	case types.ModeVisualBlock:
		return tr.ModeVisualBlock
	case types.ModeOperatorPending:
		return tr.ModeOperatorPending
	case types.ModeCommand:
		return tr.ModeCommand
	case types.ModeReplace:
		return tr.ModeReplace
	default:
		return ""
	}
}
