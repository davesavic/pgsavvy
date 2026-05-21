package logs

import (
	"context"
	"log/slog"
)

// Event emits a structured DEBUG entry with the conventional schema attrs
// {cat, evt, ...attrs}. Always sets cat and evt; any user-supplied "cat" or
// "evt" attrs are stripped to preserve the invariant.
//
// NOTE: This helper is the canonical way for instrumentation. Callers should
// NOT bypass it (lest the cat-gate at write time stop working).
func Event(l *slog.Logger, cat, evt string, attrs ...slog.Attr) {
	if l == nil {
		return
	}
	out := make([]slog.Attr, 0, len(attrs)+2)
	out = append(out, slog.String("cat", cat), slog.String("evt", evt))
	for _, a := range attrs {
		if a.Key == "cat" || a.Key == "evt" {
			continue
		}
		out = append(out, a)
	}
	l.LogAttrs(context.Background(), slog.LevelDebug, evt, out...)
}
