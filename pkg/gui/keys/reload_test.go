package keys

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/commands"
	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// --- Test doubles ------------------------------------------------------

type recordingToaster struct {
	mu       sync.Mutex
	messages []string
}

func (r *recordingToaster) toast(m string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, m)
}

func (r *recordingToaster) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.messages...)
}

type recordingMatcher struct {
	mu     sync.Mutex
	swaps  []*TrieSet
	called int
}

func (r *recordingMatcher) SwapTrieSet(t *TrieSet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.swaps = append(r.swaps, t)
	r.called++
}

func (r *recordingMatcher) swapCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

func (r *recordingMatcher) lastSwap() *TrieSet {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.swaps) == 0 {
		return nil
	}
	return r.swaps[len(r.swaps)-1]
}

type stubService struct {
	trie     *TrieSet
	warnings []Warning
	err      error
	panic    bool

	mu       sync.Mutex
	buildCh  chan struct{} // when non-nil, Build blocks on receive
	builds   int
}

func (s *stubService) Build(
	_ []*ChordBinding,
	_ *config.UserConfig,
	_ *commands.Registry,
	_ ContextKindLookup,
) (*TrieSet, []Warning, error) {
	s.mu.Lock()
	s.builds++
	ch := s.buildCh
	s.mu.Unlock()
	if ch != nil {
		<-ch
	}
	if s.panic {
		panic("boom")
	}
	return s.trie, s.warnings, s.err
}

func (s *stubService) buildCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.builds
}

// --- Tests -------------------------------------------------------------

func TestReloadCommand_HappyPath(t *testing.T) {
	cfg := &config.UserConfig{}
	trie := NewTrieSet()
	svc := &stubService{trie: trie}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}
	loadCount := 0

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			loadCount++
			return cfg, nil
		},
		Service: svc,
		Matcher: mat,
		Toaster: toaster.toast,
	})
	if cmd.Name != "reload" {
		t.Fatalf("Name = %q, want reload", cmd.Name)
	}
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if loadCount != 1 {
		t.Errorf("LoadUserConfig called %d times, want 1", loadCount)
	}
	if svc.buildCount() != 1 {
		t.Errorf("Build called %d times, want 1", svc.buildCount())
	}
	if mat.swapCount() != 1 {
		t.Errorf("SwapTrieSet called %d times, want 1", mat.swapCount())
	}
	if mat.lastSwap() != trie {
		t.Errorf("SwapTrieSet got wrong trie")
	}
	msgs := toaster.snapshot()
	if len(msgs) != 1 || msgs[0] != "config reloaded" {
		t.Errorf("toaster messages = %v, want [config reloaded]", msgs)
	}
}

func TestReloadCommand_LoadError(t *testing.T) {
	svc := &stubService{trie: NewTrieSet()}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			return nil, errors.New("yaml: line 7: bad")
		},
		Service: svc,
		Matcher: mat,
		Toaster: toaster.toast,
	})
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if svc.buildCount() != 0 {
		t.Errorf("Build called %d times after LoadUserConfig error, want 0", svc.buildCount())
	}
	if mat.swapCount() != 0 {
		t.Errorf("SwapTrieSet called %d times after LoadUserConfig error, want 0", mat.swapCount())
	}
	msgs := toaster.snapshot()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "reload failed") || !strings.Contains(msgs[0], "yaml: line 7: bad") {
		t.Errorf("toaster messages = %v, want one 'reload failed: yaml: line 7: bad'-style entry", msgs)
	}
}

func TestReloadCommand_BuildError(t *testing.T) {
	svc := &stubService{err: errors.New("nil registry")}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) { return &config.UserConfig{}, nil },
		Service:        svc,
		Matcher:        mat,
		Toaster:        toaster.toast,
	})
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mat.swapCount() != 0 {
		t.Errorf("SwapTrieSet called after Build error, want not called")
	}
	msgs := toaster.snapshot()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "reload failed") || !strings.Contains(msgs[0], "nil registry") {
		t.Errorf("toaster messages = %v, want one 'reload failed: nil registry' entry", msgs)
	}
}

func TestReloadCommand_BuildPanicRecovered(t *testing.T) {
	svc := &stubService{panic: true}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) { return &config.UserConfig{}, nil },
		Service:        svc,
		Matcher:        mat,
		Toaster:        toaster.toast,
	})
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mat.swapCount() != 0 {
		t.Errorf("SwapTrieSet called after Build panic, want not called (old trie must remain)")
	}
	msgs := toaster.snapshot()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "reload failed") || !strings.Contains(msgs[0], "build panic") {
		t.Errorf("toaster messages = %v, want one 'reload failed: build panic: …' entry", msgs)
	}
}

func TestReloadCommand_WarningCountInToast(t *testing.T) {
	trie := NewTrieSet()
	svc := &stubService{
		trie: trie,
		warnings: []Warning{
			{Level: WarnLevel, Code: "orphan_action", Message: "no such action", Origin: "user.yml:7"},
		},
	}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) { return &config.UserConfig{}, nil },
		Service:        svc,
		Matcher:        mat,
		Toaster:        toaster.toast,
	})
	if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
	if mat.swapCount() != 1 {
		t.Errorf("SwapTrieSet called %d times, want 1", mat.swapCount())
	}
	msgs := toaster.snapshot()
	if len(msgs) != 1 || !strings.Contains(msgs[0], "config reloaded") || !strings.Contains(msgs[0], "1") {
		t.Errorf("toaster messages = %v, want one 'config reloaded (1 warning(s))' entry", msgs)
	}
}

func TestReloadCommand_ExtraArgsDropped(t *testing.T) {
	loadCalls := 0
	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			loadCalls++
			return &config.UserConfig{}, nil
		},
		Service: &stubService{trie: NewTrieSet()},
		Matcher: &recordingMatcher{},
		Toaster: func(string) {},
	})
	// Args MUST NOT influence LoadUserConfig (which takes no args).
	if err := cmd.Handler([]string{"/etc/passwd", "evil"}, commands.ExecCtx{}); err != nil {
		t.Fatalf("Handler with extra args: %v", err)
	}
	if loadCalls != 1 {
		t.Errorf("LoadUserConfig called %d times, want 1", loadCalls)
	}
}

func TestReloadCommand_ConcurrentSupersede(t *testing.T) {
	// 3 reloads queue against a blocking Build; the LAST submitted should
	// be the only one whose work actually runs. Earlier queued invocations
	// emit "reload superseded".
	gate := make(chan struct{})
	svc := &stubService{trie: NewTrieSet(), buildCh: gate}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) { return &config.UserConfig{}, nil },
		Service:        svc,
		Matcher:        mat,
		Toaster:        toaster.toast,
	})

	const N = 3
	var startWg, doneWg sync.WaitGroup
	startWg.Add(N)
	doneWg.Add(N)
	var started atomic.Int32
	// Pre-arm a barrier so all 3 goroutines hit Handler nearly together.
	barrier := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			defer doneWg.Done()
			startWg.Done()
			<-barrier
			started.Add(1)
			_ = cmd.Handler(nil, commands.ExecCtx{})
		}()
	}
	startWg.Wait()
	close(barrier)
	// Wait until all goroutines have entered Handler (each bumps the
	// counter and queues at runMu / blocks at the buildCh). The first to
	// acquire runMu blocks on the gate; the other two are queued.
	for started.Load() != N {
		// busy-wait is fine for this test; the gate is closed below.
	}
	// Release the gate; the in-flight Build returns. Then the queued
	// handlers wake; each sees latestGen != myGen (because subsequent
	// invocations bumped it) EXCEPT the very last one — which DOES run.
	close(gate)
	doneWg.Wait()

	// Exactly one Build (the in-flight one) ran; that's the FIRST to
	// acquire runMu. Per the "latest wins" rule, the LAST queued
	// invocation will also run Build because its myGen matches latestGen.
	// So Build runs either 1 or 2 times depending on scheduling — both
	// are acceptable. What MUST hold: at least one "reload superseded"
	// toast was emitted for the middle / superseded invocation(s).
	msgs := toaster.snapshot()
	supersedeCount := 0
	reloadedCount := 0
	for _, m := range msgs {
		if strings.Contains(m, "reload superseded") {
			supersedeCount++
		}
		if strings.Contains(m, "config reloaded") {
			reloadedCount++
		}
	}
	if supersedeCount == 0 {
		t.Errorf("expected at least one 'reload superseded' toast among %d invocations; got %v", N, msgs)
	}
	if reloadedCount == 0 {
		t.Errorf("expected at least one 'config reloaded' toast; got %v", msgs)
	}
	if supersedeCount+reloadedCount != N {
		t.Errorf("toast count mismatch: superseded=%d reloaded=%d total=%d, want sum=%d; full=%v",
			supersedeCount, reloadedCount, len(msgs), N, msgs)
	}
	// Defensive: swap count <= reloadedCount (only non-superseded paths swap).
	if mat.swapCount() != reloadedCount {
		t.Errorf("SwapTrieSet count = %d, want %d (matches non-superseded toast count)",
			mat.swapCount(), reloadedCount)
	}
}

func TestReloadCommand_SerialSuccessions(t *testing.T) {
	// Each serial call should fully run and use the latest cfg.
	cfgs := []*config.UserConfig{
		{Leader: "a"},
		{Leader: "b"},
		{Leader: "c"},
	}
	var idx atomic.Int32
	svc := &stubService{trie: NewTrieSet()}
	mat := &recordingMatcher{}
	toaster := &recordingToaster{}

	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) {
			i := idx.Add(1) - 1
			return cfgs[i], nil
		},
		Service: svc,
		Matcher: mat,
		Toaster: toaster.toast,
	})
	for i := 0; i < len(cfgs); i++ {
		if err := cmd.Handler(nil, commands.ExecCtx{}); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if mat.swapCount() != len(cfgs) {
		t.Errorf("SwapTrieSet count = %d, want %d", mat.swapCount(), len(cfgs))
	}
	msgs := toaster.snapshot()
	for _, m := range msgs {
		if !strings.Contains(m, "config reloaded") {
			t.Errorf("unexpected toast in serial run: %q", m)
		}
	}
	if len(msgs) != len(cfgs) {
		t.Errorf("toast count = %d, want %d", len(msgs), len(cfgs))
	}
}

// Sanity check: ensure ExCommand returned by ReloadCommand also conforms
// to the ExRegistry contract end-to-end.
func TestReloadCommand_RegistersIntoExRegistry(t *testing.T) {
	r := NewExRegistry()
	cmd := ReloadCommand(ReloadDeps{
		LoadUserConfig: func() (*config.UserConfig, error) { return &config.UserConfig{}, nil },
		Service:        &stubService{trie: NewTrieSet()},
		Matcher:        &recordingMatcher{},
	})
	if err := r.Register(cmd); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Get("reload")
	if !ok {
		t.Fatal("Get(reload): not found")
	}
	if err := got.Handler(nil, commands.ExecCtx{Mode: types.ModeCommand}); err != nil {
		t.Fatalf("Handler: %v", err)
	}
}

