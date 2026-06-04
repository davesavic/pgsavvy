package editor

import (
	"strings"
	"unicode/utf8"
)

const (
	// Bright-black (dark gray) background — works in OutputNormal
	// (8-color) mode. SGR 100 = bright black bg.
	ansiSelBgOn  = "\x1b[100m"
	ansiSelBgOff = "\x1b[49m"
	ansiReset    = "\x1b[0m"
	ansiResetSel = "\x1b[0;100m"

	// Yellow background for the transient post-yank flash. SGR 43 =
	// yellow bg (8-color, works in OutputNormal). Mirrors the selection
	// constant precedent; no theme integration.
	ansiYankBgOn  = "\x1b[43m"
	ansiYankBgOff = "\x1b[49m"
	ansiResetYank = "\x1b[0;43m"
)

func ApplySelectionOverlay(highlighted string, sel Range) string {
	if highlighted == "" {
		return highlighted
	}

	startLine, startCol := sel.Start.Line, sel.Start.Col
	endLine, endCol := sel.End.Line, sel.End.Col
	if posLess(sel.End, sel.Start) {
		startLine, startCol = sel.End.Line, sel.End.Col
		endLine, endCol = sel.Start.Line, sel.Start.Col
	}

	// Visual mode is inclusive: both Start and End characters are
	// selected. Adjust endCol +1 to convert to half-open for the
	// character-wise renderer. Line-wise ignores columns.
	if !sel.LineWise {
		if startLine == endLine {
			endCol++
		} else {
			endCol++
		}
	}

	lines := strings.Split(highlighted, "\n")
	for i := range lines {
		if i < startLine || i > endLine {
			continue
		}

		if sel.LineWise {
			lines[i] = tintWholeLine(lines[i])
			continue
		}

		if i > startLine && i < endLine {
			lines[i] = tintWholeLine(lines[i])
			continue
		}

		var cs, ce int
		toEnd := false
		switch {
		case i == startLine && i == endLine:
			cs, ce = startCol, endCol
		case i == startLine:
			cs = startCol
			toEnd = true
		case i == endLine:
			cs, ce = 0, endCol
		}

		lines[i] = tintRuneRange(lines[i], cs, ce, toEnd)
	}

	return strings.Join(lines, "\n")
}

func tintWholeLine(line string) string {
	replaced := strings.ReplaceAll(line, ansiReset, ansiResetSel)
	return ansiSelBgOn + replaced + ansiReset
}

func tintRuneRange(line string, colStart, colEnd int, toEnd bool) string {
	var out strings.Builder
	out.Grow(len(line) + 30)

	runeIdx := 0
	inSel := false
	i := 0

	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && !isCSITerminator(line[j]) {
				j++
			}
			if j < len(line) {
				j++
			}
			esc := line[i:j]
			if inSel && esc == ansiReset {
				out.WriteString(ansiResetSel)
			} else {
				out.WriteString(esc)
			}
			i = j
			continue
		}

		_, size := utf8.DecodeRuneInString(line[i:])

		if runeIdx == colStart && !inSel {
			out.WriteString(ansiSelBgOn)
			inSel = true
		}

		out.WriteString(line[i : i+size])
		runeIdx++

		if inSel && !toEnd && runeIdx == colEnd {
			out.WriteString(ansiSelBgOff)
			inSel = false
		}

		i += size
	}

	if inSel {
		out.WriteString(ansiReset)
	}

	return out.String()
}

func isCSITerminator(b byte) bool {
	return b >= 0x40 && b <= 0x7E
}

// ApplyYankFlashOverlay tints r over highlighted with the yank-flash
// background. Unlike ApplySelectionOverlay it treats r as HALF-OPEN
// [Start, End) exactly as given — callers pass half-open ranges, so
// there is no inclusive-to-half-open endCol++ adjustment. Panic-safe:
// empty input, out-of-bounds lines/cols, or a startLine past the line
// count return byte-identical output.
func ApplyYankFlashOverlay(highlighted string, r Range) string {
	if highlighted == "" {
		return highlighted
	}

	startLine, startCol := r.Start.Line, r.Start.Col
	endLine, endCol := r.End.Line, r.End.Col
	if posLess(r.End, r.Start) {
		startLine, startCol = r.End.Line, r.End.Col
		endLine, endCol = r.Start.Line, r.Start.Col
	}

	lines := strings.Split(highlighted, "\n")
	for i := range lines {
		if i < startLine || i > endLine {
			continue
		}

		if r.LineWise {
			lines[i] = tintWholeLineYank(lines[i])
			continue
		}

		if i > startLine && i < endLine {
			lines[i] = tintWholeLineYank(lines[i])
			continue
		}

		var cs, ce int
		toEnd := false
		switch {
		case i == startLine && i == endLine:
			cs, ce = startCol, endCol
		case i == startLine:
			cs = startCol
			toEnd = true
		case i == endLine:
			cs, ce = 0, endCol
		}

		lines[i] = tintRuneRangeYank(lines[i], cs, ce, toEnd)
	}

	return strings.Join(lines, "\n")
}

func tintWholeLineYank(line string) string {
	replaced := strings.ReplaceAll(line, ansiReset, ansiResetYank)
	return ansiYankBgOn + replaced + ansiReset
}

func tintRuneRangeYank(line string, colStart, colEnd int, toEnd bool) string {
	var out strings.Builder
	out.Grow(len(line) + 30)

	runeIdx := 0
	inSel := false
	i := 0

	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && !isCSITerminator(line[j]) {
				j++
			}
			if j < len(line) {
				j++
			}
			esc := line[i:j]
			if inSel && esc == ansiReset {
				out.WriteString(ansiResetYank)
			} else {
				out.WriteString(esc)
			}
			i = j
			continue
		}

		_, size := utf8.DecodeRuneInString(line[i:])

		if runeIdx == colStart && !inSel {
			out.WriteString(ansiYankBgOn)
			inSel = true
		}

		out.WriteString(line[i : i+size])
		runeIdx++

		if inSel && !toEnd && runeIdx == colEnd {
			out.WriteString(ansiYankBgOff)
			inSel = false
		}

		i += size
	}

	if inSel {
		out.WriteString(ansiReset)
	}

	return out.String()
}
