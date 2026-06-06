package ui_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/ui"
)

func newBufLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, buf
}

func linesContainingAll(buf *bytes.Buffer, subs ...string) []string {
	var out []string
	for ln := range strings.SplitSeq(buf.String(), "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok && ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func TestShow_EmitsToastSetEvent(t *testing.T) {
	h := ui.NewToastHelper(nil)
	l, buf := newBufLogger()
	h.SetLogger(l)

	h.Show("saved query 'X'", 3*time.Second)

	lines := linesContainingAll(buf, `"cat":"state"`, `"evt":"toast_set"`)
	require.Len(t, lines, 1, "expected one toast_set line; got %v", buf.String())
	require.Contains(t, lines[0], `"key":""`)
	require.Contains(t, lines[0], `"msg_preview":"saved query 'X'"`)
	require.Contains(t, lines[0], `"ttl_ms":3000`)
	require.Contains(t, lines[0], `"gen":1`)
}

func TestShowOrUpdate_EmitsToastSetEventWithKey(t *testing.T) {
	h := ui.NewToastHelper(nil)
	l, buf := newBufLogger()
	h.SetLogger(l)

	h.ShowOrUpdate("notice:rows", "rows: 100", time.Second)

	lines := linesContainingAll(buf, `"evt":"toast_set"`, `"key":"notice:rows"`)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], `"msg_preview":"rows: 100"`)
}

// TestShow_RedactsPasswordInMsgPreview verifies the msg_preview field
// uses the already-redacted value (AD-13a: ToastHelper.Show calls
// session.RedactDSN before storing in h.current). A raw DSN with a
// password must NOT appear in the emitted line.
func TestShow_RedactsPasswordInMsgPreview(t *testing.T) {
	h := ui.NewToastHelper(nil)
	l, buf := newBufLogger()
	h.SetLogger(l)

	const dsn = "postgres://alice:hunter2@db.example.com:5432/app"
	h.Show("connect failed: "+dsn, 0)

	out := buf.String()
	require.NotContains(t, out, "hunter2", "password leaked into toast_set line: %s", out)
	require.Contains(t, out, "alice:***@", "expected redacted user:***@ form in toast_set line: %s", out)
}
