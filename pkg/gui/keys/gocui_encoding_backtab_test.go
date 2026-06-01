package keys

import (
	"testing"

	"github.com/gdamore/tcell/v3"
	"github.com/jesseduffield/lazygit/pkg/gocui"
)

// TestKeyFromGocuiBacktab pins the decode of Shift+Tab. gocui surfaces it
// as gocui.KeyBacktab with no modifier (tcell folds Shift+Tab into the
// standalone Backtab key), and KeyFromGocui must map it to KeyBacktab —
// not the zero/KeyNone "drop" value, which is what made Shift+Tab a no-op.
func TestKeyFromGocuiBacktab(t *testing.T) {
	gk := gocui.NewKey(gocui.KeyName(tcell.KeyBacktab), "", gocui.ModNone)
	got := KeyFromGocui(gk)
	want := Key{Special: KeyBacktab}
	if got != want {
		t.Fatalf("KeyFromGocui(Backtab) = %+v; want %+v", got, want)
	}
}
