package status

import (
	"fmt"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const (
	sectionSep = " | "
	optionSep  = " · "
)

// spinnerGlyphs is the braille spinner frame set indexed by busy count.
// Precomputed at package init so the BuildStatusLine spinner segment is
// allocation-free per frame (the only allocation per call is the wrapping
// string returned to the caller; selecting the glyph itself is a single
// indexed []rune read). Order matches the Unicode dot-pattern walk used
// by ora / cli-spinners' "dots" preset.
var spinnerGlyphs = [...]rune{
	'⠋', '⠙', '⠹', '⠸', '⠼',
	'⠴', '⠦', '⠧', '⠇', '⠏',
}

// ANSI SGR pair used to tint the connection header (icon + label) with
// the profile's `color:` field. dbsavvy-sgc: the header MUST be visibly
// distinct after connect so the user can tell at a glance which
// connection is active. Recognised colour names map to the standard
// 8-colour ANSI palette; hex / unknown tokens fall through to an
// untinted header (we never emit a malformed escape).
const (
	ansiResetSGR    = "\x1b[0m"
	ansiYellowFgSGR = "\x1b[33m" // WarningFg: active transaction
	ansiRedFgSGR    = "\x1b[31m" // ErrorFg: aborted transaction
)

// BuildStatusLine renders the plain text shown in the status slot.
//
// Layout: "<modeLabel> | <spinner> | <icon> <label> | [RO] <option1> <option2> … | <Tr.OptionsBarMore>".
//
// Each section is omitted when empty:
//   - the mode banner slot is omitted when modeLabel is the empty string
//     (no leading separator);
//   - the spinner slot is omitted when busyCount <= 0 (quiescent); when
//     positive it renders a single braille glyph from a 10-frame cycle
//     indexed by spinnerFrame % 10. spinnerFrame is a wall-clock frame
//     counter (U8) advanced by a periodic re-render while any work is in
//     flight, so the glyph animates continuously even with a single
//     worker — busyCount is only the show/hide gate, not the index;
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
func BuildStatusLine(modeLabel string, activeConn *models.Connection, options []string, tr *i18n.TranslationSet, busyCount, spinnerFrame int64, txStatus models.TxStatus, savepoints []string) string {
	if tr == nil {
		return ""
	}

	var sections []string

	if modeLabel != "" {
		sections = append(sections, modeLabel)
	}

	if busyCount > 0 {
		// busyCount gates show/hide; the glyph index comes from the
		// wall-clock frame counter (U8) so a single long-running worker
		// still animates. Mod into the precomputed glyph table;
		// allocation-free aside from the single-rune string conversion the
		// section join consumes downstream. Mask the sign so a wrapped or
		// negative frame can't index out of range.
		idx := (spinnerFrame%int64(len(spinnerGlyphs)) + int64(len(spinnerGlyphs))) % int64(len(spinnerGlyphs))
		sections = append(sections, string(spinnerGlyphs[idx]))
	}

	if header := presentation.HeaderTextFor(activeConn); header != "" {
		sections = append(sections, tintHeaderForConn(header, activeConn))
	}

	if tag := txIndicator(txStatus, savepoints); tag != "" {
		sections = append(sections, tag)
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

// txIndicator renders the transaction status badge for the status bar.
// Returns "" when no transaction is active (zero-value TxStatus or
// committed/rolled-back). Active transactions render [TX] (no savepoints)
// or [TX:sp1,sp2] (with savepoints) in WarningFg (yellow). Aborted
// transactions render [TX*] in ErrorFg (red).
func txIndicator(st models.TxStatus, savepoints []string) string {
	switch st {
	case models.TxActive:
		if len(savepoints) > 0 {
			tag := fmt.Sprintf("[TX:%s]", strings.Join(savepoints, ","))
			return ansiYellowFgSGR + tag + ansiResetSGR
		}
		return ansiYellowFgSGR + "[TX]" + ansiResetSGR
	case models.TxAbortedInTx:
		return ansiRedFgSGR + "[TX*]" + ansiResetSGR
	default:
		return ""
	}
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
