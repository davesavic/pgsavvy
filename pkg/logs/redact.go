package logs

import "github.com/sirupsen/logrus"

// Redactor is the hook contract. T2 will provide a real implementation that
// walks struct tags + applies regex. For T1, DefaultRedactor returns a no-op
// implementation so the package compiles end-to-end.
type Redactor interface {
	Levels() []logrus.Level
	Fire(*logrus.Entry) error
}

// DefaultRedactor returns a no-op Redactor that satisfies the logrus.Hook
// contract for all levels. Replaced by T2.
func DefaultRedactor() Redactor { return noopRedactor{} }

type noopRedactor struct{}

func (noopRedactor) Levels() []logrus.Level   { return logrus.AllLevels }
func (noopRedactor) Fire(*logrus.Entry) error { return nil }
