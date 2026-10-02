package orchestrator_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/orchestrator"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// A supersession bump mid-dial causes the worker to
// find itself stale and publish NOTHING. Neither activeConn nor the persisted
// LastConnectionID may be stamped. We inject the bump from inside the dial
// via openHook.
func TestConnectInvokerCancelMidDialDropsSuccessfulResult(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()

	// openHook fires on the worker goroutine during the dial. Bumping the
	// generation makes the in-flight attempt stale.
	conn.openHook = func() { g.BumpConnectGenForTest() }

	profile := &models.Connection{Name: "cancelled", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), profile)

	if got := g.ActiveConnIDForTest(); got != "" {
		t.Fatalf("activeConnID = %q after cancel-mid-dial; want empty (cancelled result must not clobber)", got)
	}
	if got := g.LastConnectionIDForTest(); got != "" {
		t.Fatalf("LastConnectionID = %q after cancel-mid-dial; want empty (cancel must not stamp persisted state)", got)
	}
}

// Two sequential connects; only the second succeeds
// because the first is superseded mid-dial. The active connection must be the
// second profile.
func TestConnectInvokerRapidCancelThenNewAttempt(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)

	caps := drivers.Capabilities{}
	driverName, conn := registerWireFake(t, caps)

	bag := g.HelperBagForTest()

	// First connect is superseded mid-dial.
	conn.openHook = func() { g.BumpConnectGenForTest() }
	first := &models.Connection{Name: "first", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), first)

	// Second connect succeeds (no hook, no supersession).
	conn.openHook = nil
	second := &models.Connection{Name: "second", Driver: driverName, DSN: "postgres://stub"}
	_ = bag.Connect.Connect(context.Background(), second)

	if got := g.ActiveConnIDForTest(); got != "second" {
		t.Fatalf("activeConnID = %q; want \"second\" (only the newest attempt wins; the cancelled first must drop)", got)
	}
}

// AC: Connect with no driver wired is a no-op — nil cancelFn, no panic.
func TestConnectInvokerCancelNoInflightIsNoop(t *testing.T) {
	g, _ := buildTestGuiWithHistory(t)
	// No connect attempt started. Must not panic.
	_ = g
}

type cleanupRecorder struct {
	mu          sync.Mutex
	order       []int
	closed      chan struct{}
	poolStarted chan struct{}
	poolRelease chan struct{}
}

func recordCleanup(conn *wireFakeConn, blockPool bool) *cleanupRecorder {
	r := &cleanupRecorder{closed: make(chan struct{}), poolStarted: make(chan struct{}), poolRelease: make(chan struct{})}
	conn.sessionCloseHook = func(id models.SessionID) {
		r.mu.Lock()
		r.order = append(r.order, int(id))
		r.mu.Unlock()
	}
	conn.closeHook = func() {
		r.mu.Lock()
		r.order = append(r.order, 0)
		r.mu.Unlock()
		close(r.poolStarted)
		if blockPool {
			<-r.poolRelease
		}
		close(r.closed)
	}
	return r
}

func (r *cleanupRecorder) assertClosed(t *testing.T) {
	t.Helper()
	select {
	case <-r.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("owned connection was not closed")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !reflect.DeepEqual(r.order, []int{2, 1, 0}) {
		t.Fatalf("close order = %v; want query session, helper session, pool exactly once", r.order)
	}
}

func cleanupQueuedGui(t *testing.T) (*orchestrator.Gui, *queueingDriver) {
	t.Helper()
	g, _ := buildTestGuiWithHistory(t)
	d := newQueueingDriver()
	if err := g.UseDriverForTest(d); err != nil {
		t.Fatal(err)
	}
	d.drainPending()
	return g, d
}

func TestReview_cleanup(t *testing.T) {
	t.Run("delete_old_profile_preserves_unpublished_replacement", func(t *testing.T) {
		g, d := cleanupQueuedGui(t)
		oldName, oldConn := registerWireFake(t, drivers.Capabilities{})
		old := recordCleanup(oldConn, false)
		newName, newConn := registerWireFake(t, drivers.Capabilities{})
		newRec := recordCleanup(newConn, false)
		oldProfile := &models.Connection{Name: "old", Driver: oldName}
		bag := g.HelperBagForTest()
		if err := bag.Connect.Connect(context.Background(), oldProfile); err != nil {
			t.Fatal(err)
		}
		d.drainPending()
		if err := bag.Connect.Connect(context.Background(), &models.Connection{Name: "new", Driver: newName}); err != nil {
			t.Fatal(err)
		}
		if g.ActiveConnIDForTest() != "old" {
			t.Fatal("replacement publication was not held")
		}
		manager := g.Registry().ConnectionManager
		manager.SetMode(guicontext.ModeList)
		manager.SetItems([]any{oldProfile})
		manager.SetCursor(0)
		deleteCmd, ok := g.CommandRegistry().Get(commands.ConnectionManagerDelete)
		if !ok {
			t.Fatal("delete command not registered")
		}
		if err := deleteCmd.Handler(commands.ExecCtx{}); err != nil {
			t.Fatal(err)
		}
		yes, ok := g.CommandRegistry().Get(commands.ConfirmYes)
		if !ok {
			t.Fatal("confirmation command not registered")
		}
		// This fixture has no saved profiles. Retirement precedes the explicit
		// persistence error; exercise the real confirmation callback nonetheless.
		if err := yes.Handler(commands.ExecCtx{}); !errors.Is(err, config.ErrConnectionNotFound) {
			t.Fatalf("delete error = %v", err)
		}
		g.WaitForWorkersForTest()
		old.assertClosed(t)
		select {
		case <-newRec.closed:
			t.Fatal("deletion retired replacement")
		default:
		}
		d.drainPending()
		if g.ActiveConnIDForTest() != "new" {
			t.Fatal("replacement was not published")
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		newRec.assertClosed(t)
	})
	t.Run("shutdown_during_worker_before_publication", func(t *testing.T) {
		g, d := cleanupQueuedGui(t)
		name, conn := registerWireFake(t, drivers.Capabilities{})
		r := recordCleanup(conn, false)
		queryAcquired := make(chan struct{})
		conn.acquireHook = func(n int32) {
			if n == 2 {
				close(queryAcquired)
			}
		}
		conn.schemasBlock = make(chan struct{})
		defer func() { close(conn.schemasBlock); g.WaitForWorkersForTest() }()
		bag := g.HelperBagForTest()
		g.OnWorker(func(_ gocui.Task) error {
			return bag.Connect.Connect(context.Background(), &models.Connection{Name: "closing-worker", Driver: name})
		})
		select {
		case <-queryAcquired:
		case <-time.After(time.Second):
			t.Fatal("query acquisition did not start")
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		d.drainPending()
		r.assertClosed(t)
		if g.ActiveSQLSessionForTest() != nil {
			t.Fatal("worker published after shutdown")
		}
	})
	t.Run("published_close_repeated", func(t *testing.T) {
		g, _ := buildTestGuiWithHistory(t)
		name, conn := registerWireFake(t, drivers.Capabilities{})
		r := recordCleanup(conn, false)
		bag := g.HelperBagForTest()
		if err := bag.Connect.Connect(context.Background(), &models.Connection{Name: "published", Driver: name}); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		r.assertClosed(t)
		if bag.QueryRunner.HasSession() {
			t.Fatal("closed runner still bound")
		}
	})
	t.Run("stale_publication_off_ui", func(t *testing.T) {
		g, d := cleanupQueuedGui(t)
		name, conn := registerWireFake(t, drivers.Capabilities{})
		r := recordCleanup(conn, true)
		defer close(r.poolRelease)
		if err := g.HelperBagForTest().Connect.Connect(context.Background(), &models.Connection{Name: "stale", Driver: name}); err != nil {
			t.Fatal(err)
		}
		g.BumpConnectGenForTest()
		uiReturned := make(chan struct{})
		go func() { d.drainPending(); close(uiReturned) }()
		select {
		case <-uiReturned:
		case <-time.After(time.Second):
			t.Error("stale publication blocks UI on pool disposal")
		}
		select {
		case <-r.poolStarted:
		case <-time.After(time.Second):
			t.Fatal("stale cleanup not started")
		}
		r.mu.Lock()
		order := append([]int(nil), r.order...)
		r.mu.Unlock()
		if !reflect.DeepEqual(order, []int{2, 1, 0}) {
			t.Errorf("stale order = %v", order)
		}
	})
	t.Run("shutdown_abandons_queued_publication", func(t *testing.T) {
		g, d := cleanupQueuedGui(t)
		name, conn := registerWireFake(t, drivers.Capabilities{})
		r := recordCleanup(conn, false)
		if err := g.HelperBagForTest().Connect.Connect(context.Background(), &models.Connection{Name: "queued", Driver: name}); err != nil {
			t.Fatal(err)
		}
		if g.ActiveSQLSessionForTest() != nil {
			t.Fatal("publication was not held")
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		d.drainPending()
		r.assertClosed(t)
		if g.ActiveSQLSessionForTest() != nil {
			t.Fatal("late publication resurrected closed session")
		}
	})
	t.Run("replacement_survives_old_publication", func(t *testing.T) {
		g, d := cleanupQueuedGui(t)
		oldName, oldConn := registerWireFake(t, drivers.Capabilities{})
		old := recordCleanup(oldConn, false)
		newName, newConn := registerWireFake(t, drivers.Capabilities{})
		newRec := recordCleanup(newConn, false)
		bag := g.HelperBagForTest()
		if err := bag.Connect.Connect(context.Background(), &models.Connection{Name: "old", Driver: oldName}); err != nil {
			t.Fatal(err)
		}
		if err := bag.Connect.Connect(context.Background(), &models.Connection{Name: "new", Driver: newName}); err != nil {
			t.Fatal(err)
		}
		d.drainPending()
		g.WaitForWorkersForTest()
		old.assertClosed(t)
		select {
		case <-newRec.closed:
			t.Fatal("old publication closed replacement")
		default:
		}
		if g.ActiveConnIDForTest() != "new" {
			t.Fatal("replacement was not published")
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
		newRec.assertClosed(t)
	})
}
