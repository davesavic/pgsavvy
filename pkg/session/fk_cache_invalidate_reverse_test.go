package session_test

import (
	"context"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
)

// TestFKCacheInvalidateReverse_DropsOnlyReverseEntry confirms InvalidateReverse
// evicts the cached inbound (reverse) entry for the matching key while leaving
// the forward entry and other reverse keys intact.
func TestFKCacheInvalidateReverse_DropsOnlyReverseEntry(t *testing.T) {
	t.Parallel()
	fwd := newLoaderFn()
	rev := newLoaderFn()
	fwd.stage("app", "customers", []models.ForeignKey{{Name: "fwd_customers"}})
	rev.stage("app", "customers", []models.ForeignKey{{Name: "rev_orders"}})
	rev.stage("app", "items", []models.ForeignKey{{Name: "rev_item_children"}})

	c := session.NewFKCache(fwd.load)
	c.SetReverseLoader(rev.load)
	ctx := context.Background()

	// Warm forward + two reverse keys.
	if _, err := c.Get(ctx, "app", "customers"); err != nil {
		t.Fatalf("Get forward: %v", err)
	}
	if _, err := c.GetReverse(ctx, "app", "customers"); err != nil {
		t.Fatalf("GetReverse customers: %v", err)
	}
	if _, err := c.GetReverse(ctx, "app", "items"); err != nil {
		t.Fatalf("GetReverse items: %v", err)
	}

	c.InvalidateReverse("app", "customers")

	// Re-read all three: only the reverse customers key reloads.
	_, _ = c.Get(ctx, "app", "customers")
	_, _ = c.GetReverse(ctx, "app", "customers")
	_, _ = c.GetReverse(ctx, "app", "items")

	if got := fwd.callCount("app", "customers"); got != 1 {
		t.Errorf("forward loader calls = %d, want 1 (untouched by InvalidateReverse)", got)
	}
	if got := rev.callCount("app", "customers"); got != 2 {
		t.Errorf("reverse customers loader calls = %d, want 2 (initial + post-invalidate)", got)
	}
	if got := rev.callCount("app", "items"); got != 1 {
		t.Errorf("reverse items loader calls = %d, want 1 (still cached)", got)
	}
}

// TestFKCacheInvalidateReverse_AbsentKeyIsNoop invalidating a reverse key that
// was never cached does not panic and leaves the cache usable.
func TestFKCacheInvalidateReverse_AbsentKeyIsNoop(t *testing.T) {
	t.Parallel()
	c := session.NewFKCache(func(context.Context, string, string) ([]models.ForeignKey, error) {
		return nil, nil
	})
	c.InvalidateReverse("nope", "nothing") // absent key, no reverse loader wired
}
