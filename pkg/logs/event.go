package logs

import "github.com/sirupsen/logrus"

// Event emits a structured DEBUG entry with the conventional schema fields
// {cat, evt, ...fields}. Always sets `cat` and `evt`, overriding any value
// passed in fields.
//
// NOTE: This helper is the canonical way for T5/T6/T7 to emit instrumentation.
// Callers should NOT bypass it (lest the cat-gate at write time stop working).
func Event(l *logrus.Logger, cat, evt string, fields logrus.Fields) {
	if l == nil {
		return
	}
	if fields == nil {
		fields = logrus.Fields{}
	}
	fields["cat"] = cat
	fields["evt"] = evt
	l.WithFields(fields).Debug(evt)
}
