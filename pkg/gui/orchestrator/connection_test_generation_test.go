package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers"
	guicontext "github.com/davesavic/pgsavvy/pkg/gui/context"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/gui/internal/testfake"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/stretchr/testify/require"
)

type formProbeQueue struct {
	*testfake.RecorderGuiDriver
	mu    sync.Mutex
	queue []func() error
	ready chan struct{}
}

func (q *formProbeQueue) Update(fn func() error) {
	q.mu.Lock()
	q.queue = append(q.queue, fn)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *formProbeQueue) drain(t *testing.T) {
	t.Helper()
	q.mu.Lock()
	fns := q.queue
	q.queue = nil
	q.mu.Unlock()
	for _, fn := range fns {
		require.NoError(t, fn())
	}
}

type formProbeDriver struct {
	drivers.Driver
	open func(context.Context) (drivers.Connection, error)
}

func (d formProbeDriver) Open(ctx context.Context, _ drivers.ConnectionProfile, _ drivers.ProgressReporter) (drivers.Connection, error) {
	return d.open(ctx)
}

type formProbeConnection struct{ drivers.Connection }

func (formProbeConnection) Close() error { return nil }

var formProbeDriverSequence atomic.Uint64

func newFormProbeHarness(t *testing.T, open func(context.Context) (drivers.Connection, error)) (*Gui, *connectInvoker, *guicontext.ConnectionManagerContext, *formProbeQueue, *bytes.Buffer) {
	t.Helper()
	q := &formProbeQueue{RecorderGuiDriver: testfake.NewRecorderGuiDriver(), ready: make(chan struct{}, 8)}
	base := guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.CONNECTION_MANAGER, ViewName: string(types.CONNECTION_MANAGER)})
	cm := guicontext.NewConnectionManagerContext(base, types.ContextTreeDeps{GuiDriver: q})
	_, err := q.SetView(string(types.CONNECTION_MANAGER), 0, 0, 100, 40, 0)
	require.ErrorIs(t, err, gocui.ErrUnknownView)
	var sink bytes.Buffer
	g := &Gui{deps: Deps{Common: common.NewCommon(slog.New(slog.NewTextHandler(&sink, nil)), i18n.EnglishTranslationSet(), config.GetDefaultConfig(), nil, nil)}, driver: q, registry: &guicontext.ContextTree{ConnectionManager: cm}}
	g.spinnerState.workersWG = newWorkerGroup()
	c := &connectInvoker{g: g}
	name := fmt.Sprintf("form-probe-%d", formProbeDriverSequence.Add(1))
	drivers.Register(name, func(context.Context) (drivers.Driver, error) { return formProbeDriver{open: open}, nil })
	cm.OpenEditForm(models.Connection{Name: "origin", Driver: name, Host: "db"}, nil, nil)
	return g, c, cm, q, &sink
}

func waitFormProbeWorkers(t *testing.T, g *Gui) {
	t.Helper()
	done := make(chan struct{})
	go func() { g.spinnerState.workersWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("probe worker did not settle")
	}
}

func TestReview_form_probe(t *testing.T) {
	for _, timing := range []string{"in-flight", "queued"} {
		for _, outcome := range []string{"success", "failure"} {
			for _, action := range []string{"cancel reopen", "replace add", "replace edit", "text edit", "toggle edit", "paste edit", "save", "invalid save", "exit", "shutdown"} {
				t.Run(timing+"/"+outcome+"/"+action, func(t *testing.T) {
					started := make(chan context.Context, 1)
					gate := make(chan struct{})
					var release sync.Once
					g, inv, cm, q, _ := newFormProbeHarness(t, func(ctx context.Context) (drivers.Connection, error) {
						started <- ctx
						if action == "shutdown" {
							select {
							case <-ctx.Done():
								return nil, ctx.Err()
							case <-gate:
							}
						} else {
							<-gate
						}
						if outcome == "failure" {
							return nil, errors.New("origin probe failed")
						}
						return formProbeConnection{}, nil
					})
					t.Cleanup(func() { release.Do(func() { close(gate) }); waitFormProbeWorkers(t, g) })
					if action == "invalid save" {
						cm.FormSetFocusedValue("")
					}
					profile := cm.FormConnection()
					inv.testConnection(&profile)
					var probeCtx context.Context
					select {
					case probeCtx = <-started:
					case <-time.After(3 * time.Second):
						t.Fatal("dial not started")
					}
					if timing == "queued" {
						release.Do(func() { close(gate) })
						waitFormProbeWorkers(t, g)
					}
					switch action {
					case "cancel reopen":
						cm.SetMode(guicontext.ModeList)
						cm.OpenEditForm(profile, nil, nil)
					case "replace add":
						cm.OpenAddForm(nil, nil)
					case "replace edit":
						cm.OpenEditForm(profile, nil, nil)
					case "text edit":
						cm.FormSetFocusedValue("edited")
					case "toggle edit":
						for range 7 {
							cm.FormMoveFocus(1)
						}
						cm.FormToggleFocused()
					case "paste edit":
						_, ok := cm.FormApplyDSN("postgres://u@edited-db/db")
						require.True(t, ok)
					case "save", "invalid save":
						_, _, _, ok := cm.FormValidateAll(i18n.EnglishTranslationSet())
						require.Equal(t, action == "save", ok)
					case "exit":
						cm.SetMode(guicontext.ModeList)
					case "shutdown":
						require.NoError(t, g.Close())
					}
					if timing == "in-flight" && probeCtx.Err() == nil {
						t.Error("form invalidation did not cancel originating probe")
					}
					cm.FormSetStatus("current form status")
					release.Do(func() { close(gate) })
					waitFormProbeWorkers(t, g)
					q.drain(t)
					require.NoError(t, cm.HandleRender())
					body := q.GetViewBuffer(string(types.CONNECTION_MANAGER))
					if cm.Mode() == guicontext.ModeForm {
						require.Contains(t, body, "current form status")
						require.NotContains(t, body, "connection ok")
						require.NotContains(t, body, "origin probe failed")
					}
				})
			}
		}
	}
}

func TestReview_form_probe_cancel_ownership(t *testing.T) {
	started := make(chan context.Context, 2)
	first, second := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	var releaseFirst, releaseSecond sync.Once
	g, inv, cm, q, _ := newFormProbeHarness(t, func(ctx context.Context) (drivers.Connection, error) {
		n := calls.Add(1)
		started <- ctx
		if n == 1 {
			<-first
		} else {
			<-second
		}
		return formProbeConnection{}, nil
	})
	t.Cleanup(func() {
		releaseFirst.Do(func() { close(first) })
		releaseSecond.Do(func() { close(second) })
		waitFormProbeWorkers(t, g)
	})
	profile := cm.FormConnection()
	inv.testConnection(&profile)
	ctxA := <-started
	inv.testConnection(&profile)
	ctxB := <-started
	require.Error(t, ctxA.Err())
	releaseFirst.Do(func() { close(first) })
	// Busy decrement is sequenced after A's deferred ownership release.
	deadline := time.After(3 * time.Second)
	for g.BusyCount() != 1 {
		select {
		case <-deadline:
			t.Fatal("A did not settle")
		default:
			runtime.Gosched()
		}
	}
	cm.SetMode(guicontext.ModeList)
	require.Error(t, ctxB.Err(), "A completion must not clear B cancellation ownership")
	releaseSecond.Do(func() { close(second) })
	waitFormProbeWorkers(t, g)
	q.drain(t)
}

func TestReview_form_probe_logs(t *testing.T) {
	g, inv, cm, q, sink := newFormProbeHarness(t, func(context.Context) (drivers.Connection, error) {
		return nil, errors.New("failed postgres://user:probe-secret@host/db")
	})
	profile := cm.FormConnection()
	inv.testConnection(&profile)
	waitFormProbeWorkers(t, g)
	q.drain(t)
	require.NoError(t, cm.HandleRender())
	require.False(t, strings.Contains(sink.String(), "probe-secret"), "probe errors leaked credentials into worker logs")
	require.Contains(t, sink.String(), "OnWorker returned error", "failure visibility lost")
	require.NotContains(t, q.GetViewBuffer(string(types.CONNECTION_MANAGER)), "probe-secret")
	require.Contains(t, q.GetViewBuffer(string(types.CONNECTION_MANAGER)), "test failed:")
}

func TestReview_form_probe_settles_once(t *testing.T) {
	g, inv, cm, q, _ := newFormProbeHarness(t, func(context.Context) (drivers.Connection, error) { return formProbeConnection{}, nil })
	profile := cm.FormConnection()
	inv.testConnection(&profile)
	waitFormProbeWorkers(t, g)
	q.mu.Lock()
	fns := q.queue
	q.queue = nil
	q.mu.Unlock()
	require.Len(t, fns, 1)
	require.NoError(t, fns[0]())
	require.NoError(t, cm.HandleRender())
	require.Contains(t, q.GetViewBuffer(string(types.CONNECTION_MANAGER)), "connection ok")
	cm.FormSetStatus("settled marker")
	require.NoError(t, fns[0]())
	require.NoError(t, cm.HandleRender())
	require.Contains(t, q.GetViewBuffer(string(types.CONNECTION_MANAGER)), "settled marker")
}

func TestReview_form_probe_credential_esc(t *testing.T) {
	var secret *ui.SecretPromptHelper
	g, inv, cm, q, sink := newFormProbeHarness(t, func(ctx context.Context) (drivers.Connection, error) {
		_, err := secret.PromptSecret(ctx, "SSH Password")
		return nil, fmt.Errorf("ssh password auth: prompt: %w", err)
	})
	prompt := ui.NewPromptHelper(nil, nil)
	mask := guicontext.NewPromptContext(guicontext.NewBaseContext(guicontext.BaseContextOpts{Key: types.PROMPT}), types.ContextTreeDeps{})
	secret = ui.NewSecretPromptHelper(prompt, mask, q.Update)
	profile := cm.FormConnection()
	revision := cm.FormRevision()
	cm.FormSetStatus("previous probe status")
	inv.testConnection(&profile)
	select {
	case <-q.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("credential prompt was not queued")
	}
	q.drain(t)
	require.True(t, prompt.Active())
	require.NoError(t, prompt.Cancel())
	waitFormProbeWorkers(t, g)
	q.drain(t)
	require.True(t, cm.FormMatches(revision), "credential Esc must keep originating form identity")
	require.NoError(t, cm.HandleRender())
	body := q.GetViewBuffer(string(types.CONNECTION_MANAGER))
	require.NotContains(t, body, "test failed:", "credential cancellation must not be an authentication failure")
	require.NotContains(t, body, "connection ok")
	require.NotContains(t, body, "previous probe status")
	require.NotContains(t, sink.String(), "OnWorker returned error")
}
