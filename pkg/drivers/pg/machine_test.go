//go:build linux || darwin

package pg

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"go.uber.org/goleak"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type machineWireFixture struct {
	addr                             string
	mu                               sync.Mutex
	queries                          []string
	startup                          map[string]string
	started                          chan struct{}
	once                             sync.Once
	stallStartup, wrongAuth          bool
	connections                      atomic.Int32
	dropStartup, stallRollback       bool
	stallTLS                         bool
	stallFirstStartup, stallFirstTLS bool
	tlsConfig                        *tls.Config
	tlsName                          string
}

func newMachineWireFixture(t *testing.T) *machineWireFixture {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &machineWireFixture{addr: ln.Addr().String(), started: make(chan struct{})}
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			fixture.connections.Add(1)
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			wg.Go(func() {
				defer func() { _ = conn.Close(); mu.Lock(); delete(connections, conn); mu.Unlock() }()
				fixture.serve(conn)
			})
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for conn := range connections {
			_ = conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return fixture
}

func (f *machineWireFixture) serve(conn net.Conn) {
	backend := pgproto3.NewBackend(conn, conn)
	message, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := message.(*pgproto3.SSLRequest); ok {
		f.mu.Lock()
		configuration := f.tlsConfig
		stallTLS := f.stallTLS || (f.stallFirstTLS && f.connections.Load() == 1)
		f.mu.Unlock()
		if stallTLS {
			_, _ = io.Copy(io.Discard, conn)
			return
		}
		if configuration == nil {
			_, _ = conn.Write([]byte("N"))
			return
		}
		if _, err := conn.Write([]byte("S")); err != nil {
			return
		}
		tlsConn := tls.Server(conn, configuration)
		if tlsConn.Handshake() != nil {
			return
		}
		conn = tlsConn
		backend = pgproto3.NewBackend(conn, conn)
		message, err = backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
	}
	startup, ok := message.(*pgproto3.StartupMessage)
	if !ok {
		return
	}
	f.mu.Lock()
	f.startup = startup.Parameters
	stall, wrong, drop := f.stallStartup || (f.stallFirstStartup && f.connections.Load() == 1), f.wrongAuth, f.dropStartup
	f.mu.Unlock()
	f.once.Do(func() { close(f.started) })
	if drop {
		return
	}
	if stall {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	if wrong {
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: "28P01", Message: "SECRET authentication"})
		_ = backend.Flush()
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.NoticeResponse{Severity: "NOTICE", Code: "00000", Message: "SECRET notice SQL value"})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if backend.Flush() != nil {
		return
	}
	query := ""
	status := byte('I')
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch message := message.(type) {
		case *pgproto3.Parse:
			query = message.Query
			f.mu.Lock()
			f.queries = append(f.queries, query)
			f.mu.Unlock()
			backend.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			backend.Send(&pgproto3.BindComplete{})
		case *pgproto3.Describe:
			backend.Send(&pgproto3.NoData{})
		case *pgproto3.Execute:
			f.mu.Lock()
			stallRollback := f.stallRollback
			f.mu.Unlock()
			if query == "ROLLBACK" && stallRollback {
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if strings.Contains(query, "stalled-query") {
				_, _ = io.Copy(io.Discard, conn)
				return
			}
			if strings.HasPrefix(query, "BEGIN") {
				status = 'T'
			}
			if query == "ROLLBACK" {
				status = 'I'
			}
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SET")})
		case *pgproto3.Sync:
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		case *pgproto3.Terminate:
			return
		default:
			return
		}
		if backend.Flush() != nil {
			return
		}
	}
}

func (f *machineWireFixture) profile(t *testing.T) models.Connection {
	t.Helper()
	host, port, _ := net.SplitHostPort(f.addr)
	number, _ := strconv.Atoi(port)
	return models.Connection{Name: "synthetic", Driver: "postgres", Host: host, Port: number, User: "synthetic", Database: "synthetic", SSLMode: "disable", Password: "synthetic", CLIAccess: models.CLIAccessRowRead}
}

func machineTestEnvironment(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "PG") {
			t.Setenv(name, "")
		}
	}
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, dir)
	}
	t.Setenv("PGPASSFILE", filepath.Join(dir, "none"))
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "none-service"))
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(dir, "none-agent"))
}

func TestMachineOpen_Initialization(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	profile.StatementTimeout = "5s"
	client, err := OpenMachine(context.Background(), profile, MachineOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(cleanup); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	joined := strings.Join(fixture.queries, "\n")
	for _, sql := range []string{"SET default_transaction_read_only=on", "SET client_encoding='UTF8'", "SET DateStyle='ISO'", "SET TimeZone='UTC'", "SET IntervalStyle='postgres'", "SET bytea_output='hex'", "SET extra_float_digits=3", "SET statement_timeout=5000", "BEGIN READ ONLY", "SET LOCAL statement_timeout=5000", "SELECT 1", "ROLLBACK"} {
		if !strings.Contains(joined, sql) {
			t.Errorf("initialization missing %s", sql)
		}
	}
	if _, present := fixture.startup["default_transaction_read_only"]; present {
		t.Fatal("read-only startup default precedes target selection")
	}
	if fixture.startup["client_encoding"] != "UTF8" || fixture.connections.Load() != 1 {
		t.Fatal("startup defaults/single ownership lost")
	}
}

func TestMachineFullAccessPolicy(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
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
					profile := fixture.profile(t)
					profile.CLIAccess, profile.ReadOnly = tc.access, flags&1 != 0
					profile.ConfirmWrites, profile.ConfirmDDL = flags&2 != 0, flags&4 != 0
					profile.Password, profile.PasswordCommand = "", "SECRET helper"
					parses, helpers, dials := 0, 0, 0
					client, err := OpenMachine(context.Background(), profile, MachineOptions{
						RequiredAccess: required,
						ParseConfig: func(ctx context.Context, got models.Connection) (*pgx.ConnConfig, error) {
							parses++
							if got.ReadOnly != profile.ReadOnly || got.ConfirmWrites != profile.ConfirmWrites || got.ConfirmDDL != profile.ConfirmDDL {
								t.Error("connection safeguards changed")
							}
							return session.BuildMachineConfig(ctx, got)
						},
						Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) { helpers++; return "synthetic", nil }},
						Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
							dials++
							return (&net.Dialer{}).DialContext(ctx, network, addr)
						},
					})
					if !tc.allowed[index] {
						var safe *MachineError
						if client != nil || !errors.As(err, &safe) || safe.Code != "POLICY_DENIED" || parses != 0 || helpers != 0 || dials != 0 {
							t.Fatalf("denied open: err=%v parses=%d helpers=%d dials=%d", err, parses, helpers, dials)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := client.Close(context.Background()); err != nil {
							t.Error(err)
						}
					}()
					if parses != 1 || helpers != 1 || dials != 1 || client.access != tc.access {
						t.Fatalf("allowed open: parses=%d helpers=%d dials=%d access=%q", parses, helpers, dials, client.access)
					}
					if _, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineSchemas}); err != nil {
						t.Fatalf("metadata implied: %v", err)
					}
					_, err = client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 1"})
					if tc.access == models.CLIAccessMetadata {
						var safe *MachineError
						if !errors.As(err, &safe) || safe.Code != "POLICY_DENIED" {
							t.Fatalf("metadata queried rows: %v", err)
						}
					} else if err != nil {
						t.Fatalf("guarded read implied: %v", err)
					}
					for _, sql := range []string{"DELETE FROM secret", "SELECT 1; SELECT 2", "SELECT * FROM secret FOR UPDATE", "WITH changed AS (DELETE FROM secret RETURNING *) SELECT * FROM changed"} {
						_, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: sql})
						want := "UNSUPPORTED_SQL"
						if tc.access == models.CLIAccessMetadata {
							want = "POLICY_DENIED"
						}
						var safe *MachineError
						if !errors.As(err, &safe) || safe.Code != want {
							t.Fatalf("unguarded SQL %q: %v", sql, err)
						}
					}
				})
			}
		}
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	counts := map[string]int{}
	for _, sql := range fixture.queries {
		counts[sql]++
		if strings.Contains(sql, "secret") || sql == "SET default_transaction_read_only=off" || sql == "COMMIT" {
			t.Errorf("unguarded execution: %s", sql)
		}
	}
	if counts["SET default_transaction_read_only=on"] != 72 || counts["BEGIN READ ONLY"] != 56 || counts["BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY"] != 72 || counts["ROLLBACK"] != 128 {
		t.Fatalf("read-only transaction invariants changed: %v", counts)
	}
}

func TestMachineFullAccessPolicy_Gates(t *testing.T) {
	for _, tc := range []struct {
		access                 models.CLIAccess
		queryCode, inspectCode string
	}{
		{"", "POLICY_DENIED", "POLICY_DENIED"},
		{models.CLIAccessDisabled, "POLICY_DENIED", "POLICY_DENIED"},
		{models.CLIAccessMetadata, "POLICY_DENIED", "INVALID_INPUT"},
		{models.CLIAccessRowRead, "UNSUPPORTED_SQL", "INVALID_INPUT"},
		{models.CLIAccessFull, "UNSUPPORTED_SQL", "INVALID_INPUT"},
		{"invalid", "POLICY_DENIED", "POLICY_DENIED"},
		{"FULL", "POLICY_DENIED", "POLICY_DENIED"},
	} {
		t.Run(string(tc.access), func(t *testing.T) {
			client := &MachineClient{access: tc.access}
			_, err := client.QueryReadOnly(context.Background(), MachineQuery{SQL: "DELETE FROM secret"})
			var safe *MachineError
			if !errors.As(err, &safe) || safe.Code != tc.queryCode {
				t.Fatalf("query boundary: %v", err)
			}
			_, err = client.Inspect(context.Background(), MachineMetadataRequest{Operation: "invalid"})
			if !errors.As(err, &safe) || safe.Code != tc.inspectCode {
				t.Fatalf("metadata boundary: %v", err)
			}
		})
	}
}

func TestMachineFullAccessPolicy_Concurrent(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	var wg sync.WaitGroup
	for _, access := range []models.CLIAccess{"full", models.CLIAccessMetadata, models.CLIAccessRowRead, "full"} {
		wg.Go(func() {
			profile := fixture.profile(t)
			profile.CLIAccess = access
			client, err := OpenMachine(context.Background(), profile, MachineOptions{RequiredAccess: access})
			if err != nil {
				t.Error(err)
				return
			}
			if client.access != access {
				t.Errorf("access leaked between opens: %q != %q", client.access, access)
			}
			if _, err := client.Inspect(context.Background(), MachineMetadataRequest{Operation: MachineSchemas}); err != nil {
				t.Error(err)
			}
			_, err = client.QueryReadOnly(context.Background(), MachineQuery{SQL: "SELECT 1"})
			if access == models.CLIAccessMetadata {
				var safe *MachineError
				if !errors.As(err, &safe) || safe.Code != "POLICY_DENIED" {
					t.Errorf("metadata inherited row access: %v", err)
				}
			} else if err != nil {
				t.Error(err)
			}
			if err := client.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if fixture.connections.Load() != 4 {
		t.Fatalf("independent opens: connections=%d", fixture.connections.Load())
	}
}

func TestMachineFullAccessPolicy_NetworkPrivacy(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	profile.CLIAccess = "full"
	dials := 0
	_, err := OpenMachine(context.Background(), profile, MachineOptions{RequiredAccess: "full", Dial: func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("SECRET DSN HELPER SQL")
	}})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "CONNECTION_UNAVAILABLE" || strings.Contains(err.Error(), "SECRET") || dials != 1 {
		t.Fatalf("network privacy: %v dials=%d", err, dials)
	}
}

func TestMachineOpen_TargetSnapshot(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	profile.Password = ""
	profile.PasswordCommand = "trusted"
	parsed, err := session.BuildMachineConfig(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	parses := 0
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { parses++; return parsed, nil }, Credentials: session.MachineCredentialOptions{Exec: func(context.Context, string) (string, error) {
		parsed.Host = "wrong.invalid"
		parsed.Port = 1
		parsed.RuntimeParams["client_encoding"] = "SQL_ASCII"
		return "synthetic", nil
	}}})
	if err != nil || parses != 1 {
		t.Fatalf("frozen config: %v parses=%d", err, parses)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	encoding := fixture.startup["client_encoding"]
	fixture.mu.Unlock()
	if fixture.connections.Load() != 1 || encoding != "UTF8" {
		t.Fatal("credential callback mutated frozen target")
	}
	profile = fixture.profile(t)
	profile.Password = ""
	profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=disable"
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("first:5432:d:u:one\nsecond:5432:d:u:two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile.PgpassPath = path
	dials := 0
	_, err = OpenMachine(context.Background(), profile, MachineOptions{Dial: func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, errors.New("unexpected network")
	}})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "UNSUPPORTED_PGPASS_FAILOVER" || dials != 0 {
		t.Fatalf("pgpass before network: %v dials%d", err, dials)
	}
}

func TestMachineOpen_TargetSnapshot_Failover(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=disable"
	profile.Password = ""
	path := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(path, []byte("first:5432:d:u:one\nsecond:5432:d:u:one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	profile.PgpassPath = path
	var calls atomic.Int32
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
		cfg, err := session.BuildMachineConfig(ctx, p)
		if err == nil {
			cfg.LookupFunc = func(_ context.Context, name string) ([]string, error) { return []string{name}, nil }
		}
		return cfg, err
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		calls.Add(1)
		if strings.HasPrefix(addr, "first:") {
			return nil, errors.New("synthetic refused endpoint")
		}
		return (&net.Dialer{}).DialContext(ctx, network, fixture.addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("static credential failover dials=%d", calls.Load())
	}
}

func TestMachineOpen_Lifecycle_Concurrent(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			client, err := OpenMachine(context.Background(), profile, MachineOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			if err := client.Ping(context.Background()); err != nil {
				t.Error(err)
			}
			if err := client.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if fixture.connections.Load() != 4 {
		t.Fatal("independent invocations did not own four connections")
	}
}

func machineBastionFixture(t *testing.T, backendAddr, remoteName, stallName string) (models.SSHTunnelConfig, *atomic.Int32) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	identity := filepath.Join(dir, "identity")
	if err := os.WriteFile(identity, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	known := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{ln.Addr().String()}, signer.PublicKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	count := &atomic.Int32{}
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	wg.Go(func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			mu.Lock()
			connections[raw] = true
			mu.Unlock()
			wg.Go(func() {
				defer func() { _ = raw.Close(); mu.Lock(); delete(connections, raw); mu.Unlock() }()
				cfg := &ssh.ServerConfig{NoClientAuth: true}
				cfg.AddHostKey(signer)
				server, chans, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer func() { _ = server.Close() }()
				go ssh.DiscardRequests(requests)
				for next := range chans {
					var target struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if next.ChannelType() != "direct-tcpip" || ssh.Unmarshal(next.ExtraData(), &target) != nil {
						_ = next.Reject(ssh.ConnectionFailed, "synthetic mapping missing")
						continue
					}
					if target.Host == stallName {
						continue
					}
					if target.Host != remoteName && remoteName != "*" {
						_ = next.Reject(ssh.ConnectionFailed, "synthetic mapping missing")
						continue
					}
					channel, requests, err := next.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(requests)
					wg.Go(func() {
						defer func() { _ = channel.Close() }()
						backend, err := net.Dial("tcp", backendAddr)
						if err != nil {
							return
						}
						defer func() { _ = backend.Close() }()
						var copies sync.WaitGroup
						copies.Go(func() {
							_, _ = io.Copy(backend, channel)
							if tcp, ok := backend.(*net.TCPConn); ok {
								_ = tcp.CloseWrite()
							}
						})
						copies.Go(func() { _, _ = io.Copy(channel, backend); _ = channel.Close() })
						copies.Wait()
					})
				}
			})
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for raw := range connections {
			_ = raw.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	number, _ := strconv.Atoi(port)
	return models.SSHTunnelConfig{Host: host, Port: number, User: "synthetic", IdentityFile: identity, KnownHosts: known}, count
}

func TestMachineOpen_TargetSnapshot_SSHDNS(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	const remote = "pgsavvy-v17a-bastion-only.invalid"
	lookup, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if addresses, err := net.DefaultResolver.LookupHost(lookup, remote); err == nil && len(addresses) > 0 {
		t.Fatal("fixture name unexpectedly resolves locally")
	}
	profile := fixture.profile(t)
	profile.Host = remote
	profile.Port = 5432
	tunnel, _ := machineBastionFixture(t, fixture.addr, remote, "")
	profile.SSHTunnel = &tunnel
	localLookups := 0
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
		cfg, err := session.BuildMachineConfig(ctx, p)
		if err == nil {
			cfg.LookupFunc = func(context.Context, string) ([]string, error) {
				localLookups++
				return nil, errors.New("local lookup denied")
			}
		}
		return cfg, err
	}})
	if err != nil {
		t.Fatalf("bastion-only name failed: %v", err)
	}
	if localLookups != 0 {
		t.Fatal("SSH did not replace local DNS")
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMachineOpen_Lifecycle(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	fixture.mu.Lock()
	fixture.stallStartup = true
	fixture.mu.Unlock()
	start := time.Now()
	_, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{ConnectTimeout: 30 * time.Millisecond})
	var safe *MachineError
	if !errors.As(err, &safe) || safe.Code != "TIMEOUT" || time.Since(start) > 2*time.Second {
		t.Fatalf("startup timeout: %v elapsed%s", err, time.Since(start))
	}
	fixture = newMachineWireFixture(t)
	var dials atomic.Int32
	client, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = client.withReadOnly(ctx, false, "", func(ctx context.Context) error { return client.exec(ctx, "SELECT 'stalled-query'") })
	cancel()
	cancel()
	if !errors.As(err, &safe) || safe.Code != "TIMEOUT" {
		t.Fatalf("query cancellation: %v", err)
	}
	start = time.Now()
	_ = client.Close(context.Background())
	client.Abort()
	_ = client.Close(context.Background())
	if time.Since(start) > 2*time.Second || dials.Load() != 1 {
		t.Fatalf("cleanup/auxiliarydial: %s dials%d", time.Since(start), dials.Load())
	}
}

func TestMachineOpen_Privacy(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	fixture.mu.Lock()
	fixture.wrongAuth = true
	fixture.mu.Unlock()
	_, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("auth error leaked: %v", err)
	}
	denied := fixture.profile(t)
	denied.CLIAccess = models.CLIAccessDisabled
	called := false
	_, err = OpenMachine(context.Background(), denied, MachineOptions{ParseConfig: func(context.Context, models.Connection) (*pgx.ConnConfig, error) { called = true; return nil, nil }})
	if err == nil || called {
		t.Fatal("direct machine client bypassed policy")
	}
}

func TestMachineOpen_Lifecycle_TimeoutFailoverAndJoinedStartup(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	machineTestEnvironment(t)
	stalled := newMachineWireFixture(t)
	stalled.mu.Lock()
	stalled.stallStartup = true
	stalled.mu.Unlock()
	healthy := newMachineWireFixture(t)
	profile := healthy.profile(t)
	profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=disable"
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
		cfg, err := session.BuildMachineConfig(ctx, p)
		if err == nil {
			cfg.ConnectTimeout = 30 * time.Millisecond
			cfg.LookupFunc = func(_ context.Context, name string) ([]string, error) { return []string{name}, nil }
		}
		return cfg, err
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		target := healthy.addr
		if strings.HasPrefix(addr, "first:") {
			target = stalled.addr
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}})
	if err != nil {
		t.Fatalf("timed-out host prevented failover: %v", err)
	}
	if len(client.attempts) != 2 {
		t.Fatalf("attempt tracking=%d", len(client.attempts))
	}
	select {
	case <-client.attempts[0].CleanupDone():
	default:
		t.Fatal("failed fallback cleanup not joined")
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	dropped := newMachineWireFixture(t)
	dropped.mu.Lock()
	dropped.dropStartup = true
	dropped.mu.Unlock()
	var owner *MachineClient
	_, err = OpenMachine(context.Background(), dropped.profile(t), MachineOptions{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		owner = ctx.Value(machineDialKey{}).(*MachineClient)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}})
	if err == nil || owner == nil || len(owner.attempts) != 1 {
		t.Fatalf("dropped startup not exercised: %v", err)
	}
	select {
	case <-owner.attempts[0].CleanupDone():
	default:
		t.Fatal("nil-return startup cleanup not joined")
	}
}

type machineBlockedWrite struct {
	net.Conn
	block   atomic.Bool
	release chan struct{}
	once    sync.Once
}

func (c *machineBlockedWrite) Write(p []byte) (int, error) {
	if c.block.Load() {
		<-c.release
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *machineBlockedWrite) Close() error {
	c.once.Do(func() { close(c.release) })
	return c.Conn.Close()
}

func TestMachineOpen_Lifecycle_RollbackAndCloseBudget(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	fixture.mu.Lock()
	fixture.stallRollback = true
	fixture.mu.Unlock()
	client, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{CleanupTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := client.Ping(context.Background()); err == nil {
		t.Fatal("stalled rollback succeeded")
	}
	deadline, _ := client.freshCleanup().Deadline()
	_ = client.Close(context.Background())
	after, _ := client.freshCleanup().Deadline()
	if deadline != after || time.Since(start) > time.Second {
		t.Fatal("close reset shared rollback cleanup budget")
	}
	fixture = newMachineWireFixture(t)
	var blocked *machineBlockedWrite
	client, err = OpenMachine(context.Background(), fixture.profile(t), MachineOptions{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		blocked = &machineBlockedWrite{Conn: raw, release: make(chan struct{})}
		return blocked, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	blocked.block.Store(true)
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start = time.Now()
	if err := client.Close(cleanup); err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked close was not bounded: %v", err)
	}
	select {
	case <-client.conn.PgConn().CleanupDone():
	default:
		t.Fatal("blocked close left cleanup unjoined")
	}
}

func TestMachineOpen_TargetSnapshot_SSHTLS(t *testing.T) {
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	const remote = "pgsavvy-bastion-tls.invalid"
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{remote}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "root.pem")
	if err := os.WriteFile(root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	fixture.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, GetConfigForClient: func(info *tls.ClientHelloInfo) (*tls.Config, error) {
		fixture.mu.Lock()
		fixture.tlsName = info.ServerName
		fixture.mu.Unlock()
		return nil, nil
	}}
	fixture.mu.Unlock()
	profile := fixture.profile(t)
	profile.DSN = "postgres://synthetic@" + remote + "/synthetic?sslmode=verify-full&sslrootcert=" + url.QueryEscape(root)
	tunnel, _ := machineBastionFixture(t, fixture.addr, remote, "")
	profile.SSHTunnel = &tunnel
	client, err := OpenMachine(context.Background(), profile, MachineOptions{})
	if err != nil {
		t.Fatalf("verify-full over SSH: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	name := fixture.tlsName
	fixture.mu.Unlock()
	if name != remote {
		t.Fatalf("TLS name changed: %q", name)
	}
}

func TestMachineOpen_TargetSnapshot_SSHChannelTimeoutFailover(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	machineTestEnvironment(t)
	fixture := newMachineWireFixture(t)
	profile := fixture.profile(t)
	profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=disable"
	tunnel, count := machineBastionFixture(t, fixture.addr, "second", "first")
	profile.SSHTunnel = &tunnel
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ConnectTimeout: time.Second, ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
		cfg, err := session.BuildMachineConfig(ctx, p)
		if err == nil {
			cfg.ConnectTimeout = 100 * time.Millisecond
		}
		return cfg, err
	}})
	if err != nil {
		t.Fatalf("SSH channel timeout prevented fallback: %v", err)
	}
	if count.Load() != 2 {
		t.Fatalf("expected closed old tunnel plus replacement, got%d connections", count.Load())
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMachineOpen_Lifecycle_TLSNegotiationTimeoutFailover(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
	machineTestEnvironment(t)
	stalled := newMachineWireFixture(t)
	stalled.mu.Lock()
	stalled.stallTLS = true
	stalled.mu.Unlock()
	healthy := newMachineWireFixture(t)
	profile := healthy.profile(t)
	profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=prefer"
	client, err := OpenMachine(context.Background(), profile, MachineOptions{ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
		cfg, err := session.BuildMachineConfig(ctx, p)
		if err == nil {
			cfg.ConnectTimeout = 30 * time.Millisecond
			cfg.LookupFunc = func(_ context.Context, name string) ([]string, error) { return []string{name}, nil }
		}
		return cfg, err
	}, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		target := healthy.addr
		if strings.HasPrefix(addr, "first:") {
			target = stalled.addr
		}
		return (&net.Dialer{}).DialContext(ctx, network, target)
	}})
	if err != nil {
		t.Fatalf("pre-handler TLS timeout prevented fallback: %v", err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMachineOpen_TargetSnapshot_SSHStartupTimeoutFailover(t *testing.T) {
	for _, mode := range []string{"startup", "tls"} {
		t.Run(mode, func(t *testing.T) {
			baseline := goleak.IgnoreCurrent()
			t.Cleanup(func() { goleak.VerifyNone(t, baseline) })
			machineTestEnvironment(t)
			fixture := newMachineWireFixture(t)
			fixture.mu.Lock()
			fixture.stallFirstStartup = mode == "startup"
			fixture.stallFirstTLS = mode == "tls"
			fixture.mu.Unlock()
			profile := fixture.profile(t)
			ssl := "disable"
			if mode == "tls" {
				ssl = "prefer"
			}
			profile.DSN = "host=first,second port=5432 user=u dbname=d sslmode=" + ssl
			tunnel, count := machineBastionFixture(t, fixture.addr, "*", "")
			profile.SSHTunnel = &tunnel
			client, err := OpenMachine(context.Background(), profile, MachineOptions{ConnectTimeout: time.Second, ParseConfig: func(ctx context.Context, p models.Connection) (*pgx.ConnConfig, error) {
				cfg, err := session.BuildMachineConfig(ctx, p)
				if err == nil {
					cfg.ConnectTimeout = 100 * time.Millisecond
				}
				return cfg, err
			}})
			if err != nil {
				t.Fatalf("SSH %s timeout prevented fallback: %v", mode, err)
			}
			if count.Load() != 2 {
				t.Fatalf("stalled SSH startup did not replace exactly one tunnel: %d", count.Load())
			}
			if err := client.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
