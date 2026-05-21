package i18n

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path"

	"github.com/spf13/afero"
)

// maxOverlayBytes caps locale-file reads at 1 MiB. Anything larger is treated
// as malicious or accidental and ignored. See LoadAndMerge for the detection
// strategy.
const maxOverlayBytes = 1 << 20

//go:embed translations/*.json
var embeddedTranslations embed.FS

// LoadAndMerge returns a TranslationSet for the given language code (e.g.,
// "en", "fr"). The returned set is a freshly allocated EnglishTranslationSet
// with any fields present in the locale overlay JSON file replaced.
//
// Overlay lookup tries the provided afero.Fs first (path
// "translations/<lang>.json"); if it is nil, missing, or unreadable, it falls
// back to the package's embedded translations/<lang>.json. When neither source
// supplies a usable file, the English baseline is returned with a debug log
// and a nil error.
//
// Failure modes are all soft (English fallback + logged warning):
//   - file larger than 1 MiB: ignored to bound memory and protect against
//     malicious overlays;
//   - malformed JSON: a freshly allocated English baseline is returned so
//     callers never observe partial unmarshal state from the failed read.
//
// If logger is nil, a discard logger is used. LoadAndMerge never panics and
// never returns a nil *TranslationSet.
func LoadAndMerge(filesystem afero.Fs, lang string, logger *slog.Logger) (*TranslationSet, error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	set := EnglishTranslationSet()
	if lang == "" {
		logger.Debug("i18n: empty lang; using English baseline")
		return set, nil
	}

	rel := path.Join("translations", lang+".json")
	data, source, found := readOverlay(filesystem, rel, logger)
	if !found {
		logger.Debug(fmt.Sprintf("i18n: no overlay found for %q; using English baseline", lang))
		return set, nil
	}

	// Oversize check: readOverlay reads up to maxOverlayBytes+1 bytes; any
	// payload larger than the cap is rejected before unmarshal to bound
	// memory and avoid trusting attacker-controlled file sizes.
	if len(data) > maxOverlayBytes {
		logger.Warn(fmt.Sprintf("i18n: overlay %q (source=%s) exceeds %d bytes; using English baseline", lang, source, maxOverlayBytes))
		return EnglishTranslationSet(), nil
	}

	if err := json.Unmarshal(data, set); err != nil {
		logger.Warn(fmt.Sprintf("i18n: overlay %q (source=%s) is malformed: %v; using English baseline", lang, source, err))
		// Re-allocate so callers never see partial mutation from the
		// failed unmarshal.
		return EnglishTranslationSet(), nil
	}

	return set, nil
}

// readOverlay attempts to read the overlay JSON from the provided afero.Fs,
// falling back to the package's embedded FS. The returned source string is
// "afero" or "embed" for diagnostic logging. The boolean is true only when a
// readable file was found in at least one source.
//
// Reads are bounded by maxOverlayBytes+1: callers detect oversize by checking
// len(data) > maxOverlayBytes.
func readOverlay(filesystem afero.Fs, rel string, logger *slog.Logger) ([]byte, string, bool) {
	if filesystem != nil {
		if data, ok := readAferoFile(filesystem, rel, logger); ok {
			return data, "afero", true
		}
	}
	if data, ok := readEmbeddedFile(rel, logger); ok {
		return data, "embed", true
	}
	return nil, "", false
}

func readAferoFile(filesystem afero.Fs, rel string, logger *slog.Logger) ([]byte, bool) {
	f, err := filesystem.Open(rel)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Warn(fmt.Sprintf("i18n: open afero overlay %q: %v", rel, err))
		}
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxOverlayBytes+1))
	if err != nil {
		logger.Warn(fmt.Sprintf("i18n: read afero overlay %q: %v", rel, err))
		return nil, false
	}
	return data, true
}

func readEmbeddedFile(rel string, logger *slog.Logger) ([]byte, bool) {
	f, err := embeddedTranslations.Open(rel)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			logger.Warn(fmt.Sprintf("i18n: open embedded overlay %q: %v", rel, err))
		}
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxOverlayBytes+1))
	if err != nil {
		logger.Warn(fmt.Sprintf("i18n: read embedded overlay %q: %v", rel, err))
		return nil, false
	}
	return data, true
}
