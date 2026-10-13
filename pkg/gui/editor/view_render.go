package editor

import (
	"strings"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/editor/highlight"
)

// InteractiveRuneLimit bounds whole-document SQL lexing on the UI thread.
// Larger documents retain their text, but use a plain-text viewport and manual
// completion rather than scanning the document on every keystroke.
const InteractiveRuneLimit = 64 << 10

func (b *Buffer) IsLargeDocument() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.isLargeDocumentLocked()
}

func (b *Buffer) isLargeDocumentLocked() bool {
	remaining := InteractiveRuneLimit - max(0, len(b.Lines)-1)
	if remaining < 0 {
		return true
	}
	for _, line := range b.Lines {
		remaining -= len(line.Runes)
		if remaining < 0 {
			return true
		}
	}
	return remaining < 0
}

// PaintBuffer keeps document coordinates in the view, but materializes only
// visible rows for large documents. Never use the partial view as canonical SQL.
func PaintBuffer(v *gocui.View, buf *Buffer) {
	if v == nil || buf == nil {
		return
	}
	_, origin := v.Origin()
	content, start, count, cur, large, sel, flash := buf.viewSnapshot(origin, max(1, v.InnerHeight()))
	if !large {
		content = highlight.Highlight(content)
	}
	if sel != nil {
		content = ApplySelectionOverlay(content, *sel)
	}
	if flash != nil {
		content = ApplyYankFlashOverlay(content, *flash)
	}
	if large {
		// OverwriteLines assigns offsets directly, leaving a deferred trailing
		// newline/ANSI interpreter from the prior paint unless we reset it.
		v.Rewind()
		v.OverwriteLinesAndClearEverythingElse(count, start, content)
		v.SetOriginY(start)
		v.SetCursor(cur.Col, cur.Line-start)
	} else {
		v.SetContent(content)
		v.SetCursor(cur.Col, cur.Line)
	}
}

func (b *Buffer) viewSnapshot(origin, height int) (content string, start, count int, cur Position, large bool, sel, flash *Range) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	count, cur, large = len(b.Lines), b.Cursor, b.isLargeDocumentLocked()
	end := count
	if large {
		start = min(max(0, origin), max(0, count-height))
		if cur.Line < start || cur.Line >= start+height {
			start = min(max(0, cur.Line-height/2), max(0, count-height))
		}
		end = min(count, start+height)
	}
	var text strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			text.WriteByte('\n')
		}
		text.WriteString(string(b.Lines[i].Runes))
	}
	shift := func(r *Range) *Range {
		if r == nil {
			return nil
		}
		copy := *r
		copy.Start.Line -= start
		copy.End.Line -= start
		return &copy
	}
	return text.String(), start, count, cur, large, shift(b.Selection), shift(b.yankFlash)
}
