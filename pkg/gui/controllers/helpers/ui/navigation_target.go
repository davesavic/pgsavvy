package ui

import (
	"context"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

type NavigationTarget struct {
	helper                *ResultTabsHelper
	tab                   *Tab
	generation            uint64
	editabilityGeneration uint64
	ctx                   context.Context
	preempt               func() bool
}

func (h *ResultTabsHelper) invalidateNavigationLocked() {
	h.navigationGeneration++
	if h.navigationCancel != nil {
		h.navigationCancel()
		h.navigationCancel = nil
	}
}

func (h *ResultTabsHelper) CancelNavigation() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.invalidateNavigationLocked()
}

// CaptureNavigation binds an action to the current tab before dispatch. Tab
// switching, close and another navigation permanently invalidate this action.
func (h *ResultTabsHelper) CaptureNavigation() *NavigationTarget {
	h.mu.Lock()
	h.invalidateNavigationLocked()
	var tab *Tab
	for _, candidate := range h.tabs {
		if candidate.id == h.activeID {
			tab = candidate
			break
		}
	}
	if tab == nil {
		h.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.navigationCancel = cancel
	target := &NavigationTarget{helper: h, tab: tab, generation: h.navigationGeneration, editabilityGeneration: tab.EditabilityGeneration(), ctx: ctx}
	var targets []preemptTarget
	for _, candidate := range h.tabs {
		candidate.mu.Lock()
		if candidate.state == StateRunning || candidate.state == StateSorting {
			targets = append(targets, preemptTarget{rh: candidate.rh, runner: candidate.runner})
		}
		candidate.mu.Unlock()
	}
	h.mu.Unlock()
	target.preempt = func() bool {
		if !target.Current() {
			return false
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, stream := range targets {
				if stream.rh != nil {
					_ = stream.rh.Cancel()
				}
				if stream.runner != nil {
					stream.runner.Stop()
				}
			}
		}()
		select {
		case <-done:
			return false
		case <-time.After(preemptStopBound):
			return true
		}
	}
	return target
}

func (target *NavigationTarget) Context() context.Context {
	if target == nil {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}
	return target.ctx
}

func (target *NavigationTarget) Current() bool {
	if target == nil || target.ctx.Err() != nil {
		return false
	}
	h := target.helper
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.navigationGeneration == target.generation && h.activeID == target.tab.ID() && !target.tab.disposed.Load() && target.tab.EditabilityGeneration() == target.editabilityGeneration
}

func (target *NavigationTarget) Preempt() bool { return target.preempt() }

func (target *NavigationTarget) AdoptSort(rh *session.RunHandle, sql, original string) bool {
	if !target.Current() {
		return false
	}
	target.helper.reattachTab(target.tab, rh, sql, original)
	return true
}

func (target *NavigationTarget) Origin() (string, []any, string) { return target.tab.Origin() }

func (target *NavigationTarget) Columns() []models.ColumnMeta { return target.tab.Grid().Columns() }
