// Package textcells reconciles the pinned gocui and tcell grapheme widths.
package textcells

import (
	"strings"

	"github.com/gdamore/tcell/v3"
	"github.com/rivo/uniseg"
)

// Measure uses a private buffer to follow tcell's startup locale policy without
// touching the global screen. Its zero value is ready; do not share it concurrently.
type Measure struct{ buffer tcell.CellBuffer }

func (m *Measure) ClusterWidth(cluster string) int {
	if len(cluster) == 1 && cluster[0] >= ' ' && cluster[0] <= '~' {
		return 1
	}
	return m.clusterWidth(cluster, uniseg.StringWidth(cluster))
}

func (m *Measure) clusterWidth(cluster string, logicalWidth int) int {
	if len(cluster) == 1 && cluster[0] >= ' ' && cluster[0] <= '~' {
		return 1
	}
	if w, _ := m.buffer.Size(); w == 0 {
		m.buffer.Resize(2, 1)
	}
	_, width := m.buffer.Put(0, 0, cluster, tcell.StyleDefault)
	return max(width, logicalWidth)
}

// StringWidth measures plain, unpadded text, not ANSI-decorated output.
func (m *Measure) StringWidth(s string) int {
	if printableASCII(s) {
		return len(s)
	}
	width := 0
	clusters := uniseg.NewGraphemes(s)
	for clusters.Next() {
		width += m.clusterWidth(clusters.Str(), clusters.Width())
	}
	return width
}

// Prefix retains whole graphemes without adding an overflow marker or padding.
func (m *Measure) Prefix(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if printableASCII(s) {
		return s[:min(len(s), width)]
	}
	end, used := 0, 0
	clusters := uniseg.NewGraphemes(s)
	for clusters.Next() {
		used += m.clusterWidth(clusters.Str(), clusters.Width())
		if used > width {
			break
		}
		_, end = clusters.Positions()
	}
	return s[:end]
}

func WriteCluster(b *strings.Builder, cluster string, width int) {
	b.WriteString(cluster)
	// gocui advances one column for VS15 emoji that tcell draws in two. The
	// continuation space advances gocui; tcell skips the covered cell on draw.
	b.WriteString(strings.Repeat(" ", width-uniseg.StringWidth(cluster)))
}

// Render adds native continuation cells exactly once, after layout and styling.
// ASCII bytes (including trusted SGR sequences) pass through unchanged; callers
// must sanitize untrusted controls and keep styling outside grapheme clusters.
func Render(s string) string {
	var b strings.Builder
	var m Measure
	written := 0
	for consumed := 0; consumed < len(s); {
		next := consumed
		for next < len(s) && s[next] < 0x80 {
			next++
		}
		if next == len(s) {
			break
		}
		// The final ASCII rune may start a combining or keycap cluster.
		start := max(consumed, next-1)
		cluster, _, logicalWidth, _ := uniseg.FirstGraphemeClusterInString(s[start:], -1)
		consumed = start + len(cluster)
		if extra := m.clusterWidth(cluster, logicalWidth) - logicalWidth; extra > 0 {
			if written == 0 {
				b.Grow(len(s))
			}
			b.WriteString(s[written:consumed])
			b.WriteString(strings.Repeat(" ", extra))
			written = consumed
		}
	}
	if written == 0 {
		return s
	}
	b.WriteString(s[written:])
	return b.String()
}

func printableASCII(s string) bool {
	for i := range len(s) {
		if s[i] < ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}
