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
	NullValue       *Style
	Numeric         *Style
	String          *Style
	Keyword         *Style
	Comment         *Style
	Identifier      *Style
	Operator        *Style
	Error           *Style
	Warning         *Style
	Success         *Style
	Info            *Style
	PopupBorder     *Style
	TableHeader     *Style
	SearchHighlight *Style
	CurSearch       *Style // current in-grid search match (stronger than SearchHighlight)
	Prompt          *Style
	DirtyCell       *Style
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
		NullValue:       parseStyle(cfg.NullValue),
		Numeric:         parseStyle(cfg.Numeric),
		String:          parseStyle(cfg.String),
		Keyword:         parseStyle(cfg.Keyword),
		Comment:         parseStyle(cfg.Comment),
		Identifier:      parseStyle(cfg.Identifier),
		Operator:        parseStyle(cfg.Operator),
		Error:           parseStyle(cfg.Error),
		Warning:         parseStyle(cfg.Warning),
		Success:         parseStyle(cfg.Success),
		Info:            parseStyle(cfg.Info),
		PopupBorder:     parseStyle(cfg.PopupBorder),
		TableHeader:     parseStyle(cfg.TableHeader),
		SearchHighlight: parseStyle(cfg.SearchHighlight),
		CurSearch:       parseStyle(cfg.CurSearch),
		Prompt:          parseStyle(cfg.Prompt),
		DirtyCell:       parseBgStyle(cfg.DirtyCell),
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

// parseBgStyle parses a style string where the colour should be routed to the
// Bg field (not Fg). If the user has already used the "on" keyword the string is
// parsed normally; otherwise "on " is prepended so the colour lands in Bg.
// This allows bg-only fields like DirtyCell to accept a plain colour value
// (e.g. "#5a4410") without requiring the confusing "on" prefix.
func parseBgStyle(s string) *Style {
	if s == "" {
		return &Style{}
	}
	tokens := strings.Fields(s)
	for _, t := range tokens {
		if strings.ToLower(t) == "on" {
			return parseStyle(s)
		}
	}
	return parseStyle("on " + s)
}

// PreviewSGR returns a combined ANSI SGR escape that renders a style string in
// its own colours for live-preview displays (e.g. settings theme tab). It
// handles compound values like "black on yellow", "bold #ff8800", "on yellow",
// and single-colour tokens. Returns "" for empty, unknown, or monochrome
// inputs.
func PreviewSGR(s string) string {
	if s == "" || IsMonochrome() {
		return ""
	}
	st := parseStyle(s)
	var params []string
	if st.Bold {
		params = append(params, "1")
	}
	if st.Underline {
		params = append(params, "4")
	}
	if st.Italic {
		params = append(params, "3")
	}
	if st.Fg != "" {
		if p := ColorParamSGR(st.Fg, Fg); p != "" {
			params = append(params, p)
		}
	}
	if st.Bg != "" {
		if p := ColorParamSGR(st.Bg, Bg); p != "" {
			params = append(params, p)
		}
	}
	if len(params) == 0 {
		return ""
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
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
