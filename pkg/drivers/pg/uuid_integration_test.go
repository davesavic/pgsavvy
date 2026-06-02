//go:build integration

// Integration test for uuid column decoding against the docker/postgres
// fixture. Skipped (not failed) when DBSAVVY_TEST_PG is unset; see
// requirePGSession for the shared probe/skip pattern.
//
// Regression guard: pgx's default UUIDCodec decodes uuid into [16]byte, which
// the grid renders as Go's "[85 14 132 ...]" byte-array notation instead of the
// canonical "550e8400-..." string. A driver-level codec must make uuid values
// stringify canonically wherever Row.Values is consumed.
package pg_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestPgUUIDDecodesToCanonicalString(t *testing.T) {
	sess := requirePGSession(t)
	const want = "550e8400-e29b-41d4-a716-446655440000"
	res, err := sess.Execute(context.Background(), models.Query{
		SQL: "SELECT '" + want + "'::uuid AS id",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 1 || len(res.Rows[0].Values) != 1 {
		t.Fatalf("want 1 row / 1 col, got %d rows", len(res.Rows))
	}
	// The grid stringifies non-special cells via fmt.Sprintf("%v", value).
	// A [16]byte renders as "[85 14 132 ...]"; the canonical string is the bar.
	got := fmt.Sprintf("%v", res.Rows[0].Values[0])
	if got != want {
		t.Fatalf("uuid value rendered as %q (type %T), want canonical %q",
			got, res.Rows[0].Values[0], want)
	}
}

func TestPgUUIDArrayDecodesToCanonicalStrings(t *testing.T) {
	sess := requirePGSession(t)
	const a, b = "550e8400-e29b-41d4-a716-446655440000", "00000000-0000-0000-0000-000000000001"
	res, err := sess.Execute(context.Background(), models.Query{
		SQL: "SELECT ARRAY['" + a + "','" + b + "']::uuid[] AS ids",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(res.Rows))
	}
	// Each array element must stringify canonically, not as [16]byte.
	got := fmt.Sprintf("%v", res.Rows[0].Values[0])
	if !strings.Contains(got, a) || !strings.Contains(got, b) {
		t.Fatalf("uuid[] rendered as %q (type %T), want elements %q and %q",
			got, res.Rows[0].Values[0], a, b)
	}
}
