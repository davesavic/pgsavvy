package unit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/davesavic/pgsavvy/pkg/tasks"
)

func TestPostgresStreamStopReleasesSession(t *testing.T) {
	addr := startStalledPostgresPeer(t)
	driver, err := pg.New(nil)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	setup, cancelSetup := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelSetup()
	conn, err := driver.Open(setup, models.Connection{
		DSN:      fmt.Sprintf("postgres://fixture@%s/fixture?sslmode=disable&default_query_exec_mode=simple_protocol", addr),
		Password: "synthetic-fixture-only",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	inner, err := conn.AcquireSession(setup)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.New(conn, inner, session.Options{})
	t.Cleanup(func() { _ = sess.Close() })
	queryCtx, rescue := context.WithCancel(context.Background())
	run, err := sess.Stream(queryCtx, models.Query{SQL: "SELECT stalled"})
	if err != nil {
		rescue()
		t.Fatal(err)
	}
	entered := make(chan struct{})
	stream := &observedRowStream{RowStream: run.Rows(), entered: entered}
	workerDone := make(chan struct{})
	manager := tasks.New(func(fn func(gocui.Task) error) {
		go func() { defer close(workerDone); _ = fn(nil) }()
	}, func(fn func() error) { _ = fn() })
	var completions atomic.Int32
	t.Cleanup(func() {
		rescue()
		manager.Stop()
		_ = run.Rows().Close()
	})
	err = manager.NewQueryTask("stalled", func(context.Context) (drivers.RowStream, error) {
		return stream, nil // Same pre-opened stream handoff as the result tab.
	}, func([]models.Row) { t.Error("stalled query unexpectedly delivered a row") }, 1, func(error) { completions.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	awaitStreamStopSignal(t, entered, "Next entry")
	stopped := make(chan struct{})
	go func() { manager.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("Stop blocked draining a query whose consumer was cancelled")
		rescue()
		awaitStreamStopSignal(t, stopped, "rescued Stop")
	}
	awaitStreamStopSignal(t, workerDone, "worker exit")
	awaitStreamStopSignal(t, run.Done(), "RunHandle completion")
	if !errors.Is(run.Err(), context.Canceled) {
		t.Errorf("run error=%v, want context.Canceled", run.Err())
	}
	manager.Stop()
	if err := run.Rows().Close(); err != nil {
		t.Fatal(err)
	}
	if completions.Load() != 1 {
		t.Errorf("onDone called %d times, want once", completions.Load())
	}
	following := make(chan struct{})
	go func() {
		// Cancellation can poison the backend; acquiring/releasing the session
		// guard is required, not successful execution on that connection.
		_, _ = sess.Execute(context.Background(), models.Query{SQL: "SELECT following"})
		close(following)
	}()
	awaitStreamStopSignal(t, following, "following query acquiring the session")
}

type observedRowStream struct {
	drivers.RowStream
	entered chan struct{}
	once    sync.Once
}

func (s *observedRowStream) Next(ctx context.Context) (models.Row, bool, error) {
	s.once.Do(func() { close(s.entered) })
	return s.RowStream.Next(ctx)
}

func awaitStreamStopSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A loopback-only protocol fixture exercises the public pg driver without an
// installed PostgreSQL server. Only the SELECT stalled response is unfinished.
func startStalledPostgresPeer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			workers.Go(func() {
				defer func() { _ = conn.Close() }()
				serveStalledPostgresPeer(conn)
			})
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		awaitStreamStopSignal(t, acceptDone, "fixture accept loop")
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return listener.Addr().String()
}

func serveStalledPostgresPeer(conn net.Conn) {
	backend := pgproto3.NewBackend(conn, conn)
	startup, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := startup.(*pgproto3.StartupMessage); !ok {
		return // CancelRequest uses a separate connection; EOF acknowledges it.
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 123, SecretKey: []byte{0, 0, 0, 1}})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}
		query, ok := msg.(*pgproto3.Query)
		if !ok {
			return
		}
		switch query.String {
		case "SELECT stalled", "SELECT version()":
			backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 25, DataTypeSize: -1, TypeModifier: -1}}})
			if query.String == "SELECT stalled" {
				if backend.Flush() != nil {
					return
				}
				continue
			}
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("PostgreSQL 17.0 synthetic")}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		default:
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
		}
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if backend.Flush() != nil {
			return
		}
	}
}
