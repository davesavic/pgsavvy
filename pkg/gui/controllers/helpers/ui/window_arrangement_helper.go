package ui

import (
	"github.com/jesseduffield/lazycore/pkg/boxlayout"
)

// Dimensions is the per-window rectangle returned by GetWindowDimensions.
// Re-exported from boxlayout so layout consumers do not need to import
// the upstream package directly; the field set matches boxlayout exactly.
type Dimensions = boxlayout.Dimensions

// requiredWindows is the canonical list of window keys the layout
// promises to populate. The dbsavvy-zro AC names exactly these eleven;
// future epics may extend the list (e.g. result-tabs N) but must NOT
// drop any of these without a coordinated update of every consumer.
var requiredWindows = []string{
	"schemas",
	"tables",
	"main",
	"secondary",
	"status",
	"options",
	"popup-overlay",
}

// GetWindowDimensions returns the per-window rectangle map for a
// terminal of the supplied (width, height). The output ALWAYS contains
// every entry in requiredWindows; even if the terminal is too small to
// render a slot at non-zero size, the entry is present with whatever
// dimensions boxlayout assigned (zero-area is fine — the layout
// callback in pkg/gui/layout.go is responsible for the "too small"
// limit overlay).
//
// Box tree (DESIGN.md §7):
//
//	root ROW
//	├── "options"  size=1            (top bar — "[Connection: prod-pg]")
//	├── body ROW (weight 1)
//	│   └── upper COLUMN (weight 1)
//	│       ├── left rail ROW size=24
//	│       │   ├── "schemas"     weight=1
//	│       │   └── "tables"      weight=1
//	│       └── right ROW (weight 1)
//	│           ├── "main"      weight=1
//	│           └── "secondary" weight=1
//	└── "status" size=2              (bottom options/progress bar, 2 rows)
//
// `popup-overlay` is added to the map AFTER the box arrangement runs:
// boxlayout has no native overlay support, and the popup contexts
// position themselves via the focus stack rather than via the slot
// grid. We hand it the full screen rectangle so the consuming layout
// callback can centre the popup view against a known canvas.
func GetWindowDimensions(width, height int) map[string]Dimensions {
	root := &boxlayout.Box{
		Direction: boxlayout.ROW,
		Children: []*boxlayout.Box{
			{Window: "options", Size: 1},
			{
				Direction: boxlayout.ROW,
				Weight:    1,
				Children: []*boxlayout.Box{
					{
						Direction: boxlayout.COLUMN,
						Weight:    1,
						Children: []*boxlayout.Box{
							{
								Direction: boxlayout.ROW,
								Size:      24,
								Children: []*boxlayout.Box{
									{Window: "schemas", Weight: 1},
									{Window: "tables", Weight: 1},
								},
							},
							{
								Direction: boxlayout.ROW,
								Weight:    1,
								Children: []*boxlayout.Box{
									{Window: "main", Weight: 1},
									{Window: "secondary", Weight: 1},
								},
							},
						},
					},
				},
			},
			{Window: "status", Size: 2},
		},
	}

	out := boxlayout.ArrangeWindows(root, 0, 0, width, height)

	// popup-overlay covers the full screen by design — the popup contexts
	// centre themselves against this canvas in pkg/gui/layout.go (E5+).
	out["popup-overlay"] = Dimensions{X0: 0, Y0: 0, X1: width - 1, Y1: height - 1}

	// Guarantee every required key is present, even at zero area, so
	// downstream callers can map-access without a nil check.
	for _, name := range requiredWindows {
		if _, ok := out[name]; !ok {
			out[name] = Dimensions{}
		}
	}
	return out
}

// RequiredWindows returns a copy of the canonical key list. Useful for
// tests asserting the map shape.
func RequiredWindows() []string {
	out := make([]string, len(requiredWindows))
	copy(out, requiredWindows)
	return out
}
