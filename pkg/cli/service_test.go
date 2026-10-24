//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func TestMachineFullAccessPolicy_Service(t *testing.T) {
	requirements := []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, "full", "invalid", "FULL"}
	for _, tc := range []struct {
		access  models.CLIAccess
		allowed [7]bool
	}{
		{"", [7]bool{}},
		{models.CLIAccessDisabled, [7]bool{}},
		{models.CLIAccessMetadata, [7]bool{true, false, true}},
		{models.CLIAccessRowRead, [7]bool{true, false, true, true}},
		{"full", [7]bool{true, false, true, true, true}},
		{"invalid", [7]bool{}},
		{"FULL", [7]bool{}},
	} {
		for index, required := range requirements {
			for flags := range 8 {
				t.Run(fmt.Sprintf("%s/%s/flags%d", tc.access, required, flags), func(t *testing.T) {
					profile := models.Connection{Name: "exact", Driver: "postgres", CLIAccess: tc.access, ReadOnly: flags&1 != 0, ConfirmWrites: flags&2 != 0, ConfirmDDL: flags&4 != 0, PasswordCommand: "SECRET helper"}
					parses, helpers, dials := 0, 0, 0
					service := InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }, Options: pg.MachineOptions{
						ParseConfig: func(_ context.Context, got models.Connection) (*pgx.ConnConfig, error) {
							parses++
							if got.ReadOnly != profile.ReadOnly || got.ConfirmWrites != profile.ConfirmWrites || got.ConfirmDDL != profile.ConfirmDDL || got.CLIAccess != profile.CLIAccess {
								t.Error("connection safeguards changed")
							}
							return nil, errors.New("SECRET parse failure")
						},
						Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { helpers++; return "SECRET", nil }},
						Dial: func(context.Context, string, string) (net.Conn, error) {
							dials++
							return nil, errors.New("SECRET network")
						},
					}}
					client, _, err := service.Open(context.Background(), profile.Name, required)
					wantCode, wantParses := PolicyDenied, 0
					if tc.allowed[index] {
						wantCode, wantParses = ConnectionUnavailable, 1
					}
					var safe *PublicError
					if client != nil || !errors.As(err, &safe) || safe.Code != wantCode || parses != wantParses || helpers != 0 || dials != 0 || strings.Contains(err.Error(), "SECRET") {
						t.Fatalf("code=%v parses=%d helpers=%d dials=%d", err, parses, helpers, dials)
					}
				})
			}
		}
	}
}

func TestMachineOpen_PolicyOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []models.Connection
		required models.CLIAccess
	}{
		{"dev", []models.Connection{{Name: "dev", Driver: "postgres"}}, models.CLIAccessMetadata},
		{"dev", []models.Connection{{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessMetadata}}, models.CLIAccessRowRead},
		{"DEV", []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessRowRead}}, models.CLIAccessMetadata},
		{"dev", nil, models.CLIAccessMetadata},
		{"dev", []models.Connection{{Name: "dev", CLIAccess: models.CLIAccessRowRead}, {Name: "dev", CLIAccess: models.CLIAccessDisabled}}, models.CLIAccessRowRead},
		{"", nil, models.CLIAccessMetadata},
		{strings.Repeat("n", 257), nil, models.CLIAccessMetadata},
	} {
		calls := 0
		service := InvocationService{Load: func() ([]models.Connection, error) { return tc.profiles, nil }, Options: pg.MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) {
			calls++
			return nil, errors.New("must-not-parse service/TLS/pgpass")
		}}}
		if _, _, err := service.Open(context.Background(), tc.name, tc.required); err == nil || calls != 0 {
			t.Fatalf("policy order: name %q calls%d err%v", tc.name, calls, err)
		}
	}
	profiles := []models.Connection{{Name: "exact", Tags: []string{"tag"}, HiddenSchemas: []string{"hidden"}, SSHTunnel: &models.SSHTunnelConfig{Host: "bastion"}}}
	snapshot, err := ResolveExactProfile(profiles, "exact")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Tags[0] = "changed"
	snapshot.HiddenSchemas[0] = "changed"
	snapshot.SSHTunnel.Host = "changed"
	if profiles[0].Tags[0] != "tag" || profiles[0].HiddenSchemas[0] != "hidden" || profiles[0].SSHTunnel.Host != "bastion" {
		t.Fatal("profile snapshot shares mutable fields")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancel()
	called := false
	if _, _, err := (InvocationService{Load: func() ([]models.Connection, error) { called = true; return nil, nil }}).Open(ctx, "dev", models.CLIAccessMetadata); err == nil || called {
		t.Fatal("cancelled invocation loaded profiles")
	}
}

func TestMachineOpen_Privacy(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "/c.yml"
	for _, text := range []string{"connections:\n  - name: dev\n    password: SECRET\n", "connections:\n  - name: dev\n    unknown: SECRET\n"} {
		if err := afero.WriteFile(fs, path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadProfiles(fs, path)
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Fatal("profile error leaked")
		}
	}
	profiles := []models.Connection{{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessMetadata, DSN: "host=127.0.0.1 user=u dbname=d sslmode=disable passfile=/tmp/opencode/no-such-pass"}}
	service := InvocationService{Load: func() ([]models.Connection, error) { return profiles, nil }, Options: pg.MachineOptions{Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("SECRET DSN HELPER SQL")
	}}}
	_, _, err := service.Open(context.Background(), "dev", models.CLIAccessMetadata)
	var safe *PublicError
	if !errors.As(err, &safe) || safe.Code != ConnectionUnavailable || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("network privacy: %v", err)
	}
}

func TestMachineProfiles_LoadRegularFile(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "connections.yml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(afero.NewOsFs(), fifo); err == nil || safeError(err).Code != ConnectionUnavailable {
		t.Fatal("profile FIFO accepted or classification changed")
	}
	if _, err := LoadProfiles(afero.NewOsFs(), dir); err == nil || safeError(err).Code != ConnectionUnavailable {
		t.Fatal("profile directory accepted or classification changed")
	}
	if profiles, err := LoadProfiles(afero.NewOsFs(), filepath.Join(dir, "missing")); err != nil || profiles == nil || len(profiles) != 0 {
		t.Fatal("offline missing config did not stay empty")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("profile loading created files")
	}
}

type serviceBlockedWrite struct {
	net.Conn
	block   atomic.Bool
	release chan struct{}
	once    sync.Once
}

func (c *serviceBlockedWrite) Write(p []byte) (int, error) {
	if c.block.Load() {
		<-c.release
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *serviceBlockedWrite) Close() error {
	c.once.Do(func() { close(c.release) })
	return c.Conn.Close()
}

func TestMachineOpen_Lifecycle_ClosePublication(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		backend := pgproto3.NewBackend(conn, conn)
		if _, err := backend.ReceiveStartupMessage(); err != nil {
			return
		}
		backend.Send(&pgproto3.AuthenticationOk{})
		backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if backend.Flush() != nil {
			return
		}
		for {
			message, err := backend.Receive()
			if err != nil {
				return
			}
			switch message.(type) {
			case *pgproto3.Parse:
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				backend.Send(&pgproto3.NoData{})
			case *pgproto3.Execute:
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
			case *pgproto3.Sync:
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			case *pgproto3.Terminate:
				return
			default:
				return
			}
			if backend.Flush() != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("service fixture not joined")
		}
	})
	profile := models.Connection{Name: "dev", Driver: "postgres", CLIAccess: models.CLIAccessMetadata, DSN: "postgres://u@" + ln.Addr().String() + "/d?sslmode=disable&passfile=/tmp/opencode/no-such-pass", Password: "synthetic"}
	var blocked *serviceBlockedWrite
	service := InvocationService{Load: func() ([]models.Connection, error) { return []models.Connection{profile}, nil }, Options: pg.MachineOptions{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		blocked = &serviceBlockedWrite{Conn: raw, release: make(chan struct{})}
		return blocked, nil
	}}}
	streams, out, _ := testStreams()
	start := time.Now()
	_, exit := Dispatch(context.Background(), []string{"test", "--timeout=500ms"}, streams, map[string]Command{"test": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
		client, _, err := service.Open(ctx, "dev", models.CLIAccessMetadata)
		if err != nil {
			return Envelope{}, err
		}
		blocked.block.Store(true)
		return Envelope{}, CloseMachine(ctx, client)
	}})
	if exit != ExitResource || !strings.Contains(out.String(), `"code":"RESOURCE_FAILURE"`) || strings.Contains(out.String(), `"ok":true`) || time.Since(start) >= 500*time.Millisecond {
		t.Fatalf("cleanup/publication reserve: exit%d elapsed%s output%s", exit, time.Since(start), out.String())
	}
}
