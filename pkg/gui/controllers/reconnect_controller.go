package controllers

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/keys"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
	"github.com/davesavic/dbsavvy/pkg/models"
)

// reconnectToastTTL is the lifetime of toasts the ReconnectController surfaces.
const reconnectToastTTL = 4 * time.Second

// ReconnectController owns the <leader>R GLOBAL-scope reconnect binding
// (hq5.7). When the session is disconnected it probes the wire via Ping
// and, if the connection is truly dead, pushes a 3-choice dialog:
// [r] Retry, [c] Pick another connection, [q] Quit. A successful Ping
// silently clears the disconnected flag and reloads the schema rail.
//
// The schema-rail intercept (OnSchemaActivate) also triggers this flow
// when the user presses <CR> on a schema while disconnected.
type ReconnectController struct {
	baseController
	reconnecting atomic.Bool // debounce concurrent reconnect attempts
}

// NewReconnectController constructs the controller.
func NewReconnectController(
	c *common.Common,
	core CoreDeps,
	nav NavDeps,
	ui UIDeps,
	query QueryDeps,
	threading ThreadingDeps,
	edit EditDeps,
) *ReconnectController {
	return &ReconnectController{
		baseController: newBase(c, HelperBag{
			CoreDeps:      core,
			NavDeps:       nav,
			UIDeps:        ui,
			QueryDeps:     query,
			ThreadingDeps: threading,
			EditDeps:      edit,
		}),
	}
}

// Reconnect is the <leader>R handler. If the session is not disconnected
// it toasts "already connected" and returns. Otherwise it attempts a Ping;
// on success it clears the disconnected flag + reloads the rail; on
// failure it shows the reconnect dialog.
func (rc *ReconnectController) Reconnect(_ commands.ExecCtx) error {
	runner := rc.helpers.QueryRunner
	if runner == nil || !runner.IsDisconnected() {
		rc.toast("already connected")
		return nil
	}
	return rc.tryReconnect()
}

// tryReconnect is the shared entry point for both <leader>R and the
// schema-rail intercept. It attempts a Ping on a worker goroutine; on
// success it clears the flag and reloads, on failure it shows the dialog.
func (rc *ReconnectController) tryReconnect() error {
	if rc.helpers.Reconnector == nil {
		rc.toast("reconnect not available")
		return nil
	}
	if rc.helpers.OnWorker == nil {
		return rc.doPingAndHandle()
	}
	rc.helpers.OnWorker(func(_ gocui.Task) error {
		return rc.doPingAndHandle()
	})
	return nil
}

func (rc *ReconnectController) doPingAndHandle() error {
	err := rc.helpers.Reconnector.PingConnection(context.Background())
	if err != nil {
		// Connection is dead — show the dialog on the UI thread.
		if rc.helpers.OnUIThread != nil {
			rc.helpers.OnUIThread(func() error {
				return rc.showReconnectDialog("")
			})
			return nil
		}
		return rc.showReconnectDialog("")
	}
	// Server is reachable, but the SQLSession's inner pgx conn is still
	// dead — Ping only verifies the pool can talk to the server, it does
	// not heal the checked-out conn the session owns. Perform the full
	// teardown+reopen via Reconnect so subsequent queries land on a live
	// session. dbsavvy-txb.
	profile := rc.activeProfile()
	if profile == nil {
		rc.toast("no active connection profile")
		return nil
	}
	return rc.doReconnect(profile)
}

// showReconnectDialog pushes the 3-choice reconnect dialog. errorPrefix
// is prepended to the body when a prior retry failed, showing the error.
func (rc *ReconnectController) showReconnectDialog(errorPrefix string) error {
	if rc.helpers.Choice == nil {
		return nil
	}

	var label string
	if errorPrefix != "" {
		label = fmt.Sprintf("retry failed: %s\n\n", errorPrefix)
	}
	label += "connection lost\n\n" +
		"The server closed the connection.\n\n" +
		"Server-side state is gone:\n" +
		"  - any open transaction has been rolled back\n" +
		"  - temp tables and session settings were discarded\n\n" +
		"Client-side state preserved:\n" +
		"  - all scratch buffers, pending edits, history"

	choices := []string{
		"[r] Retry",
		"[c] Pick another connection",
		"[q] Quit",
	}

	return rc.helpers.Choice.Choose(label, choices, func(idx int) error {
		switch idx {
		case 0: // retry
			return rc.handleRetry()
		case 1: // pick another connection
			return rc.handlePickConnection()
		case 2: // quit
			return rc.handleQuit()
		}
		return nil
	}, func() error {
		// Esc — dismiss (stay disconnected).
		return nil
	})
}

// handleRetry tears down and reconnects with the same profile on a
// worker goroutine. Debounced via the reconnecting flag.
func (rc *ReconnectController) handleRetry() error {
	if !rc.reconnecting.CompareAndSwap(false, true) {
		rc.toast("reconnect already in progress")
		return nil
	}

	profile := rc.activeProfile()
	if profile == nil {
		rc.reconnecting.Store(false)
		rc.toast("no active connection profile")
		return nil
	}

	if rc.helpers.OnWorker == nil {
		defer rc.reconnecting.Store(false)
		return rc.doReconnect(profile)
	}

	rc.helpers.OnWorker(func(_ gocui.Task) error {
		defer rc.reconnecting.Store(false)
		return rc.doReconnect(profile)
	})
	return nil
}

func (rc *ReconnectController) doReconnect(profile *models.Connection) error {
	err := rc.helpers.Reconnector.Reconnect(context.Background(), profile)
	if err != nil {
		// Re-show dialog with error prepended.
		if rc.helpers.OnUIThread != nil {
			rc.helpers.OnUIThread(func() error {
				return rc.showReconnectDialog(err.Error())
			})
			return nil
		}
		return rc.showReconnectDialog(err.Error())
	}
	// Success — clear flag, reload, toast.
	rc.clearDisconnected()
	rc.toast(fmt.Sprintf("reconnected to %s", profile.Name))
	rc.refreshSchemas()
	return nil
}

// handleQuit checks the pending-edit guard before quitting. Open-tx
// check is skipped since the connection is dead.
func (rc *ReconnectController) handleQuit() error {
	if pd := rc.helpers.PendingDiscard; pd != nil {
		if err := pd.BlockQuitIfPending(); err != nil {
			rc.toast(err.Error())
			return nil
		}
	}
	return gocui.ErrQuit
}

// handlePickConnection pushes the CONNECTIONS context so the user can
// pick a different profile. Delegates to the OnPickConnection callback
// the orchestrator wires to tree.Push(registry.Connections).
func (rc *ReconnectController) handlePickConnection() error {
	if rc.helpers.OnPickConnection == nil {
		return nil
	}
	return rc.helpers.OnPickConnection()
}

// clearDisconnected clears the disconnected flag on the underlying session.
func (rc *ReconnectController) clearDisconnected() {
	runner := rc.helpers.QueryRunner
	if runner == nil {
		return
	}
	runner.ClearDisconnected()
}

// refreshSchemas reloads the schema rail. Nil-safe.
func (rc *ReconnectController) refreshSchemas() {
	if rc.helpers.Refresh == nil {
		return
	}
	_ = rc.helpers.Refresh.RefreshSchemas(context.Background())
}

// activeProfile returns the current connection profile or nil.
func (rc *ReconnectController) activeProfile() *models.Connection {
	if rc.helpers.ActiveConnectionProfile == nil {
		return nil
	}
	return rc.helpers.ActiveConnectionProfile()
}

// toast shows a transient message.
func (rc *ReconnectController) toast(msg string) {
	if rc.helpers.Toast == nil {
		return
	}
	rc.helpers.Toast.Show(msg, reconnectToastTTL)
}

// GetKeybindings publishes the <leader>R binding under GLOBAL scope.
func (rc *ReconnectController) GetKeybindings(_ types.KeybindingsOpts) []*types.ChordBinding {
	tr := rc.tr()
	seq, err := keys.SequenceFromShorthand("<leader>R")
	if err != nil {
		return nil
	}
	return []*types.ChordBinding{
		{
			Sequence:    seq,
			Mode:        types.ModeNormal,
			Scope:       types.GLOBAL,
			ActionID:    commands.Reconnect,
			Description: tr.Actions.Reconnect,
		},
	}
}

// RegisterActions registers the reconnect action.
func (rc *ReconnectController) RegisterActions(reg *commands.Registry) {
	if reg == nil {
		return
	}
	_ = reg.Register(&commands.Command{
		ID:          commands.Reconnect,
		Description: "Reconnect to server",
		Tag:         "Connection",
		Handler:     rc.Reconnect,
	})
}

// AttachToContext registers GetKeybindings on the GLOBAL context.
func (rc *ReconnectController) AttachToContext(ctx attachable) {
	if ctx == nil {
		return
	}
	ctx.AddKeybindingsFn(rc.GetKeybindings)
}
