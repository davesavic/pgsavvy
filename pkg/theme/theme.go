package theme

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/theme/builtin"
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
	SelectedRowBg   *Style
	SelectedRowFg   *Style
	NullValueFg     *Style
	NumericFg       *Style
	StringFg        *Style
	KeywordFg       *Style
	CommentFg       *Style
	IdentifierFg    *Style
	OperatorFg      *Style
	BackgroundBg    *Style
	ForegroundFg    *Style
	StatusBarBg     *Style
	StatusBarFg     *Style
	CommandLineBg   *Style
	CommandLineFg   *Style
	ErrorFg         *Style
	WarningFg       *Style
	SuccessFg       *Style
	InfoFg          *Style
	HintFg          *Style
	PopupBg         *Style
	PopupFg         *Style
	PopupBorder     *Style
	MenuBg          *Style
	MenuFg          *Style
	MenuSelectedBg  *Style
	MenuSelectedFg  *Style
	TableHeaderBg   *Style
	TableHeaderFg   *Style
	TableRowAltBg   *Style
	GutterFg        *Style
	LineNumberFg    *Style
	CursorBg        *Style
	CursorFg        *Style
	MatchHighlight  *Style
	SearchHighlight *Style
	DiffAddedFg     *Style
	DiffRemovedFg   *Style
	DiffChangedFg   *Style
	PromptFg        *Style
	DirtyCellBg     *Style // dbsavvy-bwq.6 (A3) — Z1 wires ThemeConfig + builtins
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
		SelectedRowBg:   parseStyle(cfg.SelectedRowBg),
		SelectedRowFg:   parseStyle(cfg.SelectedRowFg),
		NullValueFg:     parseStyle(cfg.NullValueFg),
		NumericFg:       parseStyle(cfg.NumericFg),
		StringFg:        parseStyle(cfg.StringFg),
		KeywordFg:       parseStyle(cfg.KeywordFg),
		CommentFg:       parseStyle(cfg.CommentFg),
		IdentifierFg:    parseStyle(cfg.IdentifierFg),
		OperatorFg:      parseStyle(cfg.OperatorFg),
		BackgroundBg:    parseStyle(cfg.BackgroundBg),
		ForegroundFg:    parseStyle(cfg.ForegroundFg),
		StatusBarBg:     parseStyle(cfg.StatusBarBg),
		StatusBarFg:     parseStyle(cfg.StatusBarFg),
		CommandLineBg:   parseStyle(cfg.CommandLineBg),
		CommandLineFg:   parseStyle(cfg.CommandLineFg),
		ErrorFg:         parseStyle(cfg.ErrorFg),
		WarningFg:       parseStyle(cfg.WarningFg),
		SuccessFg:       parseStyle(cfg.SuccessFg),
		InfoFg:          parseStyle(cfg.InfoFg),
		HintFg:          parseStyle(cfg.HintFg),
		PopupBg:         parseStyle(cfg.PopupBg),
		PopupFg:         parseStyle(cfg.PopupFg),
		PopupBorder:     parseStyle(cfg.PopupBorder),
		MenuBg:          parseStyle(cfg.MenuBg),
		MenuFg:          parseStyle(cfg.MenuFg),
		MenuSelectedBg:  parseStyle(cfg.MenuSelectedBg),
		MenuSelectedFg:  parseStyle(cfg.MenuSelectedFg),
		TableHeaderBg:   parseStyle(cfg.TableHeaderBg),
		TableHeaderFg:   parseStyle(cfg.TableHeaderFg),
		TableRowAltBg:   parseStyle(cfg.TableRowAltBg),
		GutterFg:        parseStyle(cfg.GutterFg),
		LineNumberFg:    parseStyle(cfg.LineNumberFg),
		CursorBg:        parseStyle(cfg.CursorBg),
		CursorFg:        parseStyle(cfg.CursorFg),
		MatchHighlight:  parseStyle(cfg.MatchHighlight),
		SearchHighlight: parseStyle(cfg.SearchHighlight),
		DiffAddedFg:     parseStyle(cfg.DiffAddedFg),
		DiffRemovedFg:   parseStyle(cfg.DiffRemovedFg),
		DiffChangedFg:   parseStyle(cfg.DiffChangedFg),
		PromptFg:        parseStyle(cfg.PromptFg),
	}
	current.Store(next)
	return nil
}

// parseStyle turns a config color string into a *Style. Unknown or empty
// values still produce a non-nil Style so downstream code can rely on the
// pointer being valid; the rendering layer decides what an unrecognised Fg
// means in practice.
//
// Tokenization (AD-5, dbsavvy-56u.4): the value is split on whitespace and
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
// dbsavvy-uv0.8.
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
// need to invalidate; the variable is read at startup. dbsavvy-uv0.8.
func IsMonochrome() bool {
	monochromeOnce.Do(func() {
		monochrome = os.Getenv("NO_COLOR") != ""
	})
	return monochrome
}
