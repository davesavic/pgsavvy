package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"unicode/utf8"
)

// resetEnv unsets the AD-14 env opt-ins and restores them on t.Cleanup so
// tests are independent of the developer's local environment.
func resetEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envIncludeSQL, "")
	t.Setenv(envIncludeParams, "")
}

func TestSQLPreview_TruncatesAt200Bytes(t *testing.T) {
	resetEnv(t)
	in := strings.Repeat("a", 250)
	got := sqlPreview(in, "")
	if len(got) != sqlPreviewMax {
		t.Errorf("len = %d, want %d", len(got), sqlPreviewMax)
	}
}

func TestSQLPreview_ShorterThanMaxReturnedVerbatim(t *testing.T) {
	resetEnv(t)
	in := "SELECT 1"
	if got := sqlPreview(in, ""); got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

func TestSQLPreview_UTF8Boundary(t *testing.T) {
	resetEnv(t)
	// Build a string of 199 'a' + one 3-byte rune; sqlPreviewMax=200 cuts
	// in the middle of that rune. Expect the preview to drop the rune,
	// length=199.
	in := strings.Repeat("a", 199) + "界" // 'a'*199 + 3 bytes
	got := sqlPreview(in, "")
	if !utf8.ValidString(got) {
		t.Errorf("preview is invalid UTF-8: %q", got)
	}
	if len(got) != 199 {
		t.Errorf("len = %d, want 199 (rune dropped)", len(got))
	}
}

func TestSQLPreview_ScrubsConnectionPassword(t *testing.T) {
	resetEnv(t)
	in := "SELECT 'hunter2' AS pw"
	got := sqlPreview(in, "hunter2")
	if strings.Contains(got, "hunter2") {
		t.Errorf("preview still contains password: %q", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("preview missing redaction marker: %q", got)
	}
}

func TestSQLPreview_FullEnvOptInSkipsScrubAndTruncation(t *testing.T) {
	resetEnv(t)
	t.Setenv(envIncludeSQL, "full")
	in := strings.Repeat("a", 250) + "hunter2"
	got := sqlPreview(in, "hunter2")
	if got != in {
		t.Errorf("opt-in did not yield verbatim; got %q", got)
	}
}

func TestParamsHashes_EmptyArgsReturnsNil(t *testing.T) {
	resetEnv(t)
	if got := paramsHashes(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestParamsHashes_HashesByDefault(t *testing.T) {
	resetEnv(t)
	got := paramsHashes([]any{"alice", "hunter2"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for i, h := range got {
		if len(h) != paramHashLen {
			t.Errorf("hashes[%d] len = %d, want %d", i, len(h), paramHashLen)
		}
		if h == "alice" || h == "hunter2" {
			t.Errorf("hashes[%d] = %q is the raw value", i, h)
		}
	}
	// Verify the hash matches sha256.
	want := hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("hunter2")); return s[:] }())[:paramHashLen]
	if got[1] != want {
		t.Errorf("hashes[1] = %q, want %q", got[1], want)
	}
}

func TestParamsHashes_FirstFiveOnly(t *testing.T) {
	resetEnv(t)
	args := []any{"a", "b", "c", "d", "e", "f", "g"}
	got := paramsHashes(args)
	if len(got) != paramsHashCount {
		t.Errorf("len = %d, want %d", len(got), paramsHashCount)
	}
}

func TestParamsHashes_VerbatimWithEnvOptIn(t *testing.T) {
	resetEnv(t)
	t.Setenv(envIncludeParams, "1")
	got := paramsHashes([]any{"alice", "hunter2"})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != "alice" || got[1] != "hunter2" {
		t.Errorf("got %v, want raw values", got)
	}
}

func TestNoticePreview_ScrubsDSNAndPassword(t *testing.T) {
	in := "duplicate key error host=h password=secretpw user=u — value hunter2"
	got := noticePreview(in, "hunter2")
	if strings.Contains(got, "hunter2") {
		t.Errorf("preview contains password: %q", got)
	}
	if strings.Contains(got, "secretpw") {
		t.Errorf("preview contains kv-form DSN password: %q", got)
	}
}
