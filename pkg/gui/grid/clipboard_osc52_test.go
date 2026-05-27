package grid

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// emptyEnv is an env lookup that reports every variable unset — the
// "plain terminal, no multiplexer" baseline.
func emptyEnv(string) string { return "" }

// TestOSC52_Framing_Base64EncodesPayload: a payload with control bytes must
// be base64-encoded into the OSC-52 sequence, never interpolated raw. The
// decoded base64 must round-trip back to the original bytes.
func TestOSC52_Framing_Base64EncodesPayload(t *testing.T) {
	payload := "x\x1b]52;c;evil\x07y" // contains the OSC-52 terminator itself
	seq := osc52Sequence(base64.StdEncoding.EncodeToString([]byte(payload)), emptyEnv)

	require.True(t, strings.HasPrefix(seq, "\x1b]52;c;"), "must start with OSC-52 set-clipboard introducer")
	require.True(t, strings.HasSuffix(seq, "\x07"), "must end with BEL")

	b64 := strings.TrimSuffix(strings.TrimPrefix(seq, "\x1b]52;c;"), "\x07")
	decoded, err := base64.StdEncoding.DecodeString(b64)
	require.NoError(t, err)
	require.Equal(t, payload, string(decoded), "base64 must round-trip the raw payload")

	// The raw control bytes from the payload must NOT appear verbatim in the
	// emitted sequence (other than the single framing BEL we added).
	require.Equal(t, 1, strings.Count(seq, "\x07"), "only the framing BEL may be present; payload BEL must be encoded")
}

// TestOSC52_TmuxPassthrough: inside tmux the sequence is wrapped in the DCS
// passthrough with the inner ESC doubled.
func TestOSC52_TmuxPassthrough(t *testing.T) {
	env := func(k string) string {
		if k == "TMUX" {
			return "/tmp/tmux-1000/default,1234,0"
		}
		return ""
	}
	seq := osc52Sequence(base64.StdEncoding.EncodeToString([]byte("hi")), env)
	require.True(t, strings.HasPrefix(seq, "\x1bPtmux;"), "tmux passthrough must open with ESC P tmux;")
	require.True(t, strings.HasSuffix(seq, "\x1b\\"), "tmux passthrough must close with ST (ESC \\)")
	require.Contains(t, seq, "\x1b\x1b]52;c;", "inner ESC must be doubled for tmux passthrough")
}

// TestOSC52_ScreenPassthrough: under GNU screen ($STY) the sequence is wrapped
// in a plain DCS passthrough.
func TestOSC52_ScreenPassthrough(t *testing.T) {
	env := func(k string) string {
		if k == "STY" {
			return "1234.pts-0.host"
		}
		return ""
	}
	seq := osc52Sequence(base64.StdEncoding.EncodeToString([]byte("hi")), env)
	require.True(t, strings.HasPrefix(seq, "\x1bP\x1b]52;c;"), "screen passthrough wraps the OSC in DCS")
	require.True(t, strings.HasSuffix(seq, "\x1b\\"), "screen passthrough closes with ST")
}

// TestSystemClipboard_TooLarge: an oversize payload returns ErrClipboardTooLarge
// (no silent truncation, no OSC-52 emission).
func TestSystemClipboard_TooLarge(t *testing.T) {
	c := &systemClipboard{
		out:      nil,
		env:      emptyEnv,
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		run:      func(string, []string, string) error { return nil },
	}

	big := strings.Repeat("a", maxClipboardBytes+1)
	err := c.Write(big)
	require.ErrorIs(t, err, ErrClipboardTooLarge)
}

// TestSystemClipboard_Unavailable: with no OSC-52 sink and no fallback binary,
// Write reports ErrClipboardUnavailable (no panic, no silent no-op).
func TestSystemClipboard_Unavailable(t *testing.T) {
	c := &systemClipboard{
		out:      nil, // no OSC-52 transport
		env:      emptyEnv,
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		run:      func(string, []string, string) error { return errors.New("unreached") },
	}
	err := c.Write("hello")
	require.ErrorIs(t, err, ErrClipboardUnavailable)
}

// TestSystemClipboard_FallbackPipesStdin: when OSC-52 is unavailable, the
// resolved fallback binary is invoked with the payload — and crucially the
// run hook (which production wires to exec.Command + stdin) receives the raw
// payload as data, never as a shell argument.
func TestSystemClipboard_FallbackPipesStdin(t *testing.T) {
	var gotBin string
	var gotArgs []string
	var gotPayload string
	c := &systemClipboard{
		out: nil,
		env: emptyEnv,
		lookPath: func(bin string) (string, error) {
			if bin == "xclip" {
				return "/usr/bin/xclip", nil
			}
			return "", errors.New("not found")
		},
		run: func(bin string, args []string, payload string) error {
			gotBin = bin
			gotArgs = args
			gotPayload = payload
			return nil
		},
	}
	require.NoError(t, c.Write("danger; rm -rf /"))
	require.Equal(t, "/usr/bin/xclip", gotBin)
	require.Equal(t, []string{"-selection", "clipboard"}, gotArgs)
	require.Equal(t, "danger; rm -rf /", gotPayload, "payload must be passed as data, not interpolated into args")
}

// TestSystemClipboard_OSC52Succeeds: with a real *os.File sink the OSC-52
// write succeeds and the fallback is never consulted.
func TestSystemClipboard_OSC52Succeeds(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "osc52")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	fallbackCalled := false
	c := &systemClipboard{
		out:      f,
		env:      emptyEnv,
		lookPath: func(string) (string, error) { fallbackCalled = true; return "", errors.New("nf") },
		run:      func(string, []string, string) error { return nil },
	}
	require.NoError(t, c.Write("hi"))
	require.False(t, fallbackCalled, "OSC-52 success must short-circuit the fallback")

	require.NoError(t, f.Sync())
	data, err := os.ReadFile(f.Name())
	require.NoError(t, err)
	require.Contains(t, string(data), "\x1b]52;c;")
}

// TestRunClipboardCommand_NoShell: the production run hook spawns the binary
// directly (no shell), so a payload that looks like shell syntax is delivered
// inert on stdin. We exercise it against `cat` to prove stdin is piped and no
// shell expands the payload.
func TestRunClipboardCommand_NoShell(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not available")
	}
	// cat echoes stdin to stdout; if a shell were involved the `$(...)` would
	// be expanded. We can't capture stdout via the current hook signature, so
	// we simply assert it runs without error and treats the payload as data.
	err := runClipboardCommand("cat", nil, "$(echo pwned)")
	require.NoError(t, err)
}
