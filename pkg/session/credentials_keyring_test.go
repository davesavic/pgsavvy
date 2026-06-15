package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/99designs/keyring"
	"github.com/adrg/xdg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// withXDGDataHome rewires xdg.DataHome to a tmp path for the duration of t.
func withXDGDataHome(t *testing.T, path string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", path)
	xdg.Reload()
	t.Cleanup(func() { xdg.Reload() })
}

// seedKeyring opens the file-backend keyring under the given dir and stores
// (key, value) using passphrase. Returns once the on-disk artifacts exist.
func seedKeyring(t *testing.T, dir, passphrase, key, value string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	kr, err := keyring.Open(keyring.Config{
		AllowedBackends:  []keyring.BackendType{keyring.FileBackend},
		ServiceName:      "dbsavvy",
		FileDir:          dir,
		FilePasswordFunc: keyring.FixedStringPrompt(passphrase),
	})
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := kr.Set(keyring.Item{Key: key, Data: []byte(value)}); err != nil {
		t.Fatalf("seed set: %v", err)
	}
}

func TestResolvePassword_KeyringEnvPassphrase(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "phrase", "prod-db", "secret-from-keyring")

	t.Setenv(keyringPassphraseEnv, "phrase")
	profile := models.Connection{Name: "p", KeyringRef: "prod-db"}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "secret-from-keyring" {
		t.Fatalf("got %q", got)
	}
}

func TestKeyringEnvPassphraseSetButEmptyFallsToPrompter(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "real-phrase", "prod-db", "kr-pw")

	// Env present but empty → must fall through to prompter.
	t.Setenv(keyringPassphraseEnv, "")
	fp := &fakePrompter{value: "real-phrase"}
	profile := models.Connection{Name: "p", KeyringRef: "prod-db"}
	got, err := ResolvePassword(context.Background(), profile, fp)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "kr-pw" {
		t.Fatalf("got %q", got)
	}
	if fp.calls == 0 {
		t.Fatal("prompter not invoked")
	}
}

func TestKeyringFileMode0600(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")

	t.Setenv(keyringPassphraseEnv, "phrase")
	// Trigger creation via a Set through resolveKeyring's open path.
	seedKeyring(t, dir, "phrase", "k", "v")

	// Parent ("dbsavvy") should be 0700; we don't necessarily own its mode
	// after MkdirAll inside seedKeyring, but ensureKeyringDirMode tightens
	// when our code opens it.
	profile := models.Connection{Name: "p", KeyringRef: "k"}
	if _, err := ResolvePassword(context.Background(), profile, nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	parent := filepath.Join(tmp, "dbsavvy")
	if info, err := os.Stat(parent); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("parent perm = %o, want 0700", info.Mode().Perm())
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Errorf("keyring dir perm = %o, want 0700", info.Mode().Perm())
	}
}

func TestKeyringWarnsOnLooseMode(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "phrase", "k", "v")

	// Find a seeded file and chmod it loose.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var looseFile string
	for _, e := range entries {
		if !e.IsDir() {
			looseFile = filepath.Join(dir, e.Name())
			break
		}
	}
	if looseFile == "" {
		t.Fatal("no keyring item file created")
	}
	if err := os.Chmod(looseFile, 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture stderr.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	t.Setenv(keyringPassphraseEnv, "phrase")
	profile := models.Connection{Name: "p", KeyringRef: "k"}
	_, err = ResolvePassword(context.Background(), profile, nil)
	_ = w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	os.Stderr = origStderr
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	captured := string(buf[:n])
	if !strings.Contains(captured, "WARN") || !strings.Contains(captured, "loose mode") {
		t.Fatalf("expected WARN on stderr, got: %q", captured)
	}

	// After warn, the file should have been tightened to 0600.
	info, err := os.Stat(looseFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("post-warn perm = %o, want 0600", info.Mode().Perm())
	}
}

// installWarnTestSeams swaps the TTY detector, the WARN sink, and resets the
// once-guard for the duration of t. Returns the buffer the sink writes into.
func installWarnTestSeams(t *testing.T, isTTY bool) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}

	origIsTTY := keyringIsTTY
	origSink := keyringWarnSink
	origOnce := keyringEnvPassphraseWarnOnce

	keyringIsTTY = func() bool { return isTTY }
	keyringWarnSink = buf
	keyringEnvPassphraseWarnOnce = &sync.Once{}

	t.Cleanup(func() {
		keyringIsTTY = origIsTTY
		keyringWarnSink = origSink
		keyringEnvPassphraseWarnOnce = origOnce
	})
	return buf
}

func TestKeyringEnvPassphraseEmitsTTYWarnOnce(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "phrase", "prod-db", "kr-pw")

	t.Setenv(keyringPassphraseEnv, "phrase")
	buf := installWarnTestSeams(t, true)

	profile := models.Connection{Name: "p", KeyringRef: "prod-db"}
	if _, err := ResolvePassword(context.Background(), profile, nil); err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	want := "DBSAVVY_KEYRING_PASSPHRASE is set"
	got := buf.String()
	if !strings.Contains(got, "WARN") || !strings.Contains(got, want) {
		t.Fatalf("expected WARN substring in %q", got)
	}
	if c := strings.Count(got, want); c != 1 {
		t.Fatalf("expected exactly 1 WARN, got %d in %q", c, got)
	}

	// Second resolve: sync.Once must keep count at 1.
	if _, err := ResolvePassword(context.Background(), profile, nil); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	got = buf.String()
	if c := strings.Count(got, want); c != 1 {
		t.Fatalf("after 2nd resolve, expected 1 WARN, got %d in %q", c, got)
	}
}

func TestKeyringEnvPassphraseNoWarnWhenNotTTY(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "phrase", "prod-db", "kr-pw")

	t.Setenv(keyringPassphraseEnv, "phrase")
	buf := installWarnTestSeams(t, false)

	profile := models.Connection{Name: "p", KeyringRef: "prod-db"}
	if _, err := ResolvePassword(context.Background(), profile, nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got := buf.String(); strings.Contains(got, "DBSAVVY_KEYRING_PASSPHRASE is set") {
		t.Fatalf("did not expect TTY WARN, got: %q", got)
	}
}

func TestKeyringEnvPassphraseNoWarnWhenEnvUnset(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "real-phrase", "prod-db", "kr-pw")

	// Env explicitly unset.
	t.Setenv(keyringPassphraseEnv, "")
	_ = os.Unsetenv(keyringPassphraseEnv)

	buf := installWarnTestSeams(t, true)

	fp := &fakePrompter{value: "real-phrase"}
	profile := models.Connection{Name: "p", KeyringRef: "prod-db"}
	if _, err := ResolvePassword(context.Background(), profile, fp); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if got := buf.String(); strings.Contains(got, "DBSAVVY_KEYRING_PASSPHRASE is set") {
		t.Fatalf("did not expect TTY WARN when env unset, got: %q", got)
	}
}

func TestKeyringNoEnvNoPrompterErrors(t *testing.T) {
	tmp := t.TempDir()
	withXDGDataHome(t, tmp)
	dir := filepath.Join(tmp, "dbsavvy", "keyring")
	seedKeyring(t, dir, "phrase", "k", "v")

	// Ensure env is unset.
	t.Setenv(keyringPassphraseEnv, "")
	_ = os.Unsetenv(keyringPassphraseEnv)

	profile := models.Connection{Name: "p", KeyringRef: "k"}
	_, err := ResolvePassword(context.Background(), profile, nil)
	if err == nil {
		t.Fatal("expected error when no passphrase source")
	}
}
