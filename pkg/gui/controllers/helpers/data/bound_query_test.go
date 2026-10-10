package data_test

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func boundAwait(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bound navigation did not settle")
	}
}

func TestBoundQueryQueuedCancellationNeverExecutesOrPreempts(t *testing.T) {
	for _, mode := range []string{"rebind", "cancel", "stale", "supersede"} {
		t.Run(mode, func(t *testing.T) {
			innerA, innerB := &recInnerSession{}, &recInnerSession{}
			sessionA, sessionB := session.New(&parkedConn{}, innerA, session.Options{}), session.New(&parkedConn{}, innerB, session.Options{})
			t.Cleanup(func() { _ = sessionA.Close(); _ = sessionB.Close() })
			runner := data.NewQueryRunnerForSession(sessionA, drivers.Capabilities{})
			binding := runner.CaptureQueryBinding()
			entered, gate, workDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			binding.WorkAsync(context.Background(), func() bool { return true }, nil, func(context.Context) error { close(entered); <-gate; return nil }, func(error) { close(workDone) })
			boundAwait(t, entered)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var valid atomic.Bool
			valid.Store(true)
			var preempts atomic.Int32
			ack, ackErr := make(chan struct{}), make(chan error, 1)
			binding.RunAsync(ctx, models.Query{SQL: "must-not-execute"}, valid.Load, func() bool { preempts.Add(1); return false }, func(result *data.BoundQueryResult, err error) { result.Discard(); ackErr <- err; close(ack) })
			switch mode {
			case "rebind":
				runner.Bind(sessionB, drivers.Capabilities{})
			case "cancel":
				cancel()
			case "stale":
				valid.Store(false)
			case "supersede":
				runner.CaptureQueryBinding().WorkAsync(context.Background(), func() bool { return true }, nil, func(context.Context) error { return nil }, func(error) {})
			}
			close(gate)
			boundAwait(t, workDone)
			boundAwait(t, ack)
			if !errors.Is(<-ackErr, context.Canceled) || preempts.Load() != 0 || len(innerA.snapshot()) != 0 || len(innerB.snapshot()) != 0 {
				t.Fatal("stale queued navigation reached a session or captured preemption targets")
			}
			runner.Unbind()
		})
	}
}

func TestBoundQueryUnadoptedOwnership(t *testing.T) {
	for _, mode := range []string{"rebind", "origin-cancel", "reject", "shutdown", "accept"} {
		t.Run(mode, func(t *testing.T) {
			innerA, innerB := &recInnerSession{}, &recInnerSession{}
			innerA.stage(recInnerStream{})
			sessionA, sessionB := session.New(&parkedConn{}, innerA, session.Options{}), session.New(&parkedConn{}, innerB, session.Options{})
			t.Cleanup(func() { _ = sessionA.Close(); _ = sessionB.Close() })
			runner := data.NewQueryRunnerForSession(sessionA, drivers.Capabilities{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ack := make(chan *data.BoundQueryResult, 1)
			runner.CaptureQueryBinding().RunAsync(ctx, models.Query{SQL: "SELECT captured", Args: []any{7}, DefaultSchema: "original", Timeout: time.Second}, func() bool { return true }, func() bool { return false }, func(result *data.BoundQueryResult, err error) {
				if err != nil {
					t.Error(err)
				}
				ack <- result
			})
			var result *data.BoundQueryResult
			select {
			case result = <-ack:
			case <-time.After(3 * time.Second):
				t.Fatal("captured query did not return")
			}
			if result == nil {
				t.Fatal("successful stream lost its ownership envelope")
			}
			var handle *session.RunHandle
			result.Accept(func(rh *session.RunHandle) bool { handle = rh; return false })
			if handle == nil {
				t.Fatal("live ownership envelope refused inspection")
			}
			switch mode {
			case "rebind":
				runner.Bind(sessionB, drivers.Capabilities{})
			case "origin-cancel":
				cancel()
			case "reject":
				if result.Accept(func(*session.RunHandle) bool { return false }) {
					t.Fatal("rejected adoption transferred ownership")
				}
				result.Discard()
			case "shutdown":
				runner.Unbind()
			case "accept":
				if !result.Accept(func(*session.RunHandle) bool { return true }) {
					t.Fatal("valid adoption was rejected")
				}
				runner.Unbind()
				cancel()
				result.Discard()
				select {
				case <-handle.Done():
					t.Fatal("acknowledged stream was still owned by the launcher")
				default:
				}
				_ = handle.Rows().Close()
			}
			boundAwait(t, handle.Done())
			if mode != "accept" && result.Accept(func(*session.RunHandle) bool { t.Error("stale adoption invoked UI"); return true }) {
				t.Fatal("disposed stream was adopted")
			}
			if len(innerB.snapshot()) != 0 {
				t.Fatal("navigation or disposal executed on the replacement session")
			}
			runner.Unbind()
		})
	}
}

func TestBoundMetadataContinuationCannotReplaceNewerUserAction(t *testing.T) {
	inner := &recInnerSession{}
	inner.stage(recInnerStream{})
	sqlSession := session.New(&parkedConn{}, inner, session.Options{})
	t.Cleanup(func() { _ = sqlSession.Close() })
	runner := data.NewQueryRunnerForSession(sqlSession, drivers.Capabilities{})
	old := runner.CaptureQueryBinding()
	prepared := make(chan struct{})
	old.WorkAsync(context.Background(), func() bool { return true }, nil, func(context.Context) error { return nil }, func(err error) {
		if err != nil {
			t.Error(err)
		}
		close(prepared)
	})
	boundAwait(t, prepared)
	ordinary := make(chan struct{})
	runner.RunQueryAsync(context.Background(), models.Query{SQL: "newer-user-query"}, func(handle *session.RunHandle, err error) {
		if err != nil {
			t.Error(err)
		}
		if handle != nil {
			_ = handle.Rows().Close()
		}
		close(ordinary)
	})
	ack := make(chan error, 1)
	old.RunAsync(context.Background(), models.Query{SQL: "stale-fk-select"}, func() bool { return true }, nil, func(result *data.BoundQueryResult, err error) { result.Discard(); ack <- err })
	if !errors.Is(<-ack, context.Canceled) {
		t.Fatal("delayed history acknowledgment admitted a superseded action")
	}
	boundAwait(t, ordinary)
	for _, event := range inner.snapshot() {
		if event == "stream:stale-fk-select:start" || event == "stream:stale-fk-select:unstaged" {
			t.Fatal("stale FK continuation reached the query session")
		}
	}
	runner.Unbind()
}

func TestBoundQueryLiveTimeoutAllowsFreshSession(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires isolated PGSAVVY_TEST_PG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	driver, err := pg.New(nil)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := driver.Open(ctx, models.Connection{Name: "bound-timeout", Driver: "postgres", DSN: dsn}, nil)
	if err != nil {
		t.Fatal("isolated bound-query fixture authentication failed")
	}
	t.Cleanup(func() { _ = conn.Close() })
	inner, err := conn.AcquireSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sqlSession := session.New(conn, inner, session.Options{})
	t.Cleanup(func() { _ = sqlSession.Close() })
	runner := data.NewQueryRunnerForSession(sqlSession, driver.Capabilities())
	t.Cleanup(runner.Unbind)
	ack := make(chan error, 1)
	runner.CaptureQueryBinding().RunAsync(ctx, models.Query{SQL: "SELECT pg_sleep(10)", Timeout: 50 * time.Millisecond}, func() bool { return true }, func() bool { return false }, func(result *data.BoundQueryResult, err error) {
		if err != nil {
			result.Discard()
			ack <- err
			return
		}
		var handle *session.RunHandle
		if !result.Accept(func(rh *session.RunHandle) bool { handle = rh; return true }) {
			result.Discard()
			ack <- errors.New("timeout result could not be adopted")
			return
		}
		_, _, err = handle.Rows().Next(ctx)
		_ = handle.Rows().Close()
		ack <- err
	})
	select {
	case err := <-ack:
		if err == nil {
			t.Fatal("bound navigation ignored its statement timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bound navigation timeout did not settle")
	}
	// pgx may retire the connection on context interruption. A fresh checkout
	// must still work; navigation may not keep its serializer or orphan alive.
	if err := sqlSession.Close(); err != nil {
		t.Fatal(err)
	}
	inner, err = conn.AcquireSession(ctx)
	if err != nil {
		t.Fatal("timed-out navigation stranded the pool checkout")
	}
	fresh := session.New(conn, inner, session.Options{})
	t.Cleanup(func() { _ = fresh.Close() })
	runner.Bind(fresh, driver.Capabilities())
	values := make(chan any, 1)
	runner.CaptureQueryBinding().RunAsync(ctx, models.Query{SQL: "SELECT $1::integer", Args: []any{42}}, func() bool { return true }, func() bool { return false }, func(result *data.BoundQueryResult, err error) {
		if err != nil {
			ack <- err
			return
		}
		var handle *session.RunHandle
		if !result.Accept(func(rh *session.RunHandle) bool { handle = rh; return true }) {
			result.Discard()
			ack <- errors.New("fresh result could not be adopted after timeout")
			return
		}
		row, ok, readErr := handle.Rows().Next(ctx)
		_ = handle.Rows().Close()
		if readErr != nil || !ok {
			ack <- errors.New("fresh checkout lost usable stream after timeout")
			return
		}
		values <- row.Values[0]
	})
	select {
	case value := <-values:
		if value != int32(42) {
			t.Fatal("captured session returned incorrect parameter after timeout")
		}
	case err := <-ack:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal("captured session remained wedged after timeout")
	}
}
