package exporter

import (
	"io"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// Format renders a sequence of rows into a destination writer.
// Implementations may be streaming (Footer is a flush no-op) or buffered
// (Header/Row are no-ops; Footer emits the whole serialized result).
type Format interface {
	// Name returns the user-visible name (e.g., "CSV").
	Name() string

	// Ext returns the file extension WITHOUT a leading dot (e.g., "csv").
	Ext() string

	// IsStreaming reports whether this format can write row-by-row.
	// Buffered formats (JSON-array, Markdown) return false.
	IsStreaming() bool

	// Header writes any prologue (column header row, opening brace, etc.).
	Header(cols []models.ColumnMeta, w io.Writer) error

	// Row writes a single row. For buffered formats, this buffers in-memory.
	Row(row models.Row, w io.Writer) error

	// Footer writes any epilogue (closing bracket, flush, etc.). Called once.
	Footer(w io.Writer) error
}
