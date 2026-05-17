package status

import (
	"strings"

	"github.com/davesavic/dbsavvy/pkg/gui/presentation"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

const (
	sectionSep = " | "
	optionSep  = " "
)

// BuildStatusLine renders the plain text shown in the status slot.
//
// Layout: "<icon> <label> | [RO] <option1> <option2> … | <Tr.OptionsBarMore>".
//
// Each section is omitted when empty:
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
func BuildStatusLine(activeConn *models.Connection, options []string, tr *i18n.TranslationSet) string {
	if tr == nil {
		return ""
	}

	var sections []string

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
