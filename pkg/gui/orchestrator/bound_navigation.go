package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func (g *Gui) runBoundSort(sql string, timeout time.Duration) bool {
	if g.closed.Load() || g.resultTabsH == nil || g.queryState.queryRunner == nil {
		return false
	}
	target := g.resultTabsH.CaptureNavigation()
	binding := g.queryState.queryRunner.CaptureQueryBinding()
	if target == nil || !binding.Current() {
		return false
	}
	original, args, schema := target.Origin()
	binding.RunAsync(target.Context(), models.Query{SQL: sql, Args: args, DefaultSchema: schema, Timeout: timeout}, target.Current, target.Preempt, func(result *data.BoundQueryResult, err error) {
		g.PublishUIContent(target.Context(), func() error {
			if err != nil {
				g.navigationError(target.Current(), err)
				return nil
			}
			result.Accept(func(handle *session.RunHandle) bool { return target.AdoptSort(handle, sql, original) })
			return nil
		}, func(error) { result.Discard() })
	})
	return true
}

func (g *Gui) navigationError(current bool, err error) {
	if current && err != nil && !errors.Is(err, context.Canceled) && g.toastHelp != nil {
		g.toastHelp.Show(err.Error(), 4*time.Second)
	}
}

func (g *Gui) runBoundForwardFK(_ context.Context, origin helpers.CurrentTab, row, col int) error {
	if g.closed.Load() || g.resultTabsH == nil || g.queryState.queryRunner == nil || g.queryState.activeSQLSession == nil {
		return errors.New("fk forward: no active connection")
	}
	active := g.resultTabsH.Active()
	if origin == nil || active == nil || origin.ID() != active.ID() {
		return context.Canceled
	}
	target := g.resultTabsH.CaptureNavigation()
	binding := g.queryState.queryRunner.CaptureQueryBinding()
	if target == nil || !binding.Current() {
		return context.Canceled
	}
	cache := g.queryState.activeSQLSession.FKCache()
	if cache == nil {
		return errors.New("fk forward: metadata cache unavailable")
	}
	schema, table := origin.BaseTable()
	cols := append([]string(nil), origin.ColumnNames()...)
	values, loaded := origin.RowValues(row)
	if table == "" || !loaded {
		return helpers.ErrRowNotLoaded
	}
	values = append([]any(nil), values...)
	connID, _ := active.Identity()
	jump := ui.JumpEntry{TabSlot: origin.Slot(), TabID: fmt.Sprint(origin.ID()), Row: row, Col: col, At: time.Now()}
	limit, timeout := helpers.DefaultFKForwardLimit, time.Duration(0)
	if cfg := g.deps.Common.UserConfig.Load(); cfg != nil {
		if cfg.Editor.FKForwardLimit > 0 {
			limit = cfg.Editor.FKForwardLimit
		}
		if cfg.Query.DefaultStatementTimeout != nil {
			timeout = *cfg.Query.DefaultStatementTimeout
		}
	}
	var q models.Query
	var label, notice string
	binding.WorkAsync(target.Context(), target.Current, target.Preempt, func(context.Context) error {
		ctx, cancel := context.WithTimeout(target.Context(), relationshipPreviewTimeout)
		fks, err := cache.Get(ctx, schema, table)
		cancel()
		if err != nil {
			return err
		}
		q, label, notice, err = helpers.PrepareForwardFK(fks, cols, values, col, limit)
		if err != nil {
			return err
		}
		q.Timeout = timeout
		return nil
	}, func(err error) {
		if err != nil {
			g.publishNavigationError(target, err)
			return
		}
		// Acknowledgment commits history on UI before dispatching the SELECT,
		// without making the worker or MainLoop wait for the other.
		var historyAccepted atomic.Bool
		g.PublishUIContent(target.Context(), func() error {
			if !target.Current() || !binding.Current() || g.jumpListH == nil {
				return nil
			}
			g.jumpListH.Push(jump)
			if notice != "" && g.toastHelp != nil {
				g.toastHelp.Show(notice, 4*time.Second)
			}
			historyAccepted.Store(true)
			return nil
		}, func(err error) {
			if err != nil || !historyAccepted.Load() {
				return
			}
			binding.RunAsync(target.Context(), q, target.Current, func() bool { return false }, func(result *data.BoundQueryResult, err error) {
				g.PublishUIContent(target.Context(), func() error {
					if err != nil {
						g.navigationError(target.Current(), err)
						return nil
					}
					result.Accept(func(handle *session.RunHandle) bool {
						if err := g.resultTabsH.OpenResultTab(label, handle); err != nil {
							g.navigationError(true, err)
							return false
						}
						g.resultTabsH.AttachActiveTabIdentity(connID, query.DetectFromQuery(q.SQL))
						g.resultTabsH.AttachActiveTabOrigin(q.SQL, q.Args, q.DefaultSchema)
						return true
					})
					return nil
				}, func(error) { result.Discard() })
			})
		})
	})
	return nil
}

func (g *Gui) publishNavigationError(target *ui.NavigationTarget, err error) {
	g.PublishUIContent(target.Context(), func() error {
		g.navigationError(target.Current(), err)
		return nil
	}, func(error) {})
}
