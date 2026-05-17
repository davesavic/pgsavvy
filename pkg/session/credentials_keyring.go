package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/99designs/keyring"
	"github.com/adrg/xdg"
	"golang.org/x/term"
)

// keyringIsTTY reports whether stdin is a terminal. Indirected via a package
// variable so tests can swap it without touching public API.
var keyringIsTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// keyringWarnSink is where the env-passphrase-on-TTY WARN is written.
// Indirected via a package variable so tests can capture output.
var keyringWarnSink io.Writer = os.Stderr

// keyringEnvPassphraseWarnOnce guards the one-shot stderr WARN emitted when
// DBSAVVY_KEYRING_PASSPHRASE is set and stdin is a TTY. See Rule 7 of D20.
// Pointer-valued so tests can swap in a fresh once without violating
// copylocks (sync.Once embeds sync.noCopy).
var keyringEnvPassphraseWarnOnce = &sync.Once{}

// xdgDataHome returns the data-home base path. Indirected through a small
// helper so tests that override XDG via t.Setenv + xdg.Reload pick up the
// change without us re-parsing env ourselves (epic D1, Critic: reuse adrg/xdg).
func xdgDataHome() string {
	return xdg.DataHome
}

// resolveKeyring opens (or initializes) the dbsavvy file-backend keyring
// and fetches the item identified by ref. The passphrase is supplied via
// passphraseFunc (env first, then prompter). See epic D1, D2, D20.
func resolveKeyring(ctx context.Context, ref string, prompter Prompter) (string, error) {
	dir := keyringDir()
	if err := ensureKeyringDirMode(dir); err != nil {
		return "", err
	}

	cfg := keyring.Config{
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		ServiceName:      "dbsavvy",
		FileDir:          dir,
		FilePasswordFunc: passphraseFunc(ctx, prompter),
	}

	kr, err := keyring.Open(cfg)
	if err != nil {
		return "", fmt.Errorf("open keyring: %w", err)
	}

	item, err := kr.Get(ref)
	if err != nil {
		return "", fmt.Errorf("get %q: %w", ref, err)
	}

	// Re-check file modes after open: 99designs/keyring creates the per-item
	// files on first Set; warn (without failing) if anything looks loose.
	warnIfLooseKeyringMode(dir)

	return string(item.Data), nil
}

// errKeyringPassphraseRequiredInTUI is returned by passphraseFunc when
// DBSAVVY_KEYRING_PASSPHRASE is unset (or set-but-empty) AND the active
// Prompter is TUIRefusePrompter. It is a SEPARATE typed sentinel from
// errInteractivePromptNotSupported: the keyring path has a different
// remediation message ("set DBSAVVY_KEYRING_PASSPHRASE before launching
// dbsavvy") than the generic prompter-refusal, and the toast layer renders
// them as distinct strings.
//
// The sentinel is unexported; callers detect via
// IsKeyringPassphraseRequiredInTUI.
var errKeyringPassphraseRequiredInTUI = errors.New(
	"session: keyring passphrase required in TUI mode; " +
		"set DBSAVVY_KEYRING_PASSPHRASE before launching dbsavvy")

// IsKeyringPassphraseRequiredInTUI reports whether err (or anything it wraps)
// is the typed keyring-passphrase-in-TUI sentinel.
func IsKeyringPassphraseRequiredInTUI(err error) bool {
	return errors.Is(err, errKeyringPassphraseRequiredInTUI)
}

// passphraseFunc returns the PromptFunc the keyring library uses to obtain
// the file-backend passphrase. Resolution order: env first
// (DBSAVVY_KEYRING_PASSPHRASE, set-and-non-empty) → prompter → error.
// Set-but-empty env is treated as unset (Critic resolution).
//
// TUI-mode short-circuit: when env is unset/empty AND prompter is
// TUIRefusePrompter, the function returns errKeyringPassphraseRequiredInTUI
// instead of forwarding to the prompter's generic refusal. This lets the UI
// layer render a remediation specific to the keyring path (G3-G(iii) from the
// dbsavvy-enn review-plan resolutions).
func passphraseFunc(ctx context.Context, prompter Prompter) keyring.PromptFunc {
	return func(prompt string) (string, error) {
		if v, ok := os.LookupEnv(keyringPassphraseEnv); ok && v != "" {
			if keyringIsTTY() {
				keyringEnvPassphraseWarnOnce.Do(func() {
					_, _ = fmt.Fprintln(keyringWarnSink,
						"WARN: DBSAVVY_KEYRING_PASSPHRASE is set; prompter is safer on multi-user hosts")
				})
			}
			return v, nil
		}
		// TUI-mode keyring-specific refusal. Type-switch (not errors.Is) is
		// the right tool here: we are inspecting the static identity of the
		// prompter, not unwrapping an error chain.
		if _, isTUI := prompter.(TUIRefusePrompter); isTUI {
			return "", errKeyringPassphraseRequiredInTUI
		}
		if prompter == nil {
			return "", errors.New("no keyring passphrase: env unset and no prompter provided")
		}
		hint := "keyring passphrase"
		if prompt != "" {
			hint = prompt
		}
		return prompter.PromptPassword(ctx, hint)
	}
}

// ensureKeyringDirMode creates the keyring dir (and its parent) with mode
// 0700 if missing. If found with looser mode, it tightens to 0700 and
// returns no error (parent of keyring data is dbsavvy/, which we own).
// See epic D20.
func ensureKeyringDirMode(dir string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", parent, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir keyring %s: %w", dir, err)
	}
	// Tighten in case of pre-existing loose modes.
	_ = os.Chmod(parent, 0o700)
	_ = os.Chmod(dir, 0o700)
	return nil
}

// warnIfLooseKeyringMode scans dir and emits a stderr warning for any file
// whose perm bits include group/other access. Best-effort; errors are
// swallowed (we don't want a chmod hiccup to break credential resolution).
func warnIfLooseKeyringMode(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o077 != 0 {
			path := filepath.Join(dir, e.Name())
			fmt.Fprintf(os.Stderr, "dbsavvy: WARN keyring item %s has loose mode %04o; tightening to 0600\n",
				path, info.Mode().Perm())
			_ = os.Chmod(path, 0o600)
		}
	}
}
