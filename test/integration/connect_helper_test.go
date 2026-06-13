//go:build integration

package integration_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/drivers/pg"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// connectHelperOnce guards the one-shot driver registration for the ConnectHelper
// integration tests. drivers.Register panics on duplicate name; using a unique
// driver name + sync.Once keeps registration deterministic across the suite's
// shared TestMain.
var (
	connectHelperRegOnce sync.Once
	connectHelperPromptr trackingPrompter
)

const connectHelperDriverName = "postgres-connect-helper"

func registerConnectHelperDriver(t *testing.T) {
	t.Helper()
	connectHelperRegOnce.Do(func() {
		// Reset the prompter's invocation counter on first registration.
		drivers.Register(connectHelperDriverName, pg.New(&connectHelperPromptr))
	})
}

// trackingPrompter wraps session.TUIRefusePrompter so the test can verify the
// prompter was (or was not) invoked. PromptPassword still returns the
// errInteractivePromptNotSupported sentinel via the embedded type.
type trackingPrompter struct {
	mu    sync.Mutex
	calls int
}

func (p *trackingPrompter) PromptPassword(ctx context.Context, hint string) (string, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return session.TUIRefusePrompter{}.PromptPassword(ctx, hint)
}

func (p *trackingPrompter) reset() {
	p.mu.Lock()
	p.calls = 0
	p.mu.Unlock()
}

func (p *trackingPrompter) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestConnectHelperPasswordCommandOpens asserts that a profile carrying a
// password_command (and no inline / keyring / pgpass) opens the live Postgres
// fixture via ConnectHelper.Connect even when the driver was registered with a
// refusing TUI-mode prompter. The prompter MUST NOT be invoked — the
// password_command step satisfies the waterfall first.
func TestConnectHelperPasswordCommandOpens(t *testing.T) {
	requirePG(t)
	registerConnectHelperDriver(t)

	envDSNRaw := os.Getenv(envDSN)
	password := dsnPassword(t, envDSNRaw)
	isolateEnv(t)
	connectHelperPromptr.reset()

	profile := &models.Connection{
		Name:            "ch-password-command",
		Driver:          connectHelperDriverName,
		DSN:             dsnWithoutPassword(t, envDSNRaw),
		PasswordCommand: "printf secret",
	}
	// The command emits "secret" — but the FIXTURE password is whatever the
	// DSN carries. Swap the command to printf the real password so the
	// downstream pg.Open call succeeds. We retain the printf form per M07d
	// (no $PASSWORD env dance).
	profile.PasswordCommand = "printf " + shellQuote(password)

	h := data.NewConnectHelper()
	conn, sess, err := h.Connect(context.Background(), profile, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(h.Disconnect)
	if conn == nil || sess == nil {
		t.Fatalf("Connect returned nil conn/sess: conn=%v sess=%v", conn, sess)
	}
	if err := conn.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if got := connectHelperPromptr.Calls(); got != 0 {
		t.Fatalf("prompter invoked %d times — password_command should have satisfied the waterfall", got)
	}

	// Smoke: LoadSchemas through the queue works against the live fixture.
	schemas, err := h.LoadSchemas(context.Background(), "")
	if err != nil {
		t.Fatalf("LoadSchemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("LoadSchemas returned empty slice against live fixture")
	}
}

// TestConnectHelperNoCredsRefuses asserts that a profile with no
// Password / PasswordCommand / KeyringRef / PgpassPath fails at
// ConnectHelper.Connect with an error detectable via
// session.IsInteractivePromptUnsupported (the TUIRefusePrompter sentinel).
func TestConnectHelperNoCredsRefuses(t *testing.T) {
	requirePG(t)
	registerConnectHelperDriver(t)

	envDSNRaw := os.Getenv(envDSN)
	isolateEnv(t)
	connectHelperPromptr.reset()

	profile := &models.Connection{
		Name:   "ch-no-creds",
		Driver: connectHelperDriverName,
		DSN:    dsnWithoutPassword(t, envDSNRaw),
	}

	h := data.NewConnectHelper()
	_, _, err := h.Connect(context.Background(), profile, nil)
	if err == nil {
		t.Fatal("expected error from Connect with no credentials")
	}
	if !session.IsInteractivePromptUnsupported(err) {
		t.Fatalf("expected IsInteractivePromptUnsupported(err)==true, got err=%v", err)
	}
	if connectHelperPromptr.Calls() == 0 {
		t.Fatal("expected prompter to be invoked at least once (waterfall reached final step)")
	}
}

// shellQuote escapes a single value for `printf <arg>` so passwords containing
// spaces / shell metas pass through intact. We deliberately avoid `printf %s`
// + a separate format string here because the integration probe's password
// rarely needs more than basic quoting; if it does the test will surface a
// pg connect failure rather than silently miscompare.
func shellQuote(s string) string {
	// POSIX-safe single-quote escape: close, escape, reopen.
	out := make([]byte, 0, len(s)+2)
	out = append(out, '\'')
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	out = append(out, '\'')
	return string(out)
}
