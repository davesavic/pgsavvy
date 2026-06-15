package orchestrator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jesseduffield/lazygit/pkg/gocui"
	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/common"
	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/pgsavvy/pkg/i18n"
)

// cancelingPrompter is a ChainedPrompter that immediately cancels every
// prompt. Used to ensure that when the worker function captured by the
// fake onWorker is invoked, WalkAddConnection terminates promptly
// (cancel on the first prompt) rather than blocking the test goroutine.
type cancelingPrompter struct{}

func (cancelingPrompter) PromptString(_ context.Context, _, _ string, _ func(string) error) (string, error) {
	return "", data.PromptCanceledErr()
}

func (cancelingPrompter) PromptChoice(_ context.Context, _, _ string, _ []string) (string, error) {
	return "", data.PromptCanceledErr()
}

// newTestFormHelper builds a real *data.ConnectionFormHelper backed by
// an in-memory fs. Sufficient for verifying that WalkAdd's nil-guards
// and OnWorker dispatch behave correctly without spinning up a full
// Gui.
func newTestFormHelper(t *testing.T) *data.ConnectionFormHelper {
	t.Helper()
	fs := afero.NewMemMapFs()
	log := slog.New(slog.DiscardHandler)
	c := common.NewCommon(log, i18n.EnglishTranslationSet(), config.GetDefaultConfig(), &common.AppState{}, fs)
	return data.NewConnectionFormHelper(c, fs, "/tmp/connections.yml", func() []string { return []string{"postgres"} })
}

func TestConnectionFormInvokerWalkAddSchedulesViaOnWorker(t *testing.T) {
	helper := newTestFormHelper(t)

	var captured func(gocui.Task) error
	var calls int
	inv := &connectionFormInvoker{
		helper:   helper,
		prompter: cancelingPrompter{},
		onWorker: func(fn func(gocui.Task) error) {
			calls++
			captured = fn
		},
	}

	if err := inv.WalkAdd(context.Background()); err != nil {
		t.Fatalf("WalkAdd: got err %v, want nil", err)
	}
	if calls != 1 {
		t.Fatalf("onWorker calls = %d, want 1", calls)
	}
	if captured == nil {
		t.Fatal("onWorker did not capture a worker fn")
	}

	// Invoke the captured worker fn manually — the cancelingPrompter
	// returns PromptCanceledErr on the first prompt, which
	// WalkAddConnection treats as a clean cancel (return nil). Any
	// other return value indicates the dispatch path wired the wrong
	// prompter or context.
	if err := captured(gocui.NewFakeTask()); err != nil {
		t.Fatalf("captured worker fn: got err %v, want nil (cancel path)", err)
	}
}

func TestConnectionFormInvokerWalkAddNilGuards(t *testing.T) {
	// Nil receiver returns nil without panicking.
	var nilInv *connectionFormInvoker
	if err := nilInv.WalkAdd(context.Background()); err != nil {
		t.Fatalf("nil receiver WalkAdd: %v", err)
	}

	// Nil helper returns nil without dispatching.
	dispatchCalls := 0
	invNoHelper := &connectionFormInvoker{
		onWorker: func(_ func(gocui.Task) error) { dispatchCalls++ },
	}
	if err := invNoHelper.WalkAdd(context.Background()); err != nil {
		t.Fatalf("nil-helper WalkAdd: %v", err)
	}
	if dispatchCalls != 0 {
		t.Fatalf("nil-helper dispatched anyway (calls=%d)", dispatchCalls)
	}

	// Nil g + nil onWorker returns nil (no panic via nil g.OnWorker).
	invNoG := &connectionFormInvoker{
		helper:   newTestFormHelper(t),
		prompter: cancelingPrompter{},
	}
	if err := invNoG.WalkAdd(context.Background()); err != nil {
		t.Fatalf("nil-g WalkAdd: %v", err)
	}
}
