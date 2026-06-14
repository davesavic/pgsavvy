package status

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// buildSet returns a PendingEditSet with n edits installed against
// PK=[1..n] on column "name". n==0 returns an empty set.
func buildSet(t *testing.T, n int) *models.PendingEditSet {
	t.Helper()
	s := &models.PendingEditSet{Table: models.Ref{Schema: "public", Table: "t"}}
	for i := 1; i <= n; i++ {
		if err := s.Add(models.PendingEdit{
			PrimaryKey: []any{int64(i)},
			Column:     "name",
			NewValue:   "x",
		}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	return s
}

// TestBuildPendingIndicator_NilSetReturnsEmpty proves the no-op path:
// a nil set yields "" so callers can unconditionally append the return
// to the options slice.
func TestBuildPendingIndicator_NilSetReturnsEmpty(t *testing.T) {
	if got := BuildPendingIndicator(nil, nil, 80); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestBuildPendingIndicator_EmptySetReturnsEmpty proves IsEmpty is
// honored — a fresh set with no edits collapses to "" too.
func TestBuildPendingIndicator_EmptySetReturnsEmpty(t *testing.T) {
	got := BuildPendingIndicator(&models.PendingEditSet{}, nil, 80)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestBuildPendingIndicator_NonEmptyExpanded proves the count rendering
// at adequate width.
func TestBuildPendingIndicator_NonEmptyExpanded(t *testing.T) {
	got := BuildPendingIndicator(buildSet(t, 3), nil, 80)
	if got != "[3 pending]" {
		t.Fatalf("got %q, want %q", got, "[3 pending]")
	}
}

// TestBuildPendingIndicator_CollapsedWhenNarrow proves the amendment's
// overflow rule: when availableWidth falls below the collapse threshold
// the indicator drops the "pending" word.
func TestBuildPendingIndicator_CollapsedWhenNarrow(t *testing.T) {
	got := BuildPendingIndicator(buildSet(t, 3), nil, 10)
	if got != "[3]" {
		t.Fatalf("got %q, want %q", got, "[3]")
	}
}

// TestBuildPendingIndicator_TintedWhenConfirmWrites proves the
// destructive-flag tinting rule for the ConfirmWrites case.
func TestBuildPendingIndicator_TintedWhenConfirmWrites(t *testing.T) {
	conn := &models.Connection{Color: "red", ConfirmWrites: true}
	got := BuildPendingIndicator(buildSet(t, 1), conn, 80)
	want := "\x1b[31m[1 pending]\x1b[0m"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestBuildPendingIndicator_TintedWhenReadOnly proves the ReadOnly case
// also triggers tinting.
func TestBuildPendingIndicator_TintedWhenReadOnly(t *testing.T) {
	conn := &models.Connection{Color: "yellow", ReadOnly: true}
	got := BuildPendingIndicator(buildSet(t, 2), conn, 80)
	if !strings.HasPrefix(got, "\x1b[33m") {
		t.Fatalf("got %q, want yellow SGR prefix", got)
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Fatalf("got %q, want SGR reset suffix", got)
	}
}

// TestBuildPendingIndicator_TintedWhenConfirmDDL proves the ConfirmDDL
// case also triggers tinting.
func TestBuildPendingIndicator_TintedWhenConfirmDDL(t *testing.T) {
	conn := &models.Connection{Color: "magenta", ConfirmDDL: true}
	got := BuildPendingIndicator(buildSet(t, 1), conn, 80)
	if !strings.Contains(got, "\x1b[35m") {
		t.Fatalf("got %q, want magenta SGR", got)
	}
}

// TestBuildPendingIndicator_UntintedWhenNoDestructiveFlag proves the
// negative path: a connection with no destructive flag returns the
// plain indicator even when conn.Color is set.
func TestBuildPendingIndicator_UntintedWhenNoDestructiveFlag(t *testing.T) {
	conn := &models.Connection{Color: "red"} // no ConfirmWrites/ReadOnly/ConfirmDDL
	got := BuildPendingIndicator(buildSet(t, 1), conn, 80)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("got %q must not contain ANSI escape when conn has no destructive flag", got)
	}
	if got != "[1 pending]" {
		t.Fatalf("got %q, want %q", got, "[1 pending]")
	}
}

// TestBuildPendingIndicator_UntintedWhenHexColor proves the unknown
// colour token path: a hex value collapses to the untinted form even
// when a destructive flag is set.
func TestBuildPendingIndicator_UntintedWhenHexColor(t *testing.T) {
	conn := &models.Connection{Color: "#abcdef", ConfirmWrites: true}
	got := BuildPendingIndicator(buildSet(t, 1), conn, 80)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("got %q must not contain ANSI escape for hex colour token", got)
	}
	if got != "[1 pending]" {
		t.Fatalf("got %q, want %q", got, "[1 pending]")
	}
}

// TestBuildPendingIndicator_NilConnUntinted proves the nil-conn path:
// no tinting attempted; raw indicator returned.
func TestBuildPendingIndicator_NilConnUntinted(t *testing.T) {
	got := BuildPendingIndicator(buildSet(t, 1), nil, 80)
	if got != "[1 pending]" {
		t.Fatalf("got %q, want %q", got, "[1 pending]")
	}
}

// TestBuildPendingIndicator_LargeCountFitsExpanded sanity-checks that a
// double-digit count still renders correctly.
func TestBuildPendingIndicator_LargeCountFitsExpanded(t *testing.T) {
	got := BuildPendingIndicator(buildSet(t, 42), nil, 80)
	if got != "[42 pending]" {
		t.Fatalf("got %q, want %q", got, "[42 pending]")
	}
}

// TestBuildPendingIndicatorCount_ZeroReturnsEmpty proves the cross-table
// count form omits the segment when nothing is staged anywhere.
func TestBuildPendingIndicatorCount_ZeroReturnsEmpty(t *testing.T) {
	if got := BuildPendingIndicatorCount(0, nil, 80); got != "" {
		t.Fatalf("got %q, want empty for zero count", got)
	}
	if got := BuildPendingIndicatorCount(-3, nil, 80); got != "" {
		t.Fatalf("got %q, want empty for negative count", got)
	}
}

// TestBuildPendingIndicatorCount_AggregatedTotal proves the indicator
// renders a summed cross-table count (e.g. 2 tables × edits).
func TestBuildPendingIndicatorCount_AggregatedTotal(t *testing.T) {
	got := BuildPendingIndicatorCount(7, nil, 80)
	if got != "[7 pending]" {
		t.Fatalf("got %q, want %q", got, "[7 pending]")
	}
}

// TestBuildPendingIndicatorCount_Tinted proves the count form honours the
// same destructive-flag tinting as the set form.
func TestBuildPendingIndicatorCount_Tinted(t *testing.T) {
	conn := &models.Connection{Color: "red", ConfirmWrites: true}
	got := BuildPendingIndicatorCount(4, conn, 80)
	want := "\x1b[31m[4 pending]\x1b[0m"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestBuildDisabledEditCellOption_EditableReturnsEmpty proves the
// no-op path: when GridView.Editable=true the segment is omitted.
func TestBuildDisabledEditCellOption_EditableReturnsEmpty(t *testing.T) {
	if got := BuildDisabledEditCellOption(true, "anything"); got != "" {
		t.Fatalf("got %q, want empty when editable=true", got)
	}
}

// TestBuildDisabledEditCellOption_WithReason proves the disabled
// segment carries the supplied reason verbatim (F2 frozen strings).
func TestBuildDisabledEditCellOption_WithReason(t *testing.T) {
	got := BuildDisabledEditCellOption(false, "no row identity")
	want := "[i] edit cell — disabled: no row identity"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestBuildDisabledEditCellOption_BlankReason proves the empty-reason
// fallback: the trailing ": <reason>" is dropped.
func TestBuildDisabledEditCellOption_BlankReason(t *testing.T) {
	got := BuildDisabledEditCellOption(false, "   ")
	want := "[i] edit cell — disabled"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
