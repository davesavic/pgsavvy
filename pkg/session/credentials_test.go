package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// fakePrompter implements Prompter for unit tests. It records calls and
// returns the configured value/error.
type fakePrompter struct {
	calls  int
	hints  []string
	value  string
	err    error
	ctxErr error
}

func (p *fakePrompter) PromptPassword(ctx context.Context, hint string) (string, error) {
	p.calls++
	p.hints = append(p.hints, hint)
	if p.ctxErr != nil {
		return "", p.ctxErr
	}
	return p.value, p.err
}

func TestResolvePassword_InlineShortCircuits(t *testing.T) {
	profile := models.Connection{Name: "p", Password: "inline-pw"}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "inline-pw" {
		t.Fatalf("got %q want %q", got, "inline-pw")
	}
}

func TestResolvePassword_EmptyInlineFallsThrough(t *testing.T) {
	// No mechanisms set, no prompter → auto-discovery sentinel.
	profile := models.Connection{Name: "p", Password: ""}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q want empty", got)
	}
}

func TestResolvePassword_PasswordCommandSuccess(t *testing.T) {
	profile := models.Connection{Name: "p", PasswordCommand: "printf hunter2"}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q want %q", got, "hunter2")
	}
}

func TestResolvePassword_PasswordCommandFailureSurfacesStderr(t *testing.T) {
	profile := models.Connection{Name: "p", PasswordCommand: "echo BOOM 1>&2; exit 7"}
	_, err := ResolvePassword(context.Background(), profile, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "BOOM") {
		t.Errorf("stderr 'BOOM' missing from error: %s", msg)
	}
	if !strings.Contains(msg, "exit code 7") && !strings.Contains(msg, "exit status 7") {
		t.Errorf("exit code 7 missing from error: %s", msg)
	}
}

func TestResolvePasswordExhaustsToPgxAutoDiscoverySentinel(t *testing.T) {
	profile := models.Connection{Name: "p"}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("expected nil err sentinel, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty sentinel, got %q", got)
	}
}

func TestResolvePassword_PrompterEmptyStringReturnsEmptyPassword(t *testing.T) {
	profile := models.Connection{Name: "p"}
	fp := &fakePrompter{value: ""}
	got, err := ResolvePassword(context.Background(), profile, fp)
	if err != nil {
		t.Fatalf("expected nil error for empty password from prompter, got %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty password, got %q", got)
	}
	if fp.calls != 1 {
		t.Fatalf("expected prompter called once, got %d", fp.calls)
	}
}

func TestResolvePasswordPropagatesPrompterContextCanceled(t *testing.T) {
	profile := models.Connection{Name: "p"}
	fp := &fakePrompter{ctxErr: context.Canceled}
	_, err := ResolvePassword(context.Background(), profile, fp)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped context.Canceled, got %v", err)
	}
	if fp.calls != 1 {
		t.Fatalf("prompter called %d times, want 1", fp.calls)
	}
}

func TestPgpassRejectsPermissiveMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass")
	if err := os.WriteFile(path, []byte("localhost:5432:db:user:pw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profile := models.Connection{
		Name:       "p",
		DSN:        "postgres://user@localhost:5432/db",
		PgpassPath: path,
	}
	_, err := ResolvePassword(context.Background(), profile, nil)
	if err == nil {
		t.Fatal("expected error for permissive mode")
	}
	if !errors.Is(err, errPgpassInsecureMode) {
		t.Fatalf("expected errPgpassInsecureMode, got %v", err)
	}
}

func TestPgpassReadsCorrectLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass")
	content := strings.Join([]string{
		"# comment",
		"other-host:5432:db:user:wrong",
		"localhost:5432:db:user:right-pw",
		"*:*:*:*:fallback",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := models.Connection{
		Name:       "p",
		DSN:        "postgres://user@localhost:5432/db",
		PgpassPath: path,
	}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "right-pw" {
		t.Fatalf("got %q want right-pw", got)
	}
}

func TestPgpassMatchesDiscreteFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass")
	content := strings.Join([]string{
		"other-host:5432:weddingsavvy:wsuser:wrong",
		"172.28.0.10:5432:weddingsavvy:wsuser:right-pw",
		"*:*:*:*:fallback",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Modern connection form persists discrete fields and leaves DSN empty.
	profile := models.Connection{
		Name:       "wedding savvy",
		Host:       "172.28.0.10",
		Port:       5432,
		User:       "wsuser",
		Database:   "weddingsavvy",
		PgpassPath: path,
	}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "right-pw" {
		t.Fatalf("got %q want right-pw", got)
	}
}

func TestPgpassWildcardLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pgpass")
	content := "*:*:*:*:wild\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := models.Connection{
		Name:       "p",
		DSN:        "postgres://user@somehost:6543/anydb",
		PgpassPath: path,
	}
	got, err := ResolvePassword(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "wild" {
		t.Fatalf("got %q want wild", got)
	}
}

func TestPgpassMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist")
	profile := models.Connection{
		Name:       "p",
		DSN:        "postgres://user@localhost/db",
		PgpassPath: path,
	}
	_, err := ResolvePassword(context.Background(), profile, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestSplitPgpassLineEscapes(t *testing.T) {
	got := splitPgpassLine(`host:5432:db\:with\:colons:user:pw\:!`)
	want := []string{"host", "5432", "db:with:colons", "user", "pw:!"}
	if len(got) != len(want) {
		t.Fatalf("len=%d got=%v want=%v", len(got), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("field %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestParseDSNFieldsDefaults(t *testing.T) {
	host, port, db, user, err := parseDSNFields("postgres:///mydb")
	if err != nil {
		t.Fatal(err)
	}
	if host != "localhost" || port != "5432" || db != "mydb" || user != "" {
		t.Fatalf("unexpected: host=%q port=%q db=%q user=%q", host, port, db, user)
	}
}

func TestParseDSNFieldsRejectsBadScheme(t *testing.T) {
	if _, _, _, _, err := parseDSNFields("mysql://h/db"); err == nil {
		t.Fatal("expected error for mysql scheme")
	}
}
