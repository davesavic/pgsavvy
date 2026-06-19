package context

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// Pinned error lines for the FOREIGN_KEYS leaf. Asserted verbatim by tests.
const (
	fkOutboundErrorLine = "could not load outbound foreign keys"
	fkInboundErrorLine  = "could not load inbound foreign keys"
)

// Heading presentation. The "References ->" / "Referenced by <-" section
// headings carry the bold accent SGR (matching the active-tab colour) and
// their bodies are indented, so a heading reads as a section divider rather
// than another data row. Raw SGR mirrors the side-rail / grid approach
// (pkg/gui/context/side_rails_highlight.go) because pkg/gui/style.Sprint is
// still a no-op.
const (
	fkHeadingSGR = "\x1b[1;33m" // bold yellow
	fkAnsiReset  = "\x1b[0m"
	fkRowIndent  = "  "
)

// ForeignKeysContext renders the foreign-key relationships leaf of the
// TABLE_INSPECT tabbed popup: an outbound "References ->" section followed
// by an inbound "Referenced by <-" section. Render-only; T2 populates it.
type ForeignKeysContext struct {
	BaseContext

	deps Deps

	outbound []models.ForeignKey
	inbound  []models.ForeignKey

	outboundErr bool
	inboundErr  bool
}

// NewForeignKeysContext builds a ForeignKeysContext bound to the
// FOREIGN_KEYS key and view.
func NewForeignKeysContext(base BaseContext, deps Deps) *ForeignKeysContext {
	return &ForeignKeysContext{
		BaseContext: base,
		deps:        deps,
	}
}

// SetForeignKeys replaces both FK direction slices. A self-referencing FK
// correctly appears in BOTH sections (it is both outbound and inbound).
func (c *ForeignKeysContext) SetForeignKeys(outbound, inbound []models.ForeignKey) {
	c.outbound = outbound
	c.inbound = inbound
}

// SetError flags the named direction ("outbound" or "inbound") as failed
// to load, pinning its error line on the next render.
func (c *ForeignKeysContext) SetError(dir string, errored bool) {
	if dir == "outbound" {
		c.outboundErr = errored
		return
	}
	c.inboundErr = errored
}

// HandleRender writes both FK sections into the view as a single scroll:
// "References ->" (outbound) then "Referenced by <-" (inbound).
func (c *ForeignKeysContext) HandleRender() error {
	deps := c.deps
	viewName := c.GetViewName()
	body := c.BodyText()
	writeView(deps, func() error {
		return deps.GuiDriver.SetContent(viewName, body)
	})
	return nil
}

// BodyText returns both FK sections the leaf renders, so the TABLE_INSPECT
// container can compose a stats header above it (bodyTextRenderer).
func (c *ForeignKeysContext) BodyText() string {
	return strings.Join([]string{
		fkSection("References ->", c.outbound, c.outboundErr, fkOutboundErrorLine),
		fkSection("Referenced by <-", c.inbound, c.inboundErr, fkInboundErrorLine),
	}, "\n\n")
}

// fkSection renders one labeled FK section: the heading, then in priority
// the pinned error line, the "No foreign keys" empty-state, or one row per
// FK.
func fkSection(heading string, fks []models.ForeignKey, errored bool, errLine string) string {
	return fkHeadingSGR + heading + fkAnsiReset + "\n" + fkIndent(fkSectionBody(fks, errored, errLine))
}

// fkIndent prefixes every line of body with fkRowIndent so section bodies
// hang beneath their left-margin headings.
func fkIndent(body string) string {
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = fkRowIndent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func fkSectionBody(fks []models.ForeignKey, errored bool, errLine string) string {
	if errored {
		return errLine
	}
	if len(fks) == 0 {
		return "No foreign keys"
	}
	rows := make([]string, 0, len(fks))
	for i := range fks {
		rows = append(rows, fkRow(&fks[i]))
	}
	return strings.Join(rows, "\n")
}

// fkRow renders one FK as "cols -> refschema.reftable(refcols)" with
// optional "ON DELETE/ON UPDATE <action>" clauses. Single-column FKs use
// the bare form; composite FKs parenthesize both column lists. The
// referenced table is ALWAYS schema-qualified. Every DB string is
// SafeText-sanitized.
func fkRow(fk *models.ForeignKey) string {
	ref := config.SafeText(fk.RefSchema) + "." + config.SafeText(fk.RefTable)
	row := fkColumns(fk.Columns) + " -> " + ref + "(" + fkColumnList(fk.RefColumns) + ")"
	return row + fkActions(fk.OnDelete, fk.OnUpdate)
}

// fkColumns renders the referencing column list: a bare name for a
// single-column FK, or a parenthesized comma list for a composite FK.
func fkColumns(cols []string) string {
	if len(cols) > 1 {
		return "(" + fkColumnList(cols) + ")"
	}
	return fkColumnList(cols)
}

// fkColumnList joins SafeText-sanitized column names with ", ".
func fkColumnList(cols []string) string {
	safe := make([]string, 0, len(cols))
	for _, col := range cols {
		safe = append(safe, config.SafeText(col))
	}
	return strings.Join(safe, ", ")
}

// fkActions renders the referential-action clauses, omitting any clause
// whose action is the "NO ACTION" default.
func fkActions(onDelete, onUpdate string) string {
	return fkAction(" ON DELETE ", onDelete) + fkAction(" ON UPDATE ", onUpdate)
}

func fkAction(prefix, action string) string {
	if action == "NO ACTION" {
		return ""
	}
	return prefix + config.SafeText(action)
}
