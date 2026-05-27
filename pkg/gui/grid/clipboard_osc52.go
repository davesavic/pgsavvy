package grid

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrClipboardTooLarge is returned by the system clipboard writer when the
// payload exceeds maxClipboardBytes. The OSC-52 spec offers no portable way
// to chunk arbitrarily large payloads, and many terminals silently drop an
// oversized sequence — so rather than truncate (and hand the user a corrupt
// partial copy) we surface the failure. The controller maps this to the
// "clipboard: value too large" toast.
var ErrClipboardTooLarge = errors.New("clipboard: value too large")

// ErrClipboardUnavailable is returned when neither an OSC-52-capable terminal
// nor any fallback CLI (wl-copy / xclip / pbcopy) is usable. The controller
// maps this to the "clipboard unavailable" toast. Detected at Write time
// (not construction) because terminal/$DISPLAY state can change and the
// fallback binaries are looked up lazily.
var ErrClipboardUnavailable = errors.New("clipboard unavailable")

// maxClipboardBytes caps the raw (pre-base64) payload an OSC-52 write will
// emit. 74994 bytes base64-encodes to ~99992 bytes, just under the 100KB
// ceiling most terminal emulators enforce on a single OSC-52 sequence.
const maxClipboardBytes = 74994

// systemClipboard is the production ClipboardWriter. It prefers OSC-52
// (works over SSH / inside multiplexers when the host terminal supports it)
// and falls back to a platform clipboard CLI invoked with the payload piped
// on stdin — never interpolated into a shell command line.
type systemClipboard struct {
	// out is where the OSC-52 escape sequence is written. Defaults to
	// os.Stdout; injectable for tests.
	out *os.File
	// env reads environment variables; injectable for tests so the
	// multiplexer-passthrough branch can be exercised deterministically.
	env func(string) string
	// lookPath resolves a fallback binary on PATH; injectable for tests.
	lookPath func(string) (string, error)
	// run executes a resolved fallback binary with the payload on stdin;
	// injectable for tests so no real process is spawned.
	run func(bin string, args []string, payload string) error
}

// NewSystemClipboard returns the production ClipboardWriter. Wired into the
// result grid via View.SetClipboard at tab-creation time.
func NewSystemClipboard() ClipboardWriter {
	return &systemClipboard{
		out:      os.Stdout,
		env:      os.Getenv,
		lookPath: exec.LookPath,
		run:      runClipboardCommand,
	}
}

// Write publishes text to the host clipboard. Returns ErrClipboardTooLarge
// when text exceeds the size cap (no silent truncation) and
// ErrClipboardUnavailable when no transport succeeds.
func (c *systemClipboard) Write(text string) error {
	if len(text) > maxClipboardBytes {
		return ErrClipboardTooLarge
	}

	// OSC-52: base64-encode the payload, then frame it. The raw bytes never
	// touch the escape sequence directly, so embedded control bytes cannot
	// terminate it early or inject further escapes.
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	seq := osc52Sequence(encoded, c.env)
	if c.out != nil {
		if _, err := c.out.WriteString(seq); err == nil {
			return nil
		}
	}

	// OSC-52 unavailable / write failed → try the platform CLIs.
	if err := c.writeViaFallback(text); err != nil {
		return err
	}
	return nil
}

// osc52Sequence frames the base64 payload as an OSC-52 set-clipboard
// sequence. When running inside tmux ($TMUX) or screen ($STY) the sequence
// is wrapped in the DCS passthrough so it reaches the host terminal rather
// than being swallowed by the multiplexer.
func osc52Sequence(encoded string, env func(string) string) string {
	// ESC ] 52 ; c ; <base64> BEL
	inner := "\x1b]52;c;" + encoded + "\x07"

	if env("TMUX") != "" {
		// tmux passthrough: ESC P tmux; <inner-with-ESC-doubled> ESC \
		return "\x1bPtmux;" + strings.ReplaceAll(inner, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	if term := env("STY"); term != "" {
		// GNU screen passthrough: ESC P <inner> ESC \
		return "\x1bP" + inner + "\x1b\\"
	}
	return inner
}

// writeViaFallback tries each platform clipboard CLI in turn, piping the
// payload on the child's stdin. Returns ErrClipboardUnavailable when none
// resolve / all fail.
func (c *systemClipboard) writeViaFallback(text string) error {
	for _, cand := range fallbackCandidates() {
		bin, err := c.lookPath(cand.bin)
		if err != nil {
			continue
		}
		if err := c.run(bin, cand.args, text); err != nil {
			continue
		}
		return nil
	}
	return ErrClipboardUnavailable
}

// clipboardCandidate is a fallback CLI plus its argv (sans the binary).
type clipboardCandidate struct {
	bin  string
	args []string
}

// fallbackCandidates is the ordered list of platform clipboard CLIs tried
// when OSC-52 is unavailable: Wayland, then X11, then macOS.
func fallbackCandidates() []clipboardCandidate {
	return []clipboardCandidate{
		{bin: "wl-copy"},
		{bin: "xclip", args: []string{"-selection", "clipboard"}},
		{bin: "pbcopy"},
	}
}

// clipboardCmdTimeout bounds how long a fallback CLI may run. xclip in
// particular hangs indefinitely when $DISPLAY is missing or the X clipboard is
// busy; without this deadline that would stall the UI goroutine that called
// Yank. 3s is generous for a local clipboard write yet recovers automatically.
const clipboardCmdTimeout = 3 * time.Second

// runClipboardCommand executes bin with args, piping payload on stdin. Never
// uses a shell, so payload bytes cannot be interpreted as command syntax. A
// context deadline guarantees the call returns even if the CLI hangs.
func runClipboardCommand(bin string, args []string, payload string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is resolved via exec.LookPath from a fixed allow-list, never user input
	cmd.Stdin = strings.NewReader(payload)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", bin, err)
	}
	return nil
}
