package session

import (
	"log/slog"
	"strings"
)

// RedactSecrets is connection-scoped; no resolved password is published.
func (s *SQLSession) RedactSecrets(text string) string {
	if redactor, ok := s.conn.(interface{ RedactSecrets(string) string }); ok {
		text = redactor.RedactSecrets(text)
	}
	if s.connPwd != "" {
		encoded := strings.NewReplacer("'", "''", "\\", "\\\\").Replace(s.connPwd)
		text = strings.ReplaceAll(text, encoded, "***")
		text = strings.ReplaceAll(text, strings.ReplaceAll(s.connPwd, "'", "''"), "***")
		text = strings.ReplaceAll(text, s.connPwd, "***")
	}
	return text
}

type connectionLogRedactor struct{ redact func(string) string }

func (r connectionLogRedactor) Redact(record *slog.Record) {
	out := slog.NewRecord(record.Time, record.Level, r.redact(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		out.AddAttrs(r.attr(attr))
		return true
	})
	*record = out
}

func (r connectionLogRedactor) attr(attr slog.Attr) slog.Attr {
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(r.redact(value.String()))
	case slog.KindGroup:
		group := value.Group()
		out := make([]slog.Attr, len(group))
		for i, item := range group {
			out[i] = r.attr(item)
		}
		attr.Value = slog.GroupValue(out...)
	}
	return attr
}
