package exporter

import (
	"bytes"
	"io"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/grid"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// markdownFormat implements Format for a GitHub-Flavored Markdown table.
//
// Buffered: Header writes the header + separator lines into an internal
// buffer, Row appends a data line, Footer flushes the buffer to w.
//
// Cell escaping rules:
//   - Values pass through grid.SanitizeCellEscapes (AD-16) to strip ANSI
//     and other control sequences.
//   - '|' is escaped as '\|' so it doesn't terminate the cell.
//   - '\r\n', '\r' and '\n' are replaced with a single space so each row
//     stays on one line.
//   - NULL renders as an empty cell.
//
// Cells are emitted without padding (e.g. "|v1|v2|") — minimal width,
// still valid markdown.
type markdownFormat struct {
	cols []models.ColumnMeta
	buf  bytes.Buffer
}

// NewMarkdown returns a buffered Markdown-table writer.
func NewMarkdown() Format { return &markdownFormat{} }

func (m *markdownFormat) Name() string      { return "Markdown" }
func (m *markdownFormat) Ext() string       { return "md" }
func (m *markdownFormat) IsStreaming() bool { return false }

func (m *markdownFormat) Header(cols []models.ColumnMeta, _ io.Writer) error {
	m.cols = cols
	m.buf.Reset()
	m.buf.WriteByte('|')
	for _, c := range cols {
		m.buf.WriteString(escapeMarkdownCell(c.Name))
		m.buf.WriteByte('|')
	}
	m.buf.WriteByte('\n')
	m.buf.WriteByte('|')
	for range cols {
		m.buf.WriteString("---|")
	}
	m.buf.WriteByte('\n')
	return nil
}

func (m *markdownFormat) Row(r models.Row, _ io.Writer) error {
	m.buf.WriteByte('|')
	for i := range m.cols {
		var s string
		if i < len(r.Values) && r.Values[i] != nil {
			s = escapeMarkdownCell(grid.RenderCellText(r.Values[i], m.cols[i]))
		}
		m.buf.WriteString(s)
		m.buf.WriteByte('|')
	}
	m.buf.WriteByte('\n')
	return nil
}

func (m *markdownFormat) Footer(w io.Writer) error {
	_, err := w.Write(m.buf.Bytes())
	return err
}

// escapeMarkdownCell sanitizes a cell value for inline markdown table use.
var markdownCellReplacer = strings.NewReplacer(
	"\r\n", " ",
	"\r", " ",
	"\n", " ",
	"|", `\|`,
)

func escapeMarkdownCell(s string) string {
	s = grid.SanitizeCellEscapes(s)
	return markdownCellReplacer.Replace(s)
}
