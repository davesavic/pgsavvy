package i18n

import (
	"runtime"
	"testing"

	"golang.org/x/text/language"
)

func TestDetectLocale_ClearedEnv_FallsBackToEnglish(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("DetectLocale on darwin consults CFLocaleCopyCurrent; env-only test is unreliable")
	}

	// Clear all locale env vars consulted by go-locale on linux. After
	// these are cleared we expect go-locale to return either an error,
	// language.Und, or language.English. In all three cases DetectLocale
	// must return language.English.
	t.Setenv("LANGUAGE", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "")

	got := DetectLocale()

	// With env cleared, go-locale may either (a) error/Und → our fallback
	// returns language.English exactly, or (b) succeed via a non-env
	// source (e.g., /etc/locale.conf, system default) and return a tag
	// whose Base is English. Both are valid per D-LOC1 — what matters is
	// the result is a well-formed English-based tag.
	if got == (language.Tag{}) {
		t.Fatalf("DetectLocale returned the zero tag")
	}
	base, conf := got.Base()
	if conf == language.No {
		t.Fatalf("DetectLocale returned a tag with no base confidence: %v", got)
	}
	if base != language.MustParseBase("en") {
		t.Errorf("DetectLocale with cleared env: got base %v (tag=%v), want en", base, got)
	}
}

func TestDetectLocale_NeverReturnsZeroTag(t *testing.T) {
	got := DetectLocale()
	if got == (language.Tag{}) {
		t.Fatalf("DetectLocale returned the zero language.Tag")
	}
}
