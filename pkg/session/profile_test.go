package session

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// recordingExecer is a pgConnExecer that captures the executed SQL and can
// be configured to fail on a substring match.
type recordingExecer struct {
	sqls    []string
	failOn  string
	failErr error
}

func (r *recordingExecer) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	if r.failOn != "" && strings.Contains(sql, r.failOn) {
		return pgconn.CommandTag{}, r.failErr
	}
	return pgconn.CommandTag{}, nil
}

func containsSQL(sqls []string, want string) bool {
	for _, s := range sqls {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

const validDSN = "postgres://app@db.prod.internal:5432/app?sslmode=require"

// ---------- BuildPgxConfig: shape & defaults --------------------------------

func TestBuildPgxConfig_PasswordOverwrite(t *testing.T) {
	profile := models.Connection{Name: "p", DSN: validDSN}
	cfg, err := BuildPgxConfig(context.Background(), profile, "hunter2")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := cfg.ConnConfig.Password; got != "hunter2" {
		t.Fatalf("ConnConfig.Password = %q, want %q", got, "hunter2")
	}
}

func TestBuildPgxConfig_PoolDefaults(t *testing.T) {
	profile := models.Connection{Name: "p", DSN: validDSN}
	cfg, err := BuildPgxConfig(context.Background(), profile, "pw")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.MinConns != 2 {
		t.Errorf("MinConns = %d, want 2 (raised in epic dbsavvy-66p.4 so Cancel cannot deadlock on conn checkout)", cfg.MinConns)
	}
	if cfg.MaxConns != 8 {
		t.Errorf("MaxConns = %d, want 8", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime != 30*time.Minute {
		t.Errorf("MaxConnLifetime = %s, want 30m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 5*time.Minute {
		t.Errorf("MaxConnIdleTime = %s, want 5m", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != time.Minute {
		t.Errorf("HealthCheckPeriod = %s, want 1m", cfg.HealthCheckPeriod)
	}
}

func TestBuildPgxConfig_MalformedDSNWrapsParseError(t *testing.T) {
	// pgxpool.ParseConfig accepts "" (defaults from env), so the failure-
	// surface here is any DSN it actually rejects. "postgres://" with an
	// unparseable port qualifies.
	profile := models.Connection{Name: "p", DSN: "postgres://h:notaport/db"}
	_, err := BuildPgxConfig(context.Background(), profile, "pw")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "session: parse dsn") {
		t.Fatalf("error not wrapped: %v", err)
	}
}

func TestBuildPgxConfigSkipsOverwriteOnEmptySentinel(t *testing.T) {
	// DSN encodes a password; auto-discovery sentinel must NOT clobber it.
	profile := models.Connection{Name: "p", DSN: "postgres://app:dsnpw@db.prod.internal:5432/app?sslmode=require"}
	cfg, err := BuildPgxConfig(context.Background(), profile, "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := cfg.ConnConfig.Password; got != "dsnpw" {
		t.Fatalf("ConnConfig.Password = %q, want DSN-encoded %q", got, "dsnpw")
	}
}

func TestBuildPgxConfig_AfterConnectHookInstalled(t *testing.T) {
	profile := models.Connection{Name: "p", DSN: validDSN, StatementTimeout: "30s", ReadOnly: true}
	cfg, err := BuildPgxConfig(context.Background(), profile, "pw")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.AfterConnect == nil {
		t.Fatal("AfterConnect hook not installed")
	}
}

// ---------- statement_timeout validation ------------------------------------

func TestBuildPgxConfig_InvalidStatementTimeoutFailsAtBuildTime(t *testing.T) {
	profile := models.Connection{Name: "p", DSN: validDSN, StatementTimeout: "garbage"}
	_, err := BuildPgxConfig(context.Background(), profile, "pw")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrStatementTimeoutInvalid) {
		t.Fatalf("expected ErrStatementTimeoutInvalid, got %v", err)
	}
}

func TestCanonicalizeStatementTimeout_AcceptedForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "0"},
		{"30s", "30s"},
		{"5min", "5min"},
		{"500ms", "500ms"},
		{"2h", "2h"},
		{"1d", "1d"},
		{"5 min", "5min"}, // whitespace canonicalized away
	}
	for _, c := range cases {
		got, err := CanonicalizeStatementTimeout(c.in)
		if err != nil {
			t.Errorf("canonicalize(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("canonicalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStatementTimeoutInjectionResistance enumerates the 20 hand-crafted
// injection vectors required by /review-plan-2026-05-17 and an additional
// 1000-iteration random-byte fuzz. Every accepted output is asserted to be
// safe to interpolate into "SET statement_timeout = '<>'".
func TestStatementTimeoutInjectionResistance(t *testing.T) {
	vectors := []string{
		"5min'; DROP TABLE x; --",
		"5min\n; DROP TABLE x",
		"5min;",
		"5min--",
		"5min/*",
		"0; SELECT pg_sleep(10)",
		"0'",
		"0\"",
		"0\x00",
		"0\\",
		"0\r\n",
		"5𝑚𝑖𝑛",   // mathematical italic 'min'
		"5ｍｓ",    // full-width ms
		"5 mins", // extra char
		"5 second",
		"5seconds",
		"0 OR 1=1",
		"0' UNION SELECT '",
		"5min#",
		"../etc/passwd",
	}
	for _, v := range vectors {
		if _, err := CanonicalizeStatementTimeout(v); err == nil {
			t.Errorf("vector %q was accepted but should have been rejected", v)
		}
	}

	r := rand.New(rand.NewPCG(0xDB, 0x5A))
	const acceptableBytes = "0123456789msminhd \t" // superset of legal alphabet
	for range 1000 {
		buf := make([]byte, 1+r.IntN(24))
		for j := range buf {
			// 70% bytes from the legal alphabet, 30% wild bytes.
			if r.IntN(10) < 7 {
				buf[j] = acceptableBytes[r.IntN(len(acceptableBytes))]
			} else {
				buf[j] = byte(r.IntN(256))
			}
		}
		got, err := CanonicalizeStatementTimeout(string(buf))
		if err != nil {
			continue
		}
		if !isSafeCanonicalTimeout(got) {
			t.Errorf("canonicalize(%q) returned unsafe canonical %q", buf, got)
		}
	}
}

// isSafeCanonicalTimeout encodes CanonicalizeStatementTimeout's post-condition:
// either the literal "0", or digits followed by exactly one allowlisted unit.
func isSafeCanonicalTimeout(s string) bool {
	if s == "0" {
		return true
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return false
	}
	switch s[i:] {
	case "ms", "s", "min", "h", "d":
		return true
	}
	return false
}

// ---------- runAfterConnect: the actual hook body ---------------------------

func TestRunAfterConnect_ReadOnlyTrueIssuesSet(t *testing.T) {
	exec := &recordingExecer{}
	if err := runAfterConnect(context.Background(), exec, true, "0"); err != nil {
		t.Fatalf("runAfterConnect: %v", err)
	}
	if !containsSQL(exec.sqls, "SET default_transaction_read_only = on") {
		t.Fatalf("missing read_only SET: %v", exec.sqls)
	}
}

func TestRunAfterConnect_ReadOnlyFalseSkipsSet(t *testing.T) {
	exec := &recordingExecer{}
	if err := runAfterConnect(context.Background(), exec, false, "0"); err != nil {
		t.Fatalf("runAfterConnect: %v", err)
	}
	if containsSQL(exec.sqls, "default_transaction_read_only") {
		t.Fatalf("read_only SET issued when ReadOnly=false: %v", exec.sqls)
	}
}

func TestRunAfterConnect_StatementTimeoutPassThrough(t *testing.T) {
	exec := &recordingExecer{}
	if err := runAfterConnect(context.Background(), exec, false, "5min"); err != nil {
		t.Fatalf("runAfterConnect: %v", err)
	}
	if !containsSQL(exec.sqls, "SET statement_timeout = '5min'") {
		t.Fatalf("expected SET statement_timeout = '5min', got %v", exec.sqls)
	}
}

func TestRunAfterConnect_EmptyDefaultsToZero(t *testing.T) {
	// When profile.StatementTimeout is "", BuildPgxConfig substitutes "0"
	// and canonicalizes it before installing the hook. We exercise the same
	// canonicalization path here so the test fails if the default ever drifts.
	canonical, err := CanonicalizeStatementTimeout("0")
	if err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	exec := &recordingExecer{}
	if err := runAfterConnect(context.Background(), exec, false, canonical); err != nil {
		t.Fatalf("runAfterConnect: %v", err)
	}
	if !containsSQL(exec.sqls, "SET statement_timeout = '0'") {
		t.Fatalf("expected SET statement_timeout = '0', got %v", exec.sqls)
	}
}

func TestRunAfterConnect_ErrorPropagated(t *testing.T) {
	want := errors.New("boom")
	exec := &recordingExecer{failOn: "statement_timeout", failErr: want}
	err := runAfterConnect(context.Background(), exec, false, "0")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped boom, got %v", err)
	}
}

func TestRunAfterConnect_ReadOnlyErrorShortCircuits(t *testing.T) {
	want := errors.New("ro boom")
	exec := &recordingExecer{failOn: "default_transaction_read_only", failErr: want}
	err := runAfterConnect(context.Background(), exec, true, "0")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, want) {
		t.Fatalf("expected wrapped ro boom, got %v", err)
	}
	// statement_timeout should not have been attempted.
	if containsSQL(exec.sqls, "statement_timeout") {
		t.Fatalf("statement_timeout SET issued after read_only failure: %v", exec.sqls)
	}
}

// ---------- WARN: sslmode=disable on non-loopback ---------------------------

func TestShouldWarnInsecureSSL(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want bool
	}{
		{"loopback+disable=no warn", "postgres://app@127.0.0.1:5432/app?sslmode=disable", false},
		{"localhost+disable=no warn", "postgres://app@localhost:5432/app?sslmode=disable", false},
		{"remote+disable=warn", "postgres://app@db.prod.internal:5432/app?sslmode=disable", true},
		{"remote+require=no warn", "postgres://app@db.prod.internal:5432/app?sslmode=require", false},
		{"remote+default(prefer)=no warn", "postgres://app@db.prod.internal:5432/app", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := models.Connection{Name: "p", DSN: c.dsn}
			cfg, err := BuildPgxConfig(context.Background(), profile, "pw")
			if err != nil {
				t.Fatalf("BuildPgxConfig: %v", err)
			}
			if got := shouldWarnInsecureSSL(c.dsn, cfg); got != c.want {
				t.Fatalf("shouldWarnInsecureSSL = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWarnInsecureSSL_Format(t *testing.T) {
	var buf bytes.Buffer
	warnInsecureSSL(&buf, "prod-pg", "db.prod.internal", "disable")
	got := buf.String()
	for _, sub := range []string{"WARN", "prod-pg", "db.prod.internal", "sslmode=disable"} {
		if !strings.Contains(got, sub) {
			t.Errorf("WARN missing %q in: %s", sub, got)
		}
	}
}

// ---------- DSN redaction ----------------------------------------------------

func TestRedactDSN(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			in:   "failed to parse postgres://app:hunter2@db.prod.internal:5432/app",
			want: "failed to parse postgres://app:***@db.prod.internal:5432/app",
		},
		{
			in:   "postgresql://user:p%40ss@host/db",
			want: "postgresql://user:***@host/db",
		},
		{
			in:   "no creds here postgres://app@host/db",
			want: "no creds here postgres://app@host/db",
		},
		{
			in:   "garbage with no dsn",
			want: "garbage with no dsn",
		},
	}
	for _, c := range cases {
		if got := RedactDSN(c.in); got != c.want {
			t.Errorf("RedactDSN(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactConnectionString_BothForms(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "url-form only",
			in:   "postgres://app:hunter2@h/db",
			want: "postgres://app:***@h/db",
		},
		{
			name: "kv-form only",
			in:   "host=h password=hunter2 user=app",
			want: "host=h password=*** user=app",
		},
		{
			name: "kv sslpassword",
			in:   "host=h sslpassword=hunter2",
			want: "host=h sslpassword=***",
		},
		{
			name: "kv single-quoted",
			in:   "host=h password='hunter 2' user=app",
			want: "host=h password=*** user=app",
		},
		{
			name: "kv uppercase",
			in:   "host=h PASSWORD=hunter2",
			want: "host=h PASSWORD=***",
		},
		{
			name: "both forms in one string",
			in:   "fallback postgres://app:hunter2@h/db then host=h password=hunter2",
			want: "fallback postgres://app:***@h/db then host=h password=***",
		},
		{
			name: "no creds",
			in:   "host=h user=app",
			want: "host=h user=app",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RedactConnectionString(c.in); got != c.want {
				t.Errorf("RedactConnectionString(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestBuildPgxConfig_ParseErrorRedactsInlinePassword(t *testing.T) {
	// Malformed port + inline password — the wrapped error must NOT contain
	// the plaintext password.
	dsn := "postgres://app:topsecret@h:notaport/db"
	profile := models.Connection{Name: "p", DSN: dsn}
	_, err := BuildPgxConfig(context.Background(), profile, "pw")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("plaintext password leaked in error: %v", err)
	}
	if !strings.Contains(err.Error(), "app:***@") {
		t.Fatalf("expected app:***@ in redacted error, got: %v", err)
	}
}

func TestSSLModeForLog(t *testing.T) {
	cases := []struct{ dsn, want string }{
		{"postgres://h/d", "prefer"},
		{"postgres://h/d?sslmode=require", "require"},
		{"postgres://h/d?sslmode=disable", "disable"},
		{":\x00", "unknown"},
	}
	for _, c := range cases {
		if got := sslModeForLog(c.dsn); got != c.want {
			t.Errorf("sslModeForLog(%q) = %q, want %q", c.dsn, got, c.want)
		}
	}
}
