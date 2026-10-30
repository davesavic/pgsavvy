//go:build integration && nativefixture && (linux || darwin)

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type temporaryProcessFixture struct {
	observer                   *pgx.Conn
	target                     []string
	passfile, password, socket string
	watchForbidden             bool
}

type temporaryProcessOwnership struct {
	Token    string `json:"token"`
	DSN      string `json:"observer_dsn"`
	SystemID string `json:"system_identifier"`
	DataDir  string `json:"data_directory"`
}

func temporaryProcessObserverConfig(root, token, dsn string) (*pgx.ConnConfig, temporaryProcessOwnership, error) {
	var owner temporaryProcessOwnership
	denied := errors.New("owned native observer authorization failed")
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "pgsavvy-native.") || token == "" {
		return nil, owner, denied
	}
	path := filepath.Join(root, "temporary-owner")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8192 {
		return nil, owner, denied
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Token != token || owner.DSN != dsn {
		return nil, owner, denied
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	identifier, idErr := strconv.ParseUint(owner.SystemID, 10, 64)
	if err != nil || idErr != nil || identifier == 0 || owner.DataDir != directory {
		return nil, owner, denied
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Database != "pgsavvy_test" || cfg.User != "pgsavvy" || cfg.Password == "" {
		return nil, owner, denied
	}
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.RuntimeParams = map[string]string{}
	cfg.ConnectTimeout = 5 * time.Second
	endpoint := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != endpoint {
			return nil, denied
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return cfg, owner, nil
}

func temporaryProcessVerifyObserver(ctx context.Context, observer *pgx.Conn, owner temporaryProcessOwnership) error {
	var identifier, directory, database, username string
	err := observer.QueryRow(ctx, "SELECT system_identifier::text,current_setting('data_directory'),current_database(),current_user FROM pg_control_system()").Scan(&identifier, &directory, &database, &username)
	if err != nil || identifier != owner.SystemID || directory != owner.DataDir || database != "pgsavvy_test" || username != "pgsavvy" {
		return errors.New("connected observer identity is not owned")
	}
	return nil
}

// Linux inotify observes opens/reads, unlike before/after snapshots, which
// establish only mutation/persistence. Watch only forbidden files, never the
// explicitly selected passfile or pgx's permitted metadata/default checks.
const temporaryForbiddenReadObserver = `
import ctypes, json, os, pathlib, subprocess, sys
binary, root, *args = sys.argv[1:]
lib = ctypes.CDLL(None, use_errno=True)
lib.inotify_init1.argtypes = [ctypes.c_int]
lib.inotify_add_watch.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_uint32]
fd = lib.inotify_init1(os.O_NONBLOCK | os.O_CLOEXEC)
if fd < 0: raise RuntimeError('access instrumentation unavailable')
try:
    for name in ('home/.pgpass', 'home/.pg_service.conf', 'config/pgsavvy/connections.yml', 'data/pgsavvy/keyring', 'state/pgsavvy/history.sqlite'):
        if lib.inotify_add_watch(fd, os.fsencode(pathlib.Path(root) / name), 0x1 | 0x20) < 0:
            raise RuntimeError('forbidden access watch unavailable')
    p = subprocess.run([binary, *args], input=sys.stdin.buffer.read(), stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=8)
    try: events = len(os.read(fd, 65536))
    except BlockingIOError: events = 0
    print(json.dumps({'exit':p.returncode, 'stdout':p.stdout.decode(), 'stderr':p.stderr.decode(), 'access_events':events}))
finally:
    os.close(fd)
`

func temporaryProcessOwned(t *testing.T) temporaryProcessFixture {
	t.Helper()
	root, token := os.Getenv("PGSAVVY_TEMP_FIXTURE_ROOT"), os.Getenv("PGSAVVY_TEMP_FIXTURE_TOKEN")
	cfg, owner, err := temporaryProcessObserverConfig(root, token, os.Getenv("PGSAVVY_TEST_PG"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	observer, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err, "owned observer unavailable")
	t.Cleanup(func() { require.NoError(t, observer.Close(context.Background())) })
	require.NoError(t, temporaryProcessVerifyObserver(ctx, observer, owner))
	passfile, socket := os.Getenv("PGSAVVY_TEMP_FIXTURE_PASSFILE"), os.Getenv("PGSAVVY_TEMP_FIXTURE_SOCKET")
	require.Equal(t, filepath.Join(root, "temporary.pass"), passfile)
	require.Equal(t, filepath.Join(root, "socket"), socket)
	data, err := os.ReadFile(passfile)
	require.NoError(t, err)
	_, password, found := strings.Cut(strings.Split(string(data), "\n")[0], "pgsavvy_temporary:")
	require.True(t, found)
	info, err := os.Stat(passfile)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o600), info.Mode().Perm())
	return temporaryProcessFixture{observer, []string{"--temporary", "--port", strconv.Itoa(int(observer.Config().Port)), "--database", observer.Config().Database, "--username", "pgsavvy_temporary", "--passfile", passfile}, passfile, password, socket, false}
}

func TestTemporaryCLIProcessObserverOwnership(t *testing.T) {
	root, err := os.MkdirTemp(t.TempDir(), "pgsavvy-native.")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "data"), 0o700))
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	require.NoError(t, err)
	const dsn = "postgres://pgsavvy:private-fixture@127.0.0.1:65432/pgsavvy_test?sslmode=disable"
	owner := temporaryProcessOwnership{Token: "private-token", DSN: dsn, SystemID: "1234567890123456789", DataDir: directory}
	data, err := json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	for _, wrong := range []string{
		strings.Replace(dsn, "127.0.0.1", "192.0.2.1", 1), strings.Replace(dsn, "65432", "65433", 1),
		strings.Replace(dsn, "pgsavvy_test", "unrelated", 1), strings.Replace(dsn, "private-fixture", "different", 1), dsn + "&options=-cstatement_timeout=0",
	} {
		cfg, _, err := temporaryProcessObserverConfig(root, owner.Token, wrong)
		require.Error(t, err)
		require.Nil(t, cfg, "mismatched DSN produced a connectable artifact")
	}
	cfg, _, err := temporaryProcessObserverConfig(root, owner.Token, dsn)
	require.NoError(t, err)
	conn, err := cfg.DialFunc(context.Background(), "tcp", "127.0.0.1:65433")
	require.Error(t, err)
	require.Nil(t, conn)
	_, _, err = temporaryProcessObserverConfig(root, "wrong-token", dsn)
	require.Error(t, err)
	owner.SystemID = ""
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryProcessObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "missing cluster identity was accepted")
	owner.SystemID, owner.DataDir = "1234567890123456789", filepath.Join(directory, "unrelated")
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryProcessObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "unowned data directory was accepted")
}

func temporaryProcessEnv(root string) []string {
	var clean []string
	for _, entry := range machineTestEnv(root) {
		if !strings.HasPrefix(entry, "PG") {
			clean = append(clean, entry)
		}
	}
	return clean
}

func (f temporaryProcessFixture) command(t *testing.T, root, sql string, args ...string) (*exec.Cmd, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	command := exec.CommandContext(ctx, machineTestBinary, args...)
	command.Env = temporaryProcessEnv(root)
	command.Stdin = strings.NewReader(sql)
	require.NotContains(t, strings.Join(command.Args, " "), f.password, "password in argv")
	return command, cancel
}

func (f temporaryProcessFixture) run(t *testing.T, root, sql string, poison []string, args ...string) (map[string]any, int) {
	t.Helper()
	command, cancel := f.command(t, root, sql, args...)
	defer cancel()
	command.Env = append(command.Env, poison...)
	if f.watchForbidden {
		python, err := exec.LookPath("python3")
		require.NoError(t, err)
		command.Path = python
		command.Args = append([]string{python, "-c", temporaryForbiddenReadObserver, machineTestBinary, root}, args...)
	}
	var out, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &out, &diagnostic
	exit := temporaryProcessExit(t, command.Run())
	if f.watchForbidden {
		require.Zero(t, exit, "access observer failed")
		var wire struct {
			Exit         int    `json:"exit"`
			AccessEvents int    `json:"access_events"`
			Stdout       string `json:"stdout"`
			Stderr       string `json:"stderr"`
		}
		require.NoError(t, json.Unmarshal(out.Bytes(), &wire))
		require.Zero(t, wire.AccessEvents, "forbidden saved/credential file was opened or read")
		exit = wire.Exit
		out.Reset()
		diagnostic.Reset()
		_, _ = out.WriteString(wire.Stdout)
		_, _ = diagnostic.WriteString(wire.Stderr)
	}
	for _, secret := range []string{f.password, f.passfile, "FORBIDDEN-SAVED-SECRET", "FORBIDDEN-AMBIENT-SECRET", "postgres://"} {
		require.NotContains(t, out.String()+diagnostic.String(), secret)
	}
	doc := machineTestDocument(t, out.Bytes(), exit)
	if diagnostic.Len() != 0 {
		var safe map[string]any
		require.NoError(t, json.Unmarshal(diagnostic.Bytes(), &safe))
		require.Len(t, safe, 2)
	}
	return doc, exit
}

func temporaryProcessExit(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	require.True(t, errors.As(err, &exit), "process failed to start")
	return exit.ExitCode()
}

type temporaryFileSnapshot struct {
	Mode     fs.FileMode
	Modified time.Time
	Digest   [32]byte
}

func temporarySnapshot(t *testing.T, root string) map[string]temporaryFileSnapshot {
	t.Helper()
	result := map[string]temporaryFileSnapshot{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var data []byte
		if entry.Type().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[name] = temporaryFileSnapshot{info.Mode(), info.ModTime(), sha256.Sum256(data)}
		return nil
	}))
	return result
}

func TestTemporaryCLIProcessIsolation(t *testing.T) {
	f := temporaryProcessOwned(t)
	t.Run("reject-connected-identity-mismatch", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.Error(t, temporaryProcessVerifyObserver(ctx, f.observer, temporaryProcessOwnership{SystemID: "not-owned"}))
	})
	f.watchForbidden = runtime.GOOS == "linux"
	root := t.TempDir()
	for _, name := range []string{"home/.pgpass", "home/.pg_service.conf", "config/pgsavvy/connections.yml", "data/pgsavvy/keyring", "state/pgsavvy/history.sqlite"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("FORBIDDEN-SAVED-SECRET\nmalformed profile/password/helper/keyring sentinel"), 0o600))
	}
	before := temporarySnapshot(t, root)
	if f.watchForbidden {
		t.Run("access-observer-positive-control", func(t *testing.T) {
			python, err := exec.LookPath("python3")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			probe := exec.CommandContext(ctx, python, "-c", temporaryForbiddenReadObserver, python, root, "-c", "import pathlib,sys; pathlib.Path(sys.argv[1]).read_bytes()", filepath.Join(root, "home/.pgpass"))
			probe.Env = temporaryProcessEnv(root)
			data, err := probe.Output()
			require.NoError(t, err)
			var observation struct {
				Access int `json:"access_events"`
			}
			require.NoError(t, json.Unmarshal(data, &observation))
			require.Positive(t, observation.Access, "instrumentation failed to detect deliberate forbidden read")
		})
	}
	passBefore, err := os.ReadFile(f.passfile)
	require.NoError(t, err)
	passInfo, err := os.Stat(f.passfile)
	require.NoError(t, err)
	for _, transport := range []string{"tcp", "unix"} {
		t.Run(transport, func(t *testing.T) {
			target := append([]string{}, f.target...)
			if transport == "unix" {
				target = append(target, "--socket-dir", f.socket)
			}
			for _, prefix := range [][]string{
				{"query", "--file", "-"}, {"schema", "list"}, {"schema", "tables", "--schema", "temporary_cli"},
				{"schema", "describe", "--schema", "temporary_cli", "--table", "effects"},
				{"schema", "relationships", "--schema", "temporary_cli", "--table", "effects"}, {"connections", "test"},
			} {
				doc, exit := f.run(t, root, "SELECT 42", nil, append(prefix, target...)...)
				require.Zero(t, exit, "temporary %s %s", transport, prefix[0])
				require.Nil(t, doc["connection"])
				require.Equal(t, "temporary", doc["target"].(map[string]any)["source"])
				if prefix[0] == "schema" {
					temporaryProcessSchemaProof(t, prefix[1], doc)
				}
				if prefix[0] == "connections" {
					require.Equal(t, true, doc["data"].(map[string]any)["connected"])
				}
				if prefix[0] == "query" {
					require.Equal(t, []any{[]any{"42"}}, doc["data"].(map[string]any)["results"].([]any)[0].(map[string]any)["rows"])
				}
			}
		})
	}
	t.Run("poison-environment-rejection-is-not-happy-path", func(t *testing.T) {
		for _, key := range []string{"PGPASSWORD", "PGPASSFILE", "PGSERVICE", "PGSERVICEFILE", "PGOPTIONS", "PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGSSLMODE"} {
			doc, exit := f.run(t, root, "SELECT 1", []string{key + "=FORBIDDEN-AMBIENT-SECRET"}, append([]string{"query", "--file", "-"}, f.target...)...)
			require.Equal(t, 2, exit, key)
			require.Equal(t, "not_started", doc["transaction_outcome"])
		}
	})
	require.Equal(t, before, temporarySnapshot(t, root), "profile/history/log/credential persistence or mutation")
	passAfter, err := os.ReadFile(f.passfile)
	require.NoError(t, err)
	afterInfo, err := os.Stat(f.passfile)
	require.NoError(t, err)
	require.Equal(t, passBefore, passAfter)
	require.True(t, os.SameFile(passInfo, afterInfo))
	require.Equal(t, passInfo.ModTime(), afterInfo.ModTime())
	require.Equal(t, passInfo.Mode(), afterInfo.Mode())
}

func temporaryProcessSchemaProof(t *testing.T, operation string, doc map[string]any) {
	t.Helper()
	data := doc["data"].(map[string]any)
	switch operation {
	case "list":
		var names []string
		for _, item := range data["items"].([]any) {
			names = append(names, item.(map[string]any)["name"].(string))
		}
		require.Contains(t, names, "temporary_cli")
	case "tables":
		require.Equal(t, "temporary_cli", data["schema"])
		var names []string
		for _, item := range data["items"].([]any) {
			relation := item.(map[string]any)
			require.Equal(t, "temporary_cli", relation["schema"])
			require.Equal(t, "table", relation["kind"])
			names = append(names, relation["name"].(string))
		}
		require.ElementsMatch(t, []string{"children", "deferred", "effects"}, names)
	case "describe", "relationships":
		relation := data["relation"].(map[string]any)
		require.Equal(t, "temporary_cli", relation["schema"])
		require.Equal(t, "effects", relation["name"])
		if operation == "describe" {
			columns := map[string]string{}
			for _, item := range data["columns"].([]any) {
				column := item.(map[string]any)
				columns[column["name"].(string)] = column["data_type"].(string)
			}
			require.Equal(t, map[string]string{"id": "integer", "value": "text"}, columns)
		} else {
			dataJSON, err := json.Marshal(data["inbound"])
			require.NoError(t, err)
			require.JSONEq(t, `[{"name":"children_parent_fkey","schema":"temporary_cli","table":"children","columns":["parent"],"ref_schema":"temporary_cli","ref_table":"effects","ref_columns":["id"],"on_update":"NO ACTION","on_delete":"NO ACTION"}]`, string(dataJSON))
			require.Empty(t, data["outbound"])
		}
	}
}

func (f temporaryProcessFixture) count(t *testing.T, id int) int {
	t.Helper()
	var count int
	require.NoError(t, f.observer.QueryRow(context.Background(), "SELECT count(*) FROM temporary_cli.effects WHERE id=$1", id).Scan(&count))
	return count
}

func (f temporaryProcessFixture) attempts(t *testing.T) int64 {
	t.Helper()
	var value int64
	// Normalize the uncalled initial sequence state to zero.
	require.NoError(t, f.observer.QueryRow(context.Background(), "SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM temporary_cli.attempts").Scan(&value))
	return value
}

func TestTemporaryCLIProcessBrokenStdout(t *testing.T) {
	f := temporaryProcessOwned(t)
	before := f.attempts(t)
	sql := "INSERT INTO temporary_cli.effects VALUES(2001,nextval('temporary_cli.attempts')::text)"
	args := append([]string{"query", "--file", "-", "--allow-write"}, f.target...)
	command, cancel := f.command(t, t.TempDir(), sql, args...)
	defer cancel()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	defer func() { _ = read.Close(); _ = write.Close() }()
	// A full owned pipe holds publication until the independent observer sees
	// the commit. Only then break the reader: startup must not fail first.
	fd := int(write.Fd())
	require.NoError(t, unix.SetNonblock(fd, true))
	for {
		_, err := unix.Write(fd, make([]byte, 4096))
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			break
		}
		require.NoError(t, err)
	}
	command.Stdout = write
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	require.NoError(t, command.Start())
	defer func() {
		cancel()
		if command.ProcessState == nil {
			_ = command.Wait()
		}
	}()
	require.Eventually(t, func() bool { return f.count(t, 2001) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, read.Close())
	exit := temporaryProcessExit(t, command.Wait())
	require.NotZero(t, exit)
	require.Equal(t, 1, f.count(t, 2001), "failed publication must not undo durable commit")
	require.Equal(t, before+1, f.attempts(t), "SQL replayed after broken stdout")
	require.NotContains(t, diagnostic.String(), f.password)
	require.NotContains(t, diagnostic.String(), sql)
	t.Logf("broken stdout exit=%d; independent observer durable rows=1 attempts=1", exit)
}

func TestTemporaryCLIProcessCommitInterruption(t *testing.T) {
	f := temporaryProcessOwned(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	var dials, commits atomic.Int32
	type observation struct {
		port int
		err  error
	}
	finished := make(chan observation, 1)
	joined := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-joined:
		case <-time.After(9 * time.Second):
			t.Error("commit proxy leaked")
		}
	})
	go func() {
		defer close(joined)
		client, err := listener.Accept()
		if err != nil {
			finished <- observation{err: err}
			return
		}
		dials.Add(1)
		defer func() { _ = client.Close() }()
		upstream, err := net.DialTimeout("tcp", net.JoinHostPort(f.observer.Config().Host, strconv.Itoa(int(f.observer.Config().Port))), time.Second)
		if err != nil {
			finished <- observation{err: err}
			return
		}
		defer func() { _ = upstream.Close() }()
		port := upstream.LocalAddr().(*net.TCPAddr).Port
		_ = client.SetDeadline(time.Now().Add(8 * time.Second))
		_ = upstream.SetDeadline(time.Now().Add(8 * time.Second))
		backend := pgproto3.NewBackend(client, client)
		startup, err := backend.ReceiveStartupMessage()
		if err == nil {
			var wire []byte
			wire, err = startup.Encode(nil)
			if err == nil {
				_, err = upstream.Write(wire)
			}
		}
		if err != nil {
			finished <- observation{port, err}
			return
		}
		copied := make(chan struct{})
		go func() {
			defer close(copied)
			frontend := pgproto3.NewFrontend(upstream, upstream)
			for {
				message, err := frontend.Receive()
				if err != nil {
					return
				}
				// No COMMIT response may escape even if the live server wins
				// the race with the deliberate connection interruption.
				if commits.Load() > 0 {
					continue
				}
				wire, err := message.Encode(nil)
				if err != nil {
					return
				}
				if _, err = client.Write(wire); err != nil {
					return
				}
			}
		}()
		defer func() { _ = upstream.Close(); _ = client.Close(); <-copied }()
		commit := false
		for {
			// Preserve SCRAM's password frames verbatim; decoding them as
			// PasswordMessage would corrupt the SASL exchange.
			header := make([]byte, 5)
			_, err := io.ReadFull(client, header)
			if err != nil {
				finished <- observation{port, err}
				return
			}
			length := binary.BigEndian.Uint32(header[1:])
			if length < 4 || length > 1<<20 {
				finished <- observation{port, errors.New("invalid proxy frame")}
				return
			}
			body := make([]byte, int(length)-4)
			if _, err = io.ReadFull(client, body); err != nil {
				finished <- observation{port, err}
				return
			}
			if header[0] == 'P' && bytes.Contains(body, []byte("\x00COMMIT\x00")) {
				commits.Add(1)
				commit = true
			}
			_, err = upstream.Write(append(header, body...))
			if err != nil {
				finished <- observation{port, err}
				return
			}
			if header[0] == 'S' && commit {
				// One real COMMIT was submitted. Lose its reply deliberately; do
				// not prescribe which side of the server's commit point wins.
				finished <- observation{port: port}
				return
			}
		}
	}()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	root := t.TempDir()
	proxyPass := filepath.Join(root, "proxy.pass")
	require.NoError(t, os.WriteFile(proxyPass, []byte("127.0.0.1:"+port+":"+f.observer.Config().Database+":pgsavvy_temporary:"+f.password+"\n"), 0o600))
	target := append([]string{}, f.target...)
	target[2], target[len(target)-1] = port, proxyPass
	before := f.attempts(t)
	doc, exit := f.run(t, root, "INSERT INTO temporary_cli.effects VALUES(2002,nextval('temporary_cli.attempts')::text)", nil, append([]string{"query", "--file", "-", "--allow-write"}, target...)...)
	require.NotZero(t, exit)
	var observed observation
	select {
	case observed = <-finished:
	case <-time.After(9 * time.Second):
		t.Fatal("commit proxy did not join")
	}
	require.NoError(t, observed.err, "commit interruption was not reached")
	require.EqualValues(t, 1, commits.Load())
	require.EqualValues(t, 1, dials.Load())
	require.Equal(t, "unknown", doc["transaction_outcome"])
	require.Eventually(t, func() bool {
		var active int
		err := f.observer.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE usename='pgsavvy_temporary' AND client_port=$1", observed.port).Scan(&active)
		return err == nil && active == 0
	}, 5*time.Second, 10*time.Millisecond, "interrupted backend remained live")
	rows := f.count(t, 2002)
	require.Contains(t, []int{0, 1}, rows)
	require.Equal(t, before+1, f.attempts(t), "interrupted SQL replayed")
	t.Logf("interrupted COMMIT attempts=1, reported=unknown, eventual observer rows=%d (not prescribed)", rows)
}
