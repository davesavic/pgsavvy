package logs

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sirupsen/logrus"
)

// slogBridge implements slog.Handler by forwarding records to a *logrus.Logger.
//
// The bridge intentionally does NOT call any Redactor — redaction is the
// logrus hook's job (single-redaction bilayer per AD-5 revised). Every
// emitted entry always carries a `cat="db"` field so the per-category
// allowlist in Open() routes session-package logs to the file sink.
type slogBridge struct {
	l      *logrus.Logger
	attrs  []slog.Attr // accumulated WithAttrs context
	groups []string    // accumulated WithGroup context (dotted-key prefix)
}

// NewSlogHandler returns a slog.Handler that forwards Records to l. Panics
// if l is nil — production code MUST pass a real logger.
func NewSlogHandler(l *logrus.Logger) slog.Handler {
	if l == nil {
		panic("logs.NewSlogHandler: nil logger")
	}
	return &slogBridge{l: l}
}

// Enabled defers level filtering to the underlying logrus logger.
func (b *slogBridge) Enabled(_ context.Context, lvl slog.Level) bool {
	return b.l.IsLevelEnabled(slogToLogrusLevel(lvl))
}

// Handle converts a slog.Record into a logrus entry and emits it. Accumulated
// WithAttrs come first so per-Record attributes can override them.
func (b *slogBridge) Handle(_ context.Context, r slog.Record) error {
	fields := logrus.Fields{}
	for _, a := range b.attrs {
		applyAttr(fields, b.groups, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		applyAttr(fields, b.groups, a)
		return true
	})
	fields["cat"] = "db"
	b.l.WithFields(fields).Log(slogToLogrusLevel(r.Level), r.Message)
	return nil
}

// WithAttrs returns a shallow-copied handler with attrs appended.
func (b *slogBridge) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return b
	}
	nb := *b
	nb.attrs = make([]slog.Attr, 0, len(b.attrs)+len(attrs))
	nb.attrs = append(nb.attrs, b.attrs...)
	nb.attrs = append(nb.attrs, attrs...)
	return &nb
}

// WithGroup returns a shallow-copied handler with name appended to the
// dotted-key prefix. Empty names are no-ops.
func (b *slogBridge) WithGroup(name string) slog.Handler {
	if name == "" {
		return b
	}
	nb := *b
	nb.groups = make([]string, 0, len(b.groups)+1)
	nb.groups = append(nb.groups, b.groups...)
	nb.groups = append(nb.groups, name)
	return &nb
}

// slogToLogrusLevel maps slog levels into the closest logrus level.
func slogToLogrusLevel(l slog.Level) logrus.Level {
	switch {
	case l >= slog.LevelError:
		return logrus.ErrorLevel
	case l >= slog.LevelWarn:
		return logrus.WarnLevel
	case l >= slog.LevelInfo:
		return logrus.InfoLevel
	default:
		return logrus.DebugLevel
	}
}

// applyAttr writes one slog.Attr (incl. nested groups) into fields, with the
// group prefix dotted (e.g. db.pg.sid for WithGroup("db").WithGroup("pg") +
// attr "sid"). Empty group names collapse (no leading dot).
func applyAttr(fields logrus.Fields, groups []string, a slog.Attr) {
	val := a.Value.Resolve()
	if val.Kind() == slog.KindGroup {
		nested := groups
		if a.Key != "" {
			nested = append(append([]string{}, groups...), a.Key)
		}
		for _, sub := range val.Group() {
			applyAttr(fields, nested, sub)
		}
		return
	}
	fields[joinGroups(groups, a.Key)] = val.Any()
}

// joinGroups assembles a dotted key from the group prefix + leaf key.
func joinGroups(groups []string, key string) string {
	if len(groups) == 0 {
		return key
	}
	parts := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		if g != "" {
			parts = append(parts, g)
		}
	}
	parts = append(parts, key)
	return strings.Join(parts, ".")
}
