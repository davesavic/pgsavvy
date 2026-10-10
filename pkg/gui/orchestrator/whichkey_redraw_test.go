package orchestrator_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

type whichKeyUpdateDriver struct {
	*testfake.RecorderGuiDriver
	fullUpdates chan func() error
}

func (d *whichKeyUpdateDriver) Update(fn func() error) {
	d.fullUpdates <- fn
}

func TestWhichKey_DelayedLeaderRequestsFullLayout(t *testing.T) {
	fs := afero.NewMemMapFs()
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(slog.New(slog.DiscardHandler), i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:          c,
		Store:           common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock()),
		ConnectionsPath: "/tmp/connections.yml",
	}, orchestrator.WithKeyDelays(time.Second, time.Millisecond, 20*time.Millisecond))
	d := &whichKeyUpdateDriver{testfake.NewRecorderGuiDriver(), make(chan func() error, 16)}
	if err := g.UseDriverForTest(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		g.Matcher().Cancel()
		_ = g.Close()
	})
	if err := g.RunLayout(130, 42); err != nil {
		t.Fatal(err)
	}
	for len(d.fullUpdates) > 0 {
		<-d.fullUpdates
	}
	if _, err := g.Matcher().Dispatch(types.QUERY_EDITOR, keys.Key{Code: ' '}); err != nil {
		t.Fatal(err)
	}
	if !g.Matcher().IsPartial() {
		t.Fatal("leader did not leave a pending chord")
	}
	select {
	case fn := <-d.fullUpdates:
		if !g.WhichKey().Visible() {
			t.Fatal("layout requested before hint became visible")
		}
		if err := fn(); err != nil {
			t.Fatal(err)
		}
		// Native full updates run the layout after the queued callback.
		if err := g.RunLayout(130, 42); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("hint timer did not request a full layout while idle")
	}
	if buf := d.GetViewBuffer(string(types.WHICH_KEY)); !strings.Contains(buf, "Run statement under cursor") {
		t.Fatalf("hint overlay was not rendered: %q", buf)
	}
}
