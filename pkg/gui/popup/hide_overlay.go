package popup

import (
	"errors"
	"strings"
)

// HideOverlay is the state object backing the <leader>gH hide-cols
// overlay. It owns the cursor + per-column hide flags during the
// overlay's lifetime; the ResultTabsHelper drives lifecycle (open /
// close / toggle / move) on behalf of the input controller and reads
// the final HiddenSet on close to apply it to the grid.
//
// The overlay is NOT itself a gocui context; rendering is done by the
// caller via Body() (renderable text). The chord wiring + focus stack
// integration is the orchestrator's responsibility.
//
// dbsavvy-uv0.6.
type HideOverlay struct {
	names   []string
	hidden  map[int]bool
	cursor  int
	persist bool
}

// ErrMinimumOneVisible is returned by Toggle when the user tries to
// hide the last visible column. The minimum-one-visible rule is
// enforced overlay-side per the dbsavvy-uv0.6 AC. dbsavvy-uv0.6.
var ErrMinimumOneVisible = errors.New("at least one column must remain visible")

// NewHideOverlay constructs an overlay over names with the supplied
// initial hidden set. persistEnabled drives the footer text; the
// actual persistence is the helper's responsibility on close.
//
// The initialHidden map is defensively copied; the overlay owns its
// own mutable copy.
func NewHideOverlay(names []string, initialHidden map[int]bool, persistEnabled bool) *HideOverlay {
	cp := make(map[int]bool, len(initialHidden))
	for k, v := range initialHidden {
		if !v {
			continue
		}
		if k < 0 || k >= len(names) {
			continue
		}
		cp[k] = true
	}
	return &HideOverlay{
		names:   append([]string(nil), names...),
		hidden:  cp,
		cursor:  0,
		persist: persistEnabled,
	}
}

// Names returns the column-name slice the overlay was constructed
// against. Caller must not mutate the returned slice.
func (o *HideOverlay) Names() []string { return o.names }

// SetNames replaces the rendered column labels. The hide/visible state
// is index-keyed and untouched, so this only changes how rows are
// displayed — used to upgrade bare labels to table-qualified ones once
// the lazy OID->relname resolution completes. A length mismatch is
// ignored to keep the index mapping consistent.
func (o *HideOverlay) SetNames(names []string) {
	if len(names) != len(o.names) {
		return
	}
	o.names = append([]string(nil), names...)
}

// Cursor returns the current cursor row (0-based).
func (o *HideOverlay) Cursor() int { return o.cursor }

// MoveCursor advances the cursor by d, clamping to [0, len(names)-1].
// A nil / empty overlay is a no-op.
func (o *HideOverlay) MoveCursor(d int) {
	if len(o.names) == 0 {
		return
	}
	o.cursor += d
	if o.cursor < 0 {
		o.cursor = 0
	}
	if o.cursor > len(o.names)-1 {
		o.cursor = len(o.names) - 1
	}
}

// Toggle flips the hide flag for the column under the cursor. Returns
// ErrMinimumOneVisible when toggling would hide the last visible
// column (caller surfaces a toast and leaves the keypress un-applied).
func (o *HideOverlay) Toggle() error {
	if len(o.names) == 0 {
		return nil
	}
	if o.hidden[o.cursor] {
		// Currently hidden → about to make visible: always allowed.
		delete(o.hidden, o.cursor)
		return nil
	}
	// Currently visible → about to hide: reject when this would leave
	// zero visible columns.
	if len(o.hidden)+1 >= len(o.names) {
		return ErrMinimumOneVisible
	}
	o.hidden[o.cursor] = true
	return nil
}

// HiddenSet returns a defensive copy of the current hidden-index set.
// Caller may mutate the returned map.
func (o *HideOverlay) HiddenSet() map[int]bool {
	if len(o.hidden) == 0 {
		return nil
	}
	out := make(map[int]bool, len(o.hidden))
	for k, v := range o.hidden {
		if v {
			out[k] = true
		}
	}
	return out
}

// PersistEnabled reports whether the overlay was opened in
// persistence-enabled mode (i.e. the underlying tab's result has a
// stable row identity).
func (o *HideOverlay) PersistEnabled() bool { return o.persist }

// Body renders the overlay as a text body suitable for writing into a
// gocui view. Lines:
//
//	hide columns (<space> toggle, <esc> apply)
//
//	  [x] col1
//	> [ ] col2
//	  [x] col3
//
//	(not persisted — query is not a single base table)
//
// The cursor row gets a "> " prefix; non-cursor rows get "  ".
// Hidden columns render with "[x]"; visible with "[ ]". The footer
// only appears when persistence is disabled.
func (o *HideOverlay) Body() string {
	var b strings.Builder
	b.WriteString("hide columns (<space> toggle, <esc> apply)\n")
	for i, name := range o.names {
		b.WriteByte('\n')
		if i == o.cursor {
			b.WriteString("> ")
		} else {
			b.WriteString("  ")
		}
		if o.hidden[i] {
			b.WriteString("[x] ")
		} else {
			b.WriteString("[ ] ")
		}
		b.WriteString(name)
	}
	if !o.persist {
		b.WriteString("\n\n")
		b.WriteString("(not persisted — query is not a single base table)")
	}
	return b.String()
}
