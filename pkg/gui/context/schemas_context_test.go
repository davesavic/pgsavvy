package context

import (
	"sync"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/gui/types"
)

// TestSchemasContextShowHiddenModeConcurrentToggle fires ~100 concurrent
// goroutines flipping SetShowHiddenMode and reading GetShowHiddenMode to
// verify the atomic.Bool guard (enn.6 deferred AC). Run under -race the
// Go runtime will fail the test if it observes a data race on the field.
func TestSchemasContextShowHiddenModeConcurrentToggle(t *testing.T) {
	tree := NewContextTree(types.ContextTreeDeps{})
	s := tree.Schemas

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n * 2)

	for i := 0; i < n; i++ {
		v := i%2 == 0
		go func(v bool) {
			defer wg.Done()
			s.SetShowHiddenMode(v)
		}(v)
		go func() {
			defer wg.Done()
			_ = s.GetShowHiddenMode()
		}()
	}

	wg.Wait()

	// Final write so the post-condition is deterministic regardless of
	// goroutine scheduling: any Load after this point must observe true.
	s.SetShowHiddenMode(true)
	if !s.GetShowHiddenMode() {
		t.Fatal("Schemas.GetShowHiddenMode() = false after final SetShowHiddenMode(true)")
	}
}
