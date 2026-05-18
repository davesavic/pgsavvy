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
		sections = append(sections, header)
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
