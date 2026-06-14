package orchestrator_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/dbsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/query"
)

// queueingDriver wraps a RecorderGuiDriver but, unlike the recorder's
// inline Update, it ENQUEUES every OnUIThread closure onto a channel so a
// dedicated "UI" goroutine can drain them — mirroring the real gocui
// MainLoop where Update closures run on a single thread, distinct from the
// worker goroutine that schedules them.
//
// This is the seam that makes the concurrency regression
// observable under -race: the recorder runs Update inline on the worker
// goroutine, so its publish writes never actually cross a thread boundary
// and the detector sees no conflict. With this driver, the worker truly
// runs on its own goroutine and the publish closures land on the drain
// goroutine, so a connect that writes shared GUI state (activeSQLSession /
// Schemas.SetItems / QueryEditor.SetBuffer) DIRECTLY on the worker (the
// pre-fix code path) races a concurrent UI-thread read.
type queueingDriver struct {
	*testfake.RecorderGuiDriver
	updates chan func() error
}

func newQueueingDriver() *queueingDriver {
	return &queueingDriver{
		RecorderGuiDriver: testfake.NewRecorderGuiDriver(),
		// Generously buffered so Update never blocks the worker goroutine
		// (production Update is non-blocking — it enqueues and returns).
		updates: make(chan func() error, 256),
	}
}

func (d *queueingDriver) Update(fn func() error) {
	if fn == nil {
		return
	}
	d.updates <- fn
}

func (d *queueingDriver) UpdateContentOnly(fn func() error) {
	if fn == nil {
		return
	}
	d.updates <- fn
}

// drainPending runs every queued closure currently in the channel on the
// CALLING goroutine (the test's "UI thread"). Non-blocking once the queue
// is empty.
func (d *queueingDriver) drainPending() {
	for {
		select {
		case fn := <-d.updates:
			_ = fn()
		default:
			return
		}
	}
}

// TestConnect_AsyncPublish_NoRaceWithLayoutRead drives a Connect through
// the REAL async worker path (g.OnWorker spawns a goroutine) while a
// dedicated UI goroutine concurrently drains the publish queue AND reads
// the shared GUI state the MainLoop reads every render frame
// (activeSQLSession, Schemas.Items()). Run under `go test -race`:
//
//   - BEFORE the fix this fails: the connect worker wrote activeSQLSession
//     (wireQueryRuntime), Schemas.SetItems (populateSchemasRail) and
//     QueryEditor.SetBuffer (hydrateQueryEditorBuffer) directly on the
//     worker goroutine, racing this goroutine's reads.
//   - AFTER the fix it is clean: every such write is marshalled into the
//     single OnUIThread publish closure, which this goroutine runs via
//     drainPending — so writes and reads share one goroutine.
//
// The openHook blocks the dial until this goroutine has begun its
// read/drain loop, guaranteeing the worker is provably in flight while the
// reads happen.
func TestConnect_AsyncPublish_NoRaceWithLayoutRead(t *testing.T) {
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	drv := newQueueingDriver()
	if err := g.UseDriverForTest(drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	// wireWithDriver may have queued startup Update closures; drain them on
	// this goroutine before the worker starts so the only cross-goroutine
	// traffic in the measured window is the connect publish.
	drv.drainPending()
	t.Cleanup(func() {
		g.WaitForWorkersForTest()
		drv.drainPending()
		_ = g.Close()
	})

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	conn.schemas = []models.Schema{
		{Name: "app", Owner: "u"},
		{Name: "pg_catalog", Owner: "u"},
	}

	// openHook blocks the dial on the worker goroutine until this (UI)
	// goroutine releases it, guaranteeing the worker is provably in flight
	// — and that its post-dial writes (pre-fix: direct on the worker;
	// post-fix: queued publish closures) overlap the read/drain loop below.
	releaseDial := make(chan struct{})
	conn.openHook = func() {
		<-releaseDial
	}

	profile := &models.Connection{Name: "async-race", Driver: driverName, DSN: "postgres://stub"}
	bag := g.HelperBagForTest()

	// Schedule Connect on the REAL worker pool (spawns a goroutine), exactly
	// like connections_controller.go does in production.
	g.OnWorker(func(_ gocui.Task) error {
		return bag.Connect.Connect(context.Background(), profile)
	})

	// Release the dial, then immediately enter the concurrent read/drain
	// loop on this (UI) goroutine.
	close(releaseDial)

	// Spin draining publish closures and reading shared state until the
	// worker has finished. Under -race a direct worker write to any of
	// these fields collides with these reads.
	deadline := time.Now().Add(5 * time.Second)
	for {
		drv.drainPending()
		_ = g.ActiveSQLSessionForTest()
		if sch := g.Registry().Schemas; sch != nil {
			_ = sch.Items()
		}
		if g.BusyCount() == 0 {
			// Worker done; drain any final publish closure(s) and read once
			// more so the published state is observed on this goroutine.
			drv.drainPending()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect worker did not complete within 5s")
		}
	}

	// Behavioural sanity: after the publish closure ran on this goroutine,
	// the active session is set and the schemas rail is populated.
	if g.ActiveSQLSessionForTest() == nil {
		t.Fatal("ActiveSQLSessionForTest() = nil after async Connect; publish phase did not run")
	}
	if sch := g.Registry().Schemas; sch == nil || len(sch.Items()) == 0 {
		t.Fatal("SchemasContext.Items() empty after async Connect; publish phase did not populate the rail")
	}
}

// TestConnect_AsyncErrorPublish_NoRaceWithConnectingRender drives a FAILING
// Connect through the real async worker path while a dedicated UI goroutine
// concurrently drains the publish queue AND renders the CONNECTING screen
// (the MainLoop reads ConnectingContext state every render frame). Run under
// `go test -race`:
//
//   - The connect error-publish (connectInvoker.routeConnectError) marshals
//     CONNECTING.SetError into an OnUIThread closure, which this goroutine
//     runs via drainPending — so the SetError write and the HandleRender read
//     share one goroutine and never race.
//   - A direct worker SetError (the bug this guards against) would collide
//     with the HandleRender read here under -race.
//
// openHook blocks the dial until this goroutine has begun its read/drain
// loop, guaranteeing the worker's error-publish overlaps the render reads.
func TestConnect_AsyncErrorPublish_NoRaceWithConnectingRender(t *testing.T) {
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/tmp/state.yml", common.DefaultClock())

	tmp := t.TempDir()
	g := orchestrator.NewGui(orchestrator.Deps{
		Common:              c,
		Store:               store,
		ConnectionsPath:     filepath.Join(tmp, "connections.yml"),
		ConnectionsProvider: func() []models.Connection { return nil },
		DriverNamesFn:       func() []string { return []string{"postgres"} },
		HistoryProvider: func() (*query.History, error) {
			return query.New(filepath.Join(tmp, "history.sqlite"))
		},
	})
	drv := newQueueingDriver()
	if err := g.UseDriverForTest(drv); err != nil {
		t.Fatalf("UseDriverForTest: %v", err)
	}
	drv.drainPending()
	t.Cleanup(func() {
		g.WaitForWorkersForTest()
		drv.drainPending()
		_ = g.Close()
	})

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)
	releaseDial := make(chan struct{})
	conn.openHook = func() { <-releaseDial }
	conn.openErr = errors.New("dial failed: connection refused")

	// Register the CONNECTION_MANAGER view so SetContent/GetViewBuffer capture
	// the body (the real layout pass does this via SetView).
	_, _ = drv.SetView(string(types.CONNECTION_MANAGER), 0, 0, 40, 10, 0)

	// CONNECTION_MANAGER is already the startup root. Set its connecting
	// state for the async error-publish path.
	bag := g.HelperBagForTest()
	cm := g.Registry().ConnectionManager
	cm.ConnectingState().SetConnectingStaged("async-err", nil)
	drv.drainPending()

	profile := &models.Connection{Name: "async-err", Driver: driverName, DSN: "postgres://stub"}
	g.OnWorker(func(_ gocui.Task) error {
		_ = bag.Connect.Connect(context.Background(), profile)
		return nil
	})

	close(releaseDial)

	deadline := time.Now().Add(5 * time.Second)
	for {
		drv.drainPending()
		if cm != nil {
			_ = cm.HandleRender()
		}
		if g.BusyCount() == 0 {
			drv.drainPending()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connect worker did not complete within 5s")
		}
	}

	// Behavioural sanity: after the error-publish closure ran on this
	// goroutine (SetError), render the screen — HandleRender enqueues its
	// SetContent via the queueing driver, so drain once more to flush it.
	if err := cm.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	drv.drainPending()
	body := drv.GetViewBuffer(string(types.CONNECTION_MANAGER))
	if body == "" {
		t.Fatal("CONNECTION_MANAGER body empty after async error publish")
	}
}
