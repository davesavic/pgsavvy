//go:build linux

package pg

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/davesavic/pgsavvy/pkg/session/sshtunnel"
)

type MachineClient struct {
	conn            *pgx.Conn
	tunnel          *sshtunnel.MachineTunnel
	access          models.CLIAccess
	statementMS     int
	cleanup         time.Duration
	operation       sync.Mutex
	transport       sync.Mutex
	raw             net.Conn
	aborted         bool
	dialAllowed     bool
	closeOnce       sync.Once
	closeErr        error
	stop            func()
	cleanupOnce     sync.Once
	cleanupCtx      context.Context
	cleanupCancel   context.CancelFunc
	attempts        []*pgconn.PgConn
	dialStops       []func()
	currentDialStop func()
	restartTunnel   bool
}

type (
	machineDialKey        struct{}
	machineContextWatcher struct {
		client *MachineClient
		raw    net.Conn
		tunnel *sshtunnel.MachineTunnel
	}
)

func (w machineContextWatcher) HandleCancel(ctx context.Context) {
	if ctx.Value(machineDialKey{}) == w.client {
		w.client.abortAttempt(w.raw, w.tunnel)
		return
	}
	w.client.Abort()
}
func (machineContextWatcher) HandleUnwatchAfterCancel() {}

func machineFailure(ctx context.Context, err error, code string) *MachineError {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &MachineError{Code: "TIMEOUT"}
	}
	var own *MachineError
	if errors.As(err, &own) {
		return own
	}
	var credential *session.MachineCredentialError
	if errors.As(err, &credential) {
		return &MachineError{Code: string(credential.Kind)}
	}
	var tunnel *sshtunnel.MachineError
	if errors.As(err, &tunnel) {
		return &MachineError{Code: tunnel.Code}
	}
	var server *pgconn.PgError
	if errors.As(err, &server) {
		if server.Code == "57014" || server.Code == "55P03" {
			code = "TIMEOUT"
		}
		return &MachineError{Code: code, SQLState: server.Code}
	}
	return &MachineError{Code: code}
}

func watchMachine(ctx context.Context, abort func()) func() {
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			abort()
		case <-stop:
		}
	}()
	return sync.OnceFunc(func() { close(stop); <-joined })
}

func OpenMachine(ctx context.Context, profile models.Connection, options MachineOptions) (*MachineClient, error) {
	access := profile.EffectiveCLIAccess()
	if access == models.CLIAccessDisabled || (options.RequiredAccess == models.CLIAccessRowRead && access != models.CLIAccessRowRead) {
		return nil, &MachineError{Code: "POLICY_DENIED"}
	}
	if options.RequiredAccess != "" && options.RequiredAccess != models.CLIAccessMetadata && options.RequiredAccess != models.CLIAccessRowRead {
		return nil, &MachineError{Code: "POLICY_DENIED"}
	}
	if ctx.Err() != nil {
		return nil, &MachineError{Code: "TIMEOUT"}
	}
	if profile.Driver != "postgres" {
		return nil, &MachineError{Code: "CONNECTION_UNAVAILABLE"}
	}
	if profile.SSHTunnel != nil {
		copy := *profile.SSHTunnel
		profile.SSHTunnel = &copy
	}
	if options.ConnectTimeout == 0 {
		options.ConnectTimeout = 10 * time.Second
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = 2 * time.Second
	}
	if options.ConnectTimeout <= 0 || options.ConnectTimeout > 10*time.Second || options.CleanupTimeout <= 0 || options.CleanupTimeout > 2*time.Second {
		return nil, &MachineError{Code: "INVALID_INPUT"}
	}
	connectCtx, cancel := context.WithTimeout(ctx, options.ConnectTimeout)
	defer cancel()
	parse := options.ParseConfig
	if parse == nil {
		parse = session.BuildMachineConfig
	}
	cfg, err := parse(connectCtx, profile)
	if err != nil {
		return nil, machineFailure(connectCtx, err, "CONNECTION_UNAVAILABLE")
	}
	if cfg == nil {
		return nil, &MachineError{Code: "CONNECTION_UNAVAILABLE"}
	}
	cfg = cfg.Copy()
	password, err := session.ResolveMachinePassword(connectCtx, profile, &cfg.Config, options.Credentials)
	if err != nil {
		return nil, machineFailure(connectCtx, err, "CREDENTIALS_UNAVAILABLE")
	}
	cfg.Password = password
	client := &MachineClient{access: access, statementMS: session.MachineStatementMilliseconds(profile.StatementTimeout), cleanup: options.CleanupTimeout, dialAllowed: true}
	client.stop = watchMachine(ctx, client.Abort)
	failed := true
	defer func() {
		if failed {
			client.Abort()
			client.stop()
		}
	}()
	dial := cfg.DialFunc
	if options.Dial != nil {
		dial = options.Dial
	}
	if profile.SSHTunnel != nil {
		execute := options.Credentials.Exec
		if execute == nil {
			execute = func(ctx context.Context, command string) (string, error) {
				password, err := session.ExecMachineCredential(ctx, command, session.MachineCredentialLimits{})
				var safe *session.MachineCredentialError
				if errors.As(err, &safe) && safe.Kind == session.MachineCredentialTimeout {
					return "", context.DeadlineExceeded
				}
				return password, err
			}
		}
		openTunnel := func(dialCtx context.Context) (*sshtunnel.MachineTunnel, error) {
			deadline, _ := connectCtx.Deadline()
			remaining := time.Until(deadline)
			if perHost, ok := dialCtx.Deadline(); ok {
				remaining = min(remaining, time.Until(perHost))
			}
			if remaining <= 0 {
				return nil, &MachineError{Code: "TIMEOUT"}
			}
			return sshtunnel.OpenMachine(ctx, *profile.SSHTunnel, sshtunnel.MachineOptions{ExecCredential: execute, ConnectTimeout: remaining})
		}
		tunnel, err := openTunnel(connectCtx)
		if err != nil {
			return nil, machineFailure(connectCtx, err, "CONNECTION_UNAVAILABLE")
		}
		client.transport.Lock()
		client.tunnel = tunnel
		aborted := client.aborted
		client.transport.Unlock()
		if aborted {
			tunnel.Abort()
			return nil, &MachineError{Code: "TIMEOUT"}
		}
		dial = func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			client.transport.Lock()
			restart := client.restartTunnel
			client.transport.Unlock()
			if restart {
				_ = tunnel.Close()
				fresh, err := openTunnel(dialCtx)
				if err != nil {
					return nil, err
				}
				client.transport.Lock()
				aborted := client.aborted
				if !aborted {
					client.tunnel = fresh
					client.restartTunnel = false
				}
				client.transport.Unlock()
				if aborted {
					fresh.Abort()
					return nil, net.ErrClosed
				}
				tunnel = fresh
			}
			raw, err := tunnel.DialContext(dialCtx, network, addr)
			// Joining a timed-out channel-open aborts its SSH transport. Only a
			// subsequent configured endpoint may replace it; no query is retried.
			if err != nil && dialCtx.Err() != nil {
				client.transport.Lock()
				client.restartTunnel = true
				client.transport.Unlock()
			}
			return raw, err
		}
		cfg.LookupFunc = tunnel.LookupHost
	}
	cfg.OnNotice = nil
	cfg.OnNotification = nil
	cfg.Tracer = nil
	cfg.BuildContextWatcherHandler = func(conn *pgconn.PgConn) ctxwatch.Handler {
		client.transport.Lock()
		raw, stop, tunnel := client.raw, client.currentDialStop, client.tunnel
		client.attempts = append(client.attempts, conn)
		client.transport.Unlock()
		if stop != nil {
			stop()
		}
		return machineContextWatcher{client: client, raw: raw, tunnel: tunnel}
	}
	// pgx asynchronous error cleanup attempts CancelRequest with a fresh context.
	// Only startup dials carry this private token; no auxiliary PID dial is allowed.
	connectCtx = context.WithValue(connectCtx, machineDialKey{}, client)
	cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		client.transport.Lock()
		allowed := client.dialAllowed && !client.aborted && ctx.Value(machineDialKey{}) == client
		client.transport.Unlock()
		if !allowed {
			return nil, net.ErrClosed
		}
		client.transport.Lock()
		previousStop := client.currentDialStop
		client.currentDialStop = nil
		client.transport.Unlock()
		// A rejected TLS candidate can share its timeout context with the next
		// plaintext candidate. Its watcher must not later abort the replacement.
		if previousStop != nil {
			previousStop()
		}
		raw, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		client.transport.Lock()
		if client.aborted {
			client.transport.Unlock()
			_ = raw.Close()
			return nil, net.ErrClosed
		}
		client.raw = raw
		tunnel := client.tunnel
		stop := watchMachine(ctx, func() { client.abortAttempt(raw, tunnel) })
		client.currentDialStop = stop
		client.dialStops = append(client.dialStops, stop)
		client.transport.Unlock()
		return raw, nil
	}
	stopConnect := watchMachine(connectCtx, client.Abort)
	client.conn, err = pgx.ConnectConfig(connectCtx, cfg)
	client.transport.Lock()
	client.dialAllowed = false
	stops := append([]func(){}, client.dialStops...)
	client.transport.Unlock()
	for _, stop := range stops {
		stop()
	}
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), client.cleanup)
	var keep *pgconn.PgConn
	if client.conn != nil {
		keep = client.conn.PgConn()
	}
	client.joinFailedAttempts(cleanup, keep)
	cancelCleanup()
	if err == nil {
		err = client.initialize(connectCtx, profile)
	}
	stopConnect()
	if err != nil {
		client.Abort()
		if client.conn != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), client.cleanup)
			_ = client.Close(cleanup)
			cancel()
		}
		return nil, machineFailure(connectCtx, err, "CONNECTION_UNAVAILABLE")
	}
	failed = false
	return client, nil
}

func (c *MachineClient) joinFailedAttempts(ctx context.Context, keep *pgconn.PgConn) {
	for _, attempt := range c.attempts {
		if attempt == keep {
			continue
		}
		_ = attempt.Close(ctx)
		<-attempt.CleanupDone()
	}
}

func (c *MachineClient) initialize(ctx context.Context, profile models.Connection) error {
	if profile.Role != "" {
		if err := c.exec(ctx, "SET ROLE "+pgx.Identifier{profile.Role}.Sanitize()); err != nil {
			return err
		}
	}
	for _, sql := range []string{"SET default_transaction_read_only=on", "SET client_encoding='UTF8'", "SET DateStyle='ISO'", "SET TimeZone='UTC'", "SET IntervalStyle='postgres'", "SET bytea_output='hex'", "SET extra_float_digits=3", "SET statement_timeout=" + strconv.Itoa(c.statementMS), "SET lock_timeout=1000"} {
		if err := c.exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}

func (c *MachineClient) exec(ctx context.Context, sql string) error {
	_, err := c.conn.PgConn().ExecParams(ctx, sql, nil, nil, nil, nil).Close()
	return err
}

func (c *MachineClient) Abort() {
	c.transport.Lock()
	c.aborted = true
	c.dialAllowed = false
	raw, tunnel := c.raw, c.tunnel
	c.transport.Unlock()
	if tunnel != nil {
		tunnel.Abort()
	}
	if raw != nil {
		_ = raw.Close()
	}
}

func (c *MachineClient) abortAttempt(raw net.Conn, tunnel *sshtunnel.MachineTunnel) {
	if tunnel != nil {
		c.transport.Lock()
		if c.tunnel == tunnel {
			c.restartTunnel = true
		}
		c.transport.Unlock()
		// Abort TCP before closing a channel, whose Close may itself write.
		tunnel.Abort()
	}
	_ = raw.Close()
}

func (c *MachineClient) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		stopParent := watchMachine(ctx, c.Abort)
		defer stopParent()
		cleanup := c.freshCleanup()
		defer c.cleanupCancel()
		if deadline, ok := ctx.Deadline(); ok {
			var cancel context.CancelFunc
			cleanup, cancel = context.WithDeadline(cleanup, deadline)
			defer cancel()
		}
		stop := watchMachine(cleanup, c.Abort)
		if c.conn != nil {
			if err := c.conn.Close(cleanup); err != nil {
				c.closeErr = machineFailure(cleanup, err, "RESOURCE_FAILURE")
			}
		}
		c.Abort()
		stop()
		if c.conn != nil {
			<-c.conn.PgConn().CleanupDone()
		}
		c.stop()
		if cleanup.Err() != nil || ctx.Err() != nil {
			c.closeErr = &MachineError{Code: "RESOURCE_FAILURE"}
		}
	})
	return c.closeErr
}

func (c *MachineClient) freshCleanup() context.Context {
	c.cleanupOnce.Do(func() { c.cleanupCtx, c.cleanupCancel = context.WithTimeout(context.Background(), c.cleanup) })
	return c.cleanupCtx
}

// withReadOnly owns setup, terminal completion and rollback on this connection.
func (c *MachineClient) withReadOnly(ctx context.Context, repeatable bool, schema string, operation func(context.Context) error) (err error) {
	c.operation.Lock()
	defer c.operation.Unlock()
	begin := "BEGIN READ ONLY"
	if repeatable {
		begin = "BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"
	}
	if err = c.exec(ctx, begin); err != nil {
		return machineFailure(ctx, err, "DATABASE_FAILURE")
	}
	defer func() {
		cleanup := c.freshCleanup()
		rollbackErr := c.exec(cleanup, "ROLLBACK")
		if rollbackErr != nil {
			c.Abort()
			if err == nil {
				err = machineFailure(cleanup, rollbackErr, "DATABASE_FAILURE")
			}
		}
	}()
	for _, sql := range []string{"SET LOCAL statement_timeout=" + strconv.Itoa(c.statementMS), "SET LOCAL lock_timeout=1000"} {
		if err = c.exec(ctx, sql); err != nil {
			return machineFailure(ctx, err, "DATABASE_FAILURE")
		}
	}
	if schema != "" {
		if err = c.exec(ctx, "SET LOCAL search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
			return machineFailure(ctx, err, "DATABASE_FAILURE")
		}
	}
	if err = operation(ctx); err != nil {
		return machineFailure(ctx, err, "DATABASE_FAILURE")
	}
	return nil
}

func (c *MachineClient) Ping(ctx context.Context) error {
	return c.withReadOnly(ctx, false, "", func(ctx context.Context) error { return c.exec(ctx, "SELECT 1") })
}
