package status

import (
	"fmt"
	"sort"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/theme"
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

// SpinnerGlyph returns the braille spinner glyph for the given wall-clock
// frame index, cycling through the same 10-frame "dots" set BuildStatusLine
// uses for the status bar. The frame is masked into range with the same
// sign-safe modulo idiom (status_bar.go:87) so a wrapped/negative frame never
// indexes out of bounds. Exported (T3 AD5/AD6a) so the CONNECTION_MANAGER
// modal can render the Active connect-stage row with the SAME animated glyph
// as the status-bar spinner.
func SpinnerGlyph(frame int64) rune {
	n := int64(len(spinnerGlyphs))
	idx := (frame%n + n) % n
	return spinnerGlyphs[idx]
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

// BuildStatusLine renders the plain text shown in the 2-row status slot.
// The returned string carries two lines joined by "\n":
//
//	line 1: "<modeLabel> | <spinner> | <icon> <label>"
//	line 2: "<txBadge> | <settings> | [RO] <option1> … | <Tr.OptionsBarMore>"
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
func BuildStatusLine(modeLabel string, activeConn *models.Connection, options []string, tr *i18n.TranslationSet, busyCount, spinnerFrame int64, txStatus models.TxStatus, savepoints []string, sessionSettings map[string]string) string {
	if tr == nil {
		return ""
	}

	// line1: mode banner / spinner / connection header.
	var line1 []string

	if modeLabel != "" {
		line1 = append(line1, modeLabel)
	}

	if busyCount > 0 {
		// busyCount gates show/hide; the glyph index comes from the
		// wall-clock frame counter (U8) so a single long-running worker
		// still animates. Mod into the precomputed glyph table;
		// allocation-free aside from the single-rune string conversion the
		// section join consumes downstream. Mask the sign so a wrapped or
		// negative frame can't index out of range.
		idx := (spinnerFrame%int64(len(spinnerGlyphs)) + int64(len(spinnerGlyphs))) % int64(len(spinnerGlyphs))
		line1 = append(line1, string(spinnerGlyphs[idx]))
	}

	if header := presentation.HeaderTextFor(activeConn); header != "" {
		line1 = append(line1, tintHeaderForConn(header, activeConn))
	}

	// line2: transaction badge / session settings / options / more-hint.
	var line2 []string

	if tag := txIndicator(txStatus, savepoints); tag != "" {
		line2 = append(line2, tag)
	}

	if tags := sessionSettingsTags(sessionSettings); len(tags) > 0 {
		line2 = append(line2, tags...)
	}

	var mid []string
	if activeConn != nil && activeConn.ReadOnly && tr.ReadOnlyTag != "" {
		mid = append(mid, tr.ReadOnlyTag)
	}
	mid = append(mid, options...)
	if len(mid) > 0 {
		line2 = append(line2, strings.Join(mid, optionSep))
	}

	// The "more" hint is the only guaranteed section; it stays the final
	// element of line 2 (tests assert HasSuffix on tr.OptionsBarMore).
	line2 = append(line2, tr.OptionsBarMore)

	first := strings.Join(line1, sectionSep)
	second := strings.Join(line2, sectionSep)
	if first == "" && second == "" {
		return ""
	}
	return strings.Join([]string{first, second}, "\n")
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

// SearchIndicator renders the active-search status segment for the
// status bar (dbsavvy-2ttm.5). Returns "" when active is false, so the
// caller appends nothing when no search is live on the focused result
// tab. Active searches render "search: <query> <cur>/<total>"; when
// total == 0 the " · no matches" suffix is appended using the shared
// optionSep so the empty-result case is visible at a glance.
func SearchIndicator(query string, cur, total int, active bool) string {
	if !active {
		return ""
	}
	seg := fmt.Sprintf("search: %s %d/%d", query, cur, total)
	if total == 0 {
		return seg + optionSep + "no matches"
	}
	return seg
}

// settingsSearchPathMaxLen is the maximum length of a search_path value
// before truncation. Values longer than this are clipped and suffixed
// with "..." to prevent status bar overflow.
const settingsSearchPathMaxLen = 40

// sessionSettingsTags renders bracketed [key=value] tags for the non-empty
// entries in the session settings map. search_path and role are emitted
// first (in that order); remaining keys follow in sorted order. Empty
// values are skipped. search_path values longer than
// settingsSearchPathMaxLen are truncated with "...".
func sessionSettingsTags(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}

	var tags []string
	// Priority keys first, in display order.
	for _, k := range []string{"search_path", "role"} {
		v, ok := m[k]
		if !ok || v == "" {
			continue
		}
		if k == "search_path" && len(v) > settingsSearchPathMaxLen {
			v = v[:settingsSearchPathMaxLen] + "..."
		}
		tags = append(tags, fmt.Sprintf("[%s=%s]", k, v))
	}
	// Remaining keys in sorted order.
	var rest []string
	for k := range m {
		if k == "search_path" || k == "role" {
			continue
		}
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		v := m[k]
		if v == "" {
			continue
		}
		tags = append(tags, fmt.Sprintf("[%s=%s]", k, v))
	}
	return tags
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
// return "" so callers can fall back to no tinting. Delegates to the shared
// theme.AnsiFgSGR converter.
func ansiSGRForColor(s string) string {
	return theme.AnsiFgSGR(s)
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
