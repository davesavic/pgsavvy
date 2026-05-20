package exporter

import (
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// synthRowSource emits N rows of a fixed shape without retaining any
// per-row state beyond a single reusable Row buffer. This ensures the
// memory pressure measured during export comes from the exporter's
// internal allocations, not the test's row source.
type synthRowSource struct {
	cols  []models.ColumnMeta
	total int
}

func (s *synthRowSource) Cols() []models.ColumnMeta { return s.cols }

func (s *synthRowSource) Iterate(fn func(models.Row) error) error {
	// Single reusable Values slice; we re-stamp the int counter per row.
	vals := make([]any, len(s.cols))
	for i := 0; i < s.total; i++ {
		// Synthesize a representative row: int id + short string + bool +
		// float — variety enough that all encoder paths get exercised.
		vals[0] = i
		vals[1] = "user-name"
		vals[2] = (i & 1) == 0
		vals[3] = float64(i) * 0.5
		if err := fn(models.Row{Values: vals}); err != nil {
			return err
		}
	}
	return nil
}

func make100kSource() *synthRowSource {
	cols := []models.ColumnMeta{
		{Name: "id", TypeOID: 23, TypeName: "int4"},
		{Name: "name", TypeOID: 25, TypeName: "text"},
		{Name: "active", TypeOID: 16, TypeName: "bool"},
		{Name: "score", TypeOID: 701, TypeName: "float8"},
	}
	return &synthRowSource{cols: cols, total: 100_000}
}

// heapBudget runs an export of 100k rows into io.Discard and asserts the
// peak HeapAlloc increase stays under 12 MiB. We sample HeapAlloc before
// and after, with two intervening runtime.GC() calls to flush.
//
// Caveats: HeapAlloc is sampled, not "peak". Go's GC may not run during
// the export, so HeapAlloc at the end represents the surviving live set
// plus uncollected garbage. We measure the delta between the live set at
// rest and the live set after Run() completes — this catches accidental
// retention but is permissive of transient allocations. The 12 MiB cap is
// generous; a streaming exporter should sit under 1 MiB delta in practice.
func heapBudget(t *testing.T, format Format) {
	t.Helper()

	// Pre-allocate the encoder + writer so their allocations don't count
	// against our budget.
	src := make100kSource()
	cols := src.Cols()

	// Warm: write one row to settle internal pools.
	if err := format.Header(cols, io.Discard); err != nil {
		t.Fatalf("Header: %v", err)
	}
	{
		vals := []any{0, "x", true, 0.5}
		if err := format.Row(models.Row{Values: vals}, io.Discard); err != nil {
			t.Fatalf("warm Row: %v", err)
		}
	}

	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	// Export 100k rows.
	for i := 0; i < src.total; i++ {
		vals := []any{i, "user-name", (i & 1) == 0, float64(i) * 0.5}
		if err := format.Row(models.Row{Values: vals}, io.Discard); err != nil {
			t.Fatalf("Row %d: %v", i, err)
		}
	}
	if err := format.Footer(io.Discard); err != nil {
		t.Fatalf("Footer: %v", err)
	}

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	var delta int64
	if after.HeapAlloc > before.HeapAlloc {
		delta = int64(after.HeapAlloc - before.HeapAlloc)
	} else {
		delta = -int64(before.HeapAlloc - after.HeapAlloc)
	}
	const budget = 12 * 1024 * 1024 // 12 MiB tolerance
	if delta > budget {
		t.Errorf("heap delta %d bytes (%.2f MiB) exceeds budget %d bytes (%.2f MiB)",
			delta, float64(delta)/1024/1024, budget, float64(budget)/1024/1024)
	}
}

// Note: heap-budget tests are gated by -short so quick CI runs skip them
// while local runs and the slow CI lane still execute them. Per the
// amendment register, the 12 MiB tolerance accommodates GC variance.

func TestStreaming100k_CSV_HeapBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heap-budget test in -short mode")
	}
	heapBudget(t, NewCSV())
}

func TestStreaming100k_TSV_HeapBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heap-budget test in -short mode")
	}
	heapBudget(t, NewTSV())
}

func TestStreaming100k_NDJSON_HeapBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heap-budget test in -short mode")
	}
	heapBudget(t, NewNDJSON())
}

func TestStreaming100k_SQLInserts_HeapBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping heap-budget test in -short mode")
	}
	// Need an Encoder; reuse the test fake from sql_inserts_test.go.
	// (sql_inserts_test.go defines fakeEncoder in the same package.)
	heapBudget(t, NewSQLInserts("public.users", fakeEncoder{}))
}

// TestStreaming100k_RowCount_Sanity is a non-budget sanity check that
// 100k rows actually flow through the source under -short too, so we
// don't lose coverage entirely when the budget tests are skipped.
func TestStreaming100k_RowCount_Sanity(t *testing.T) {
	src := make100kSource()
	n := 0
	if err := src.Iterate(func(_ models.Row) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if n != 100_000 {
		t.Errorf("row count = %d; want 100000", n)
	}
}

// TestStreaming100k_StartTime_Reasonable is a backstop: a streaming
// 100k-row export should complete in well under 5s on any modern hardware.
// We pick CSV (the simplest streaming format) and bail loudly if the
// runtime regresses (e.g., due to a per-row allocation that scales badly).
func TestStreaming100k_CSV_TimeUnderFiveSeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in -short mode")
	}
	start := time.Now()
	src := make100kSource()
	fmt := NewCSV()
	if err := fmt.Header(src.Cols(), io.Discard); err != nil {
		t.Fatalf("Header: %v", err)
	}
	if err := src.Iterate(func(r models.Row) error { return fmt.Row(r, io.Discard) }); err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if err := fmt.Footer(io.Discard); err != nil {
		t.Fatalf("Footer: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("CSV 100k took %v; want < 5s", elapsed)
	}
}
