//go:build linux || darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The adapter follows the existing owned-PTY fixture: a new session and an
// owned controlling slave, separate SQL stdin and captured JSON stdout.
const machineFullPTY = `
import fcntl, json, os, pty, select, signal, subprocess, sys, termios, time
binary, mode, root = sys.argv[1:]
signal.signal(signal.SIGHUP, signal.SIG_IGN)
master, slave = pty.openpty()
before = termios.tcgetattr(slave)
flags = fcntl.fcntl(slave, fcntl.F_GETFL)
os.setsid()
fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
children = []
def start(extra=(), alternate=False):
    global phase
    phase = 'start'
    env = os.environ.copy()
    if alternate:
        env['XDG_STATE_HOME'] = root + '/different-state'
        env['TMPDIR'] = root + '/different-tmp'
        os.mkdir(env['TMPDIR'])
    p = subprocess.Popen([binary, 'query', '--connection=Exact.Profile', '--file=-', '--timeout=' + ('600ms' if mode == 'deadline' else '4s'), *extra], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, start_new_session=bool(extra))
    children.append(p)
    p.stdin.write(b'UPDATE approval_target SET value=1'); p.stdin.close(); p.stdin = None
    return p
def prompt(text):
    global phase
    phase = 'prompt'
    output = b''; end = time.monotonic() + 3
    while text not in output:
        remaining = end - time.monotonic()
        if remaining <= 0 or not select.select([master], [], [], remaining)[0]:
            raise RuntimeError('approval prompt unavailable')
        output += os.read(master, 8192)
def finish(p):
    global phase
    phase = 'completion'
    out, err = p.communicate(timeout=6)
    return {'exit':p.returncode, 'stdout':out.decode(), 'stderr':err.decode()}
try:
    first = start()
    prompt(b'Type the exact connection name: ')
    results = []
    if mode == 'busy':
        second = start(alternate=True)
        results.append(finish(second))
        explicit = [start(('--approve','--confirm-connection=Exact.Profile')) for _ in range(2)]
        results.extend(finish(p) for p in explicit)
    phase = 'prompt'
    if mode == 'signal':
        first.send_signal(signal.SIGINT)
    elif mode != 'deadline':
        os.write(master, b'wrong\n' if mode == 'mismatch' else b'Exact.Profile\n')
        if mode != 'mismatch':
            prompt(b'Type yes to approve this invocation: ')
            os.write(master, b'no\n' if mode in ('refusal','busy') else b'yes\n')
    results.append(finish(first))
    phase = 'terminal-state'
    if termios.tcgetattr(slave) != before or fcntl.fcntl(slave, fcntl.F_GETFL) != flags:
        raise RuntimeError('borrowed terminal flags changed')
    print(json.dumps(results))
except Exception:
    print('PGSAVVY_APPROVAL_PHASE=' + phase, file=sys.stderr)
    raise
finally:
    for p in children:
        if p.poll() is None: p.kill(); p.wait()
    os.close(master); os.close(slave)
`

func machineFullProfile(t *testing.T, root, endpoint string) string {
	t.Helper()
	dir := filepath.Join(root, "config", "pgsavvy")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	marker := filepath.Join(root, "credential-attempts")
	profile := fmt.Sprintf("connections:\n - name: Exact.Profile\n   driver: postgres\n   dsn: postgres://fixture@%s/fixture?sslmode=disable\n   cli_access: full\n   read_only: false\n   confirm_writes: true\n   confirm_ddl: true\n   password_command: %q\n", endpoint, "printf x >> '"+marker+"'; printf fixture")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections.yml"), []byte(profile), 0o600))
	return marker
}

func TestMachineCLIFullApprovalProcess(t *testing.T) {
	lockDir := "/tmp"
	if runtime.GOOS == "darwin" {
		lockDir = "/private/tmp"
	}
	lockPath := filepath.Join(lockDir, fmt.Sprintf("pgsavvy-approval-%d.lock", os.Geteuid()))
	var lockInode os.FileInfo
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var dials atomic.Int32
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-joined })
	for _, mode := range []string{"no-tty", "flag-mismatch", "mismatch", "refusal", "signal", "deadline", "approved", "busy"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			marker := machineFullProfile(t, root, listener.Addr().String())
			before := dials.Load()
			type result struct {
				Exit           int
				Stdout, Stderr string
			}
			var results []result
			if mode == "no-tty" || mode == "flag-mismatch" {
				args := []string{"query", "--connection=Exact.Profile", "--file=-"}
				if mode == "flag-mismatch" {
					args = append(args, "--approve", "--confirm-connection=exact.profile")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, machineTestBinary, args...)
				command.Env = machineTestEnv(root)
				command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
				command.Stdin = bytes.NewBufferString("UPDATE approval_target SET value=1")
				var out, diagnostic bytes.Buffer
				command.Stdout, command.Stderr = &out, &diagnostic
				err := command.Run()
				var exit *exec.ExitError
				require.ErrorAs(t, err, &exit)
				results = append(results, result{exit.ExitCode(), out.String(), diagnostic.String()})
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "python3", "-c", machineFullPTY, machineTestBinary, mode, root)
				command.Env = machineTestEnv(root)
				var diagnostic bytes.Buffer
				command.Stderr = &diagnostic
				out, err := command.Output()
				require.NoError(t, err, diagnostic.String())
				require.NoError(t, json.Unmarshal(out, &results))
			}
			codes := map[string]string{"no-tty": "APPROVAL_UNAVAILABLE", "flag-mismatch": "APPROVAL_MISMATCH", "mismatch": "APPROVAL_MISMATCH", "refusal": "APPROVAL_CANCELLED", "signal": "TIMEOUT", "deadline": "TIMEOUT", "approved": "CONNECTION_UNAVAILABLE", "busy": "APPROVAL_CANCELLED"}
			for i, result := range results {
				code, exit := codes[mode], 4
				if mode == "signal" || mode == "deadline" {
					exit = 6
				}
				if mode == "approved" || mode == "busy" && (i == 1 || i == 2) {
					code, exit = "CONNECTION_UNAVAILABLE", 3
				}
				if mode == "busy" && i == 0 {
					code = "APPROVAL_UNAVAILABLE"
				}
				require.Equal(t, exit, result.Exit, result.Stdout)
				require.LessOrEqual(t, len(result.Stdout), 1<<20)
				doc := machineTestDocument(t, []byte(result.Stdout), exit)
				require.Equal(t, code, doc["error"].(map[string]any)["code"])
				require.Equal(t, "not_started", doc["execution"].(map[string]any)["status"])
				require.NotContains(t, result.Stderr, "approval_target")
				if result.Stderr != "" {
					var safe map[string]any
					require.NoError(t, json.Unmarshal([]byte(result.Stderr), &safe))
					require.Len(t, safe, 2)
					require.Equal(t, float64(exit), safe["exit"])
					require.Equal(t, doc["command"], safe["command"])
				}
			}
			attempts := 0
			if mode == "approved" {
				attempts = 1
			}
			if mode == "busy" {
				attempts = 2
			}
			data, err := os.ReadFile(marker)
			if attempts == 0 {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
				require.Len(t, data, attempts)
			}
			require.EqualValues(t, attempts, dials.Load()-before, "approval rejection reached network or approval replayed")
			info, err := os.Lstat(lockPath)
			require.NoError(t, err)
			if lockInode != nil {
				require.True(t, os.SameFile(lockInode, info), "production lock inode must never be replaced")
			}
			lockInode = info
		})
	}
}

func TestMachineCLIFullPostgresTerminal(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("native PostgreSQL terminal proof requires designated PGSAVVY_TEST_PG; not unit evidence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "designated fixture unreachable")
	defer func() { _ = admin.Close(context.Background()) }()
	name := fmt.Sprintf("full_terminal_%d_%d", os.Getpid(), time.Now().UnixNano())
	id := pgx.Identifier{name}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE ROLE "+id+" LOGIN PASSWORD 'disposable-terminal-test'; CREATE SCHEMA "+id+" AUTHORIZATION "+id)
	require.NoError(t, err)
	defer func() {
		_, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+id+" CASCADE; DROP ROLE "+id)
		require.NoError(t, err)
	}()
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	u.User = url.UserPassword(name, "disposable-terminal-test")
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	observer, err := pgx.Connect(ctx, u.String())
	require.NoError(t, err)
	defer func() { _ = observer.Close(context.Background()) }()
	_, err = observer.Exec(ctx, "CREATE TABLE approval_target(value int); INSERT INTO approval_target VALUES(0)")
	require.NoError(t, err)
	root := t.TempDir()
	dir := filepath.Join(root, "config", "pgsavvy")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	profile, err := yaml.Marshal(struct {
		Connections []models.Connection `yaml:"connections"`
	}{[]models.Connection{{Name: "Exact.Profile", Driver: "postgres", DSN: u.String(), CLIAccess: models.CLIAccessFull, ConfirmWrites: true}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "connections.yml"), profile, 0o600))
	command := exec.CommandContext(ctx, "python3", "-c", machineFullPTY, machineTestBinary, "approved", root)
	command.Env = machineTestEnv(root)
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	out, err := command.Output()
	require.NoError(t, err, diagnostic.String())
	var results []struct {
		Exit   int
		Stdout string
	}
	require.NoError(t, json.Unmarshal(out, &results))
	require.Len(t, results, 1)
	require.Zero(t, results[0].Exit)
	doc := machineTestDocument(t, []byte(results[0].Stdout), 0)
	require.Equal(t, "completed", doc["execution"].(map[string]any)["status"])
	var value int
	require.NoError(t, observer.QueryRow(ctx, "SELECT value FROM approval_target").Scan(&value))
	require.Equal(t, 1, value, "independent restricted observer must see approved SQL stdin committed")
}
