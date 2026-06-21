package theme

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/theme/builtin"
)

// Style is the resolved presentation for a single themed element. It is a
// plain value type with no third-party dependency so the rendering layer can
// translate it into terminal escape sequences.
type Style struct {
	Fg        string
	Bg        string
	Bold      bool
	Underline bool
	Italic    bool
}

// themeState mirrors every exported string field of config.ThemeConfig as a
// *Style. A nil field means the user did not configure that element; the
// rendering layer should treat it as the zero Style.
type themeState struct {
	ActiveBorder    *Style
	InactiveBorder  *Style
	NullValueFg     *Style
	NumericFg       *Style
	StringFg        *Style
	KeywordFg       *Style
	CommentFg       *Style
	IdentifierFg    *Style
	OperatorFg      *Style
	ErrorFg         *Style
	WarningFg       *Style
	SuccessFg       *Style
	InfoFg          *Style
	PopupBorder     *Style
	TableHeaderFg   *Style
	SearchHighlight *Style
	CurSearch       *Style // current in-grid search match (stronger than SearchHighlight)
	PromptFg        *Style
	DirtyCellBg     *Style // Z1 wires ThemeConfig + builtins
	WarnBorder      *Style // Z1 Phase A wires ThemeConfig + builtins
}

var (
	current   atomic.Pointer[themeState]
	initOnce  sync.Once
	errNilCfg = errors.New("theme.Apply: nil cfg")
)

// Current returns the currently-active theme snapshot. The pointer is safe to
// read concurrently with calls to Apply; readers should treat the returned
// snapshot as immutable. Current never returns nil: if no theme has been
// applied yet it lazily applies the built-in default-dark theme.
func Current() *themeState {
	if s := current.Load(); s != nil {
		return s
	}
	initOnce.Do(func() {
		_ = Apply(builtin.DefaultDark())
	})
	return current.Load()
}

// Apply atomically swaps the active theme to the one described by cfg. It
// returns a non-nil error when cfg is nil, in which case the previously
// applied theme is preserved. Unknown color strings are accepted and stored
// verbatim; the rendering layer decides how to interpret them.
func Apply(cfg *config.ThemeConfig) error {
	if cfg == nil {
		return errNilCfg
	}
	next := &themeState{
		ActiveBorder:    parseStyle(cfg.ActiveBorder),
		InactiveBorder:  parseStyle(cfg.InactiveBorder),
		NullValueFg:     parseStyle(cfg.NullValueFg),
		NumericFg:       parseStyle(cfg.NumericFg),
		StringFg:        parseStyle(cfg.StringFg),
		KeywordFg:       parseStyle(cfg.KeywordFg),
		CommentFg:       parseStyle(cfg.CommentFg),
		IdentifierFg:    parseStyle(cfg.IdentifierFg),
		OperatorFg:      parseStyle(cfg.OperatorFg),
		ErrorFg:         parseStyle(cfg.ErrorFg),
		WarningFg:       parseStyle(cfg.WarningFg),
		SuccessFg:       parseStyle(cfg.SuccessFg),
		InfoFg:          parseStyle(cfg.InfoFg),
		PopupBorder:     parseStyle(cfg.PopupBorder),
		TableHeaderFg:   parseStyle(cfg.TableHeaderFg),
		SearchHighlight: parseStyle(cfg.SearchHighlight),
		CurSearch:       parseStyle(cfg.CurSearch),
		PromptFg:        parseStyle(cfg.PromptFg),
		DirtyCellBg:     parseStyle(cfg.DirtyCellBg),
		WarnBorder:      parseStyle(cfg.WarnBorder),
	}
	current.Store(next)
	return nil
}

// parseStyle turns a config color string into a *Style. Unknown or empty
// values still produce a non-nil Style so downstream code can rely on the
// pointer being valid; the rendering layer decides what an unrecognised Fg
// means in practice.
//
// Tokenization (AD-5): the value is split on whitespace and
// each token is classified greedily:
//   - "bold" / "underline" / "italic" set the matching flag (any position,
//     order-insensitive);
//   - "on" consumes the next token as Bg;
//   - the FIRST non-attribute, non-"on" token becomes Fg;
//   - any further non-attribute tokens (after Fg is already set) are
//     unknown and skipped with a slog Debug emit under cat=theme so a
//     misconfigured theme leaves a trace without spamming the user.
//
// Backward compat: a single-token "red" continues to land as Fg=red with
// every flag at the zero value, so existing user configs keep working
// unchanged.
func parseStyle(s string) *Style {
	out := &Style{}
	if s == "" {
		return out
	}
	tokens := strings.Fields(s)
	for i := 0; i < len(tokens); i++ {
		tok := strings.ToLower(tokens[i])
		switch tok {
		case "bold":
			out.Bold = true
		case "underline":
			out.Underline = true
		case "italic":
			out.Italic = true
		case "on":
			// Consume the next token as Bg. A trailing "on" with no
			// follower is logged and ignored — same policy as unknown
			// tokens.
			if i+1 < len(tokens) {
				out.Bg = tokens[i+1]
				i++
			} else {
				slog.Debug("theme: trailing 'on' without bg token", "cat", "theme", "input", s)
			}
		default:
			if out.Fg == "" {
				out.Fg = tokens[i]
				continue
			}
			slog.Debug("theme: unknown style token", "cat", "theme", "token", tokens[i], "input", s)
		}
	}
	return out
}

func init() {
	_ = Apply(builtin.DefaultDark())
}

// monochrome caches the result of reading the NO_COLOR env var. Resolved
// lazily on first IsMonochrome() call (via monochromeOnce) so test code
// that mutates the environment before reaching the call site is honored.
var (
	monochromeOnce sync.Once
	monochrome     bool
)

// IsMonochrome reports whether the runtime should suppress color output
// in renderers that otherwise paint accent colors (e.g. EXPLAIN plan
// cost-percentile coloring). Returns true when the NO_COLOR environment
// variable is set to any non-empty value (per https://no-color.org).
//
// The value is resolved once on first call and cached for the lifetime
// of the process — subsequent calls are O(1). Production callers do not
// need to invalidate; the variable is read at startup.
func IsMonochrome() bool {
	monochromeOnce.Do(func() {
		monochrome = os.Getenv("NO_COLOR") != ""
	})
	return monochrome
}

// SetMonochromeForTest forces the cached monochrome value to v, marking the
// once as resolved so subsequent IsMonochrome() calls return v regardless of
// the NO_COLOR env var. It returns a restore func that reverts the once + value
// to their prior state. Test-only seam: the production
// monochrome cache is a process-lifetime sync.Once with no env re-read, so a
// no-color render test cannot otherwise drive IsMonochrome deterministically.
// Do NOT call from production code.
func SetMonochromeForTest(v bool) (restore func()) {
	prevVal := monochrome
	monochromeOnce = sync.Once{}
	monochromeOnce.Do(func() { monochrome = v })
	return func() {
		monochromeOnce = sync.Once{}
		monochromeOnce.Do(func() { monochrome = prevVal })
	}
}
