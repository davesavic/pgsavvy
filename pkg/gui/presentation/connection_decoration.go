package presentation

import (
	"strings"

	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// BorderStyleFor returns the TextStyle that should drive the border of any
// popup or rail associated with conn. When conn is nil or carries no
// Color, the helper falls back to theme.Current().PopupBorder so the
// rendering layer never has to special-case a missing connection.
//
// theme.Current() is invoked on every call (no caching) so a theme
// hot-reload between two BorderStyleFor invocations is reflected
// immediately on the next render pass.
func BorderStyleFor(conn *models.Connection) types.TextStyle {
	if conn == nil || conn.Color == "" {
		return popupBorderFallback()
	}
	return types.TextStyle{Fg: ResolveColor(conn.Color)}
}

// HeaderTextFor returns the "<icon> <label>" decoration shown in title
// bars, the left-rail header band, and connection-picker rows. Empty
// strings collapse: a missing icon or label is omitted; both missing
// yields the empty string. A nil connection returns the empty string.
func HeaderTextFor(conn *models.Connection) string {
	if conn == nil {
		return ""
	}
	icon := conn.Icon
	label := conn.Label
	switch {
	case icon == "" && label == "":
		return ""
	case icon == "":
		return label
	case label == "":
		return icon
	default:
		return icon + " " + label
	}
}

// ResolveColor maps a connection-supplied colour string onto the value
// passed to the rendering layer. Today it is a deliberately small
// pass-through:
//
//   - the empty string returns the empty string;
//   - everything else (hex like "#ff4d4d" or "#abc", named colours like
//     "red", and any other non-empty token) is returned verbatim.
//
// Full theme-token resolution (e.g. translating "danger" into the active
// theme's error colour) is deferred to E12; the AC explicitly carves that
// scope out of this task.
func ResolveColor(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "#") {
		return s
	}
	return s
}

// popupBorderFallback returns the theme's current PopupBorder rendered as
// a types.TextStyle. theme.Current() never returns nil; PopupBorder is a
// non-nil *Style by the parseStyle contract, so this helper is safe to
// call without further nil checks.
func popupBorderFallback() types.TextStyle {
	pb := theme.Current().PopupBorder
	if pb == nil {
		return types.TextStyle{}
	}
	return types.TextStyle{Fg: pb.Fg, Bg: pb.Bg, Bold: pb.Bold}
}

// NewPresentationHook returns the closure assigned to
// types.ContextTreeDeps.PresentationHook. It is a thin composition of
// BorderStyleFor and HeaderTextFor; both helpers read theme.Current()
// freshly on every invocation.
func NewPresentationHook() func(conn *models.Connection) (types.TextStyle, string) {
	return func(conn *models.Connection) (types.TextStyle, string) {
		return BorderStyleFor(conn), HeaderTextFor(conn)
	}
}

// NewPerRowDecorationHook returns the closure assigned to
// types.ContextTreeDeps.PerRowDecorationHook. The picker rendering pass
// uses the returned tuple to draw icon, label, and colour swatch per row.
// A nil connection produces three empty strings so the caller's nil
// branch keeps working.
//
// The label returned is Profile.Label when set, falling back to
// Profile.Name (the stable, user-supplied key in connections.yml) when
// Label is empty. This mirrors the status-bar / title-bar header
// decoration (see HeaderTextFor) so a profile's friendly label shows
// consistently across chrome. Two profiles deliberately given the same
// Label will render identically here; Name (the unique key) plus the
// host/db suffix remain the disambiguators.
//
// activeID is a LIVE accessor for the currently-active connection's name
// (g.activeConnID). It is called on every render so the marker tracks
// connect/disconnect without re-wiring. When activeID returns a non-empty
// value matching conn.Name the row's icon is overridden with the connected
// marker "●", regardless of the profile's own Icon, so the user can see at
// a glance which profile is live. A nil activeID, an empty result, or a
// non-matching name leaves the profile's own Icon untouched.
func NewPerRowDecorationHook(activeID func() string) func(conn *models.Connection) (icon, label, color string) {
	return func(conn *models.Connection) (string, string, string) {
		if conn == nil {
			return "", "", ""
		}
		icon := conn.Icon
		if activeID != nil {
			if id := activeID(); id != "" && id == conn.Name {
				icon = "●"
			}
		}
		label := conn.Label
		if label == "" {
			label = conn.Name
		}
		return icon, label, ResolveColor(conn.Color)
	}
}

// NewLimitText returns the closure assigned to
// types.ContextTreeDeps.LimitText. The closure resolves tr lazily so
// callers can swap the translation set at runtime; nil tr collapses to
// the empty string rather than panicking.
func NewLimitText(tr *i18n.TranslationSet) func() string {
	return func() string {
		if tr == nil {
			return ""
		}
		return tr.TerminalTooSmall
	}
}
