package common

import (
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// TestAppState_Save_SafeCallerPattern demonstrates the recommended pattern
// from AppState's godoc: callers serialize Save and mutators against an
// external sync.Mutex. The contract violation (concurrent map write + marshal)
// is documented in godoc, not asserted here — the assertion is that this safe
// pattern runs cleanly under -race for many iterations.
func TestAppState_Save_SafeCallerPattern(t *testing.T) {
	fs := afero.NewMemMapFs()
	const path = "/state.yml"

	a := &AppState{
		HiddenSchemas: map[string][]string{},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	const iters = 100

	wg.Add(2)
	// Writer: mutates the receiver under the lock.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			mu.Lock()
			a.HiddenSchemas["x"] = []string{"y"}
			mu.Unlock()
		}
	}()
	// Saver: takes the lock before Save so that yaml.Marshal sees a stable
	// snapshot of the map fields.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			mu.Lock()
			err := a.Save(fs, path)
			mu.Unlock()
			require.NoError(t, err)
		}
	}()
	wg.Wait()

	// Final state is loadable.
	b := &AppState{}
	require.NoError(t, b.Load(fs, path))
}
