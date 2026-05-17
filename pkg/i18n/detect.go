package i18n

import (
	golocale "github.com/Xuanwo/go-locale"
	"golang.org/x/text/language"
)

// DetectLocale returns the best-effort language tag for the current host.
// Resolution order:
//
//  1. go-locale.Detect() — consults LANGUAGE, LC_ALL, LC_MESSAGES, LC_CTYPE,
//     LANG, then OS-specific sources (CFLocaleCopyCurrent on macOS, etc.);
//  2. if Detect returns an error, language.Und, or a tag whose Base() carries
//     language.No confidence, fall back to language.English.
//
// DetectLocale never panics and always returns a non-zero language.Tag.
func DetectLocale() language.Tag {
	tag, err := golocale.Detect()
	if err != nil {
		return language.English
	}
	if tag == language.Und {
		return language.English
	}
	if _, conf := tag.Base(); conf == language.No {
		return language.English
	}
	return tag
}
