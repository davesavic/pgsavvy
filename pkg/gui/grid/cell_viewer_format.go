package grid

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/davesavic/pgsavvy/pkg/models"
)

const (
	ViewerCellNULL  = "(NULL)"
	ViewerCellEmpty = "(empty string)"
)

const (
	maxViewerCellBytes   = 1 << 20 // 1MB synchronous gate
	maxTruncatedPreview  = 1024    // 1KB preview
)

func FormatViewerBody(value any, col models.ColumnMeta, pretty bool) (body string, parseFailed bool) {
	raw, parseFailed := formatViewerBodyRaw(value, col, pretty)
	if raw == ViewerCellNULL || raw == ViewerCellEmpty {
		return raw, false
	}
	style := styleForCell(value, col)
	return wrapWithStyle(raw, style), parseFailed
}

func FormatViewerBodyPlain(value any, col models.ColumnMeta, pretty bool) string {
	raw, _ := formatViewerBodyRaw(value, col, pretty)
	return raw
}

func formatViewerBodyRaw(value any, col models.ColumnMeta, pretty bool) (body string, parseFailed bool) {
	if value == nil {
		return ViewerCellNULL, false
	}

	if t, ok := value.(time.Time); ok {
		s := t.Format("2006-01-02 15:04:05.999999-07:00")
		if s == "" {
			return ViewerCellEmpty, false
		}
		return s, false
	}

	kind := classifyColumn(col)

	if kind != kindBytes && kind != kindJSON {
		if body, ok := formatViewerArray(value); ok {
			return body, false
		}
	}

	switch kind {
	case kindBytes:
		return formatViewerBytes(value)
	case kindJSON:
		return formatViewerJSON(value, pretty)
	default:
		s := formatScalar(value)
		if s == "" {
			return ViewerCellEmpty, false
		}
		return s, false
	}
}

func formatViewerJSON(value any, pretty bool) (body string, parseFailed bool) {
	s := FormatJSONValue(value)
	if s == "" {
		return ViewerCellEmpty, false
	}
	if len(s) > maxViewerCellBytes {
		return truncatedPreview(s), false
	}
	if !pretty {
		return s, false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s + "\n(parse failed)", true
	}
	b, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		return s + "\n(parse failed)", true
	}
	return string(b), false
}

func formatViewerBytes(value any) (body string, parseFailed bool) {
	b, ok := value.([]byte)
	if !ok {
		s := fmt.Sprintf("%v", value)
		if s == "" {
			return ViewerCellEmpty, false
		}
		return s, false
	}
	if utf8.Valid(b) {
		raw := string(b)
		if len(raw) > maxViewerCellBytes {
			return truncatedPreview(raw), false
		}
		if raw == "" {
			return ViewerCellEmpty, false
		}
		return raw, false
	}
	raw := hex.Dump(b)
	if len(raw) > maxViewerCellBytes {
		return truncatedPreview(raw), false
	}
	return raw, false
}

func formatViewerArray(v any) (body string, ok bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return "", false
	}
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		elem := rv.Index(i).Interface()
		b.WriteString(formatScalar(elem))
	}
	raw := b.String()
	if len(raw) > maxViewerCellBytes {
		return truncatedPreview(raw), true
	}
	if raw == "" {
		return ViewerCellEmpty, true
	}
	return raw, true
}

func truncatedPreview(s string) string {
	if len(s) <= maxTruncatedPreview {
		return s
	}
	return fmt.Sprintf("%s... cell too large (%d bytes)", s[:maxTruncatedPreview], len(s))
}

type WrappedBody struct {
	lines      []string
	totalLines int
	totalBytes int
}

func (w WrappedBody) Bytes() int        { return w.totalBytes }
func (w WrappedBody) Lines() int        { return w.totalLines }
func (w WrappedBody) Slice() []string   { return w.lines }

func WrapWindow(body string, width, rowOffset, viewHeight int) WrappedBody {
	if width <= 0 {
		width = 1
	}
	if viewHeight <= 0 {
		return WrappedBody{totalBytes: len(body)}
	}

	var visible []string
	totalLines := 0

	remaining := body
	for remaining != "" {
		idx := strings.IndexByte(remaining, '\n')
		var line string
		if idx >= 0 {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		} else {
			line = remaining
			remaining = ""
		}

		totalLines = wrapSourceLine(line, width, rowOffset, viewHeight, totalLines, &visible)
	}

	return WrappedBody{
		lines:      visible,
		totalLines: totalLines,
		totalBytes: len(body),
	}
}

func wrapSourceLine(line string, width, rowOffset, viewHeight, curLine int, visible *[]string) int {
	if line == "" {
		if inWindow(curLine, rowOffset, viewHeight) {
			*visible = append(*visible, "")
		}
		return curLine + 1
	}
	var b strings.Builder
	used := 0
	for _, r := range line {
		w := runewidth.RuneWidth(r)
		if used+w > width && used > 0 {
			if inWindow(curLine, rowOffset, viewHeight) {
				*visible = append(*visible, b.String())
			}
			curLine++
			b.Reset()
			used = 0
		}
		b.WriteRune(r)
		used += w
	}
	if inWindow(curLine, rowOffset, viewHeight) {
		*visible = append(*visible, b.String())
	}
	return curLine + 1
}

func inWindow(line, offset, height int) bool {
	return line >= offset && line < offset+height
}
