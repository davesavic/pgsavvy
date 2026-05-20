package logs

import (
	"io"
	"testing"

	"github.com/sirupsen/logrus"
)

// NewTestLogger returns a logger that discards output. Useful for tests
// inside pkg/logs that want a non-nil *logrus.Logger without touching disk.
// IMPORTANT: writes nothing to t.TempDir(); does not call Open(); does not
// register hooks. Exists so T2..T7 tests have a uniform way to obtain a
// logger without copy-pasting `logrus.New() + SetOutput(io.Discard)`.
func NewTestLogger(t *testing.T) *logrus.Logger {
	t.Helper()
	l := logrus.New()
	l.SetOutput(io.Discard)
	l.SetLevel(logrus.DebugLevel)
	return l
}
