package keys

import (
	"time"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// WhichKeyNotifier is the interface the Matcher uses to drive the
// which-key popup. The concrete implementation ships in dlp.6; the
// Matcher (dlp.5) only consumes this interface.
//
// ShowAfter is called when the Matcher enters a PARTIAL state. The
// implementation typically schedules a timer that, on fire, renders a
// popup describing the children reachable from prefix. The Matcher
// passes the configured `whichkey_delay`; the notifier may also choose
// to render immediately or coalesce repeated calls.
//
// Hide is called by Matcher.Cancel (and on Dispatched leaf fires) to
// pull down any visible popup. Hide MUST be invoked OUTSIDE the
// Matcher's internal mutex — implementations therefore MUST NOT call
// back into the Matcher synchronously.
type WhichKeyNotifier interface {
	ShowAfter(delay time.Duration, scope types.ContextKey, prefix []Key)
	Hide()
}
