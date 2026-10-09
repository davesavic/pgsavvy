package textcells

import (
	"strings"
	"testing"

	"github.com/rivo/uniseg"
)

func TestRenderASCIIBypassPreservesGraphemeBoundaries(t *testing.T) {
	parts := []string{"", "ASCII", "界", "e\u0301", "⌛︎", "☺️", "1️⃣", "👩‍💻", "🇺🇸", "👍🏽", "\u0600A", "\x1b[33m", "\x1b[0m", "\n"}
	for _, first := range parts {
		for _, last := range parts {
			input := "before " + first + last + " after"
			var measure Measure
			var expected strings.Builder
			clusters := uniseg.NewGraphemes(input)
			for clusters.Next() {
				cluster := clusters.Str()
				if len(cluster) == 1 {
					expected.WriteString(cluster)
				} else {
					WriteCluster(&expected, cluster, measure.ClusterWidth(cluster))
				}
			}
			if got := Render(input); got != expected.String() {
				t.Fatalf("Render(%q)=%q, want %q", input, got, expected.String())
			}
		}
	}
}

func TestPrefixNativeCellBudget(t *testing.T) {
	for _, input := range []string{"ASCII", "⌛︎x", "1️⃣x", "界e\u0301👩‍💻z", "🇺🇸👍🏽!"} {
		var measure Measure
		for budget := 0; budget < 12; budget++ {
			prefix := measure.Prefix(input, budget)
			if !strings.HasPrefix(input, prefix) || measure.StringWidth(prefix) > budget {
				t.Fatalf("Prefix(%q,%d)=%q exceeds the native budget or changes input", input, budget, prefix)
			}
			if measure.StringWidth(input) <= budget && prefix != input {
				t.Fatalf("fitting input %q was truncated at %d", input, budget)
			}
		}
	}
}
