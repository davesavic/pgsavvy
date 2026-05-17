// Package i18n provides localized UI strings for dbsavvy. It exposes the
// English baseline (EnglishTranslationSet) plus LoadAndMerge, which overlays
// locale-specific JSON files (read first from an afero.Fs, falling back to the
// embedded translations/*.json) onto a fresh English clone using stdlib
// encoding/json. DetectLocale picks the best language tag from the host
// environment via go-locale, with safe fallbacks to English.
package i18n
