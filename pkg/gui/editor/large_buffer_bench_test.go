package editor

import (
	"strings"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func BenchmarkLargeBufferTyping(b *testing.B) {
	for _, tc := range []struct {
		name string
		text string
		pos  Position
	}{
		{"rows=100", strings.Repeat("SELECT 1; -- large editor reproduction\n", 100), Position{Line: 50}},
		{"rows=10000", strings.Repeat("SELECT 1; -- large editor reproduction\n", 10000), Position{Line: 5000}},
		{"rows=100000", strings.Repeat("SELECT 1; -- large editor reproduction\n", 100000), Position{Line: 50000}},
		{"single-line=1MiB", strings.Repeat("x", 1<<20), Position{Col: 1 << 19}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			buf := largeBenchmarkBuffer(tc.text)
			buf.SetCursor(tc.pos)
			store := keys.NewModeStore()
			store.Set(types.QUERY_EDITOR, types.ModeInsert)
			matcher, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{Modes: store})
			if err != nil {
				b.Fatal(err)
			}
			e := NewVimEditor(benchmarkBufferProvider{buf}, matcher, types.QUERY_EDITOR)
			v := gocui.NewView("benchmark", 0, 0, 100, 24, gocui.OutputNormal)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				e.Edit(v, gocui.NewKeyRune('x'))
				e.Edit(v, gocui.NewKeyName(gocui.KeyBackspace))
			}
		})
	}
}

type benchmarkBufferProvider struct{ buf *Buffer }

func (p benchmarkBufferProvider) Buffer() *Buffer { return p.buf }

func BenchmarkLargeBufferAutoContext(b *testing.B) {
	buf := largeBenchmarkBuffer(strings.Repeat("SELECT 1;\n", 10000))
	pos := Position{Line: 5000, Col: 6}
	b.ReportAllocs()
	for b.Loop() {
		InAliasSlot(buf, pos)
		AutoTriggerFromContext(buf, pos)
	}
}

func largeBenchmarkBuffer(text string) *Buffer {
	buf := &Buffer{}
	for _, line := range strings.Split(text, "\n") {
		buf.Lines = append(buf.Lines, Line{Runes: []rune(line)})
	}
	return buf
}
