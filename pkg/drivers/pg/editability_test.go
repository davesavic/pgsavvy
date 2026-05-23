package pg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// stubIntrospector returns the configured rows / err, ignoring the supplied
// OIDs. The unit tests below feed precisely the relRows they want to test.
func stubIntrospector(rows []relRow, err error) relIntrospector {
	return func(_ context.Context, _ []uint32) ([]relRow, error) {
		return rows, err
	}
}

func TestDecideEditability_AllZeroTableOID_Computed(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "n", TableOID: 0, TableAttributeNumber: 0},
		{Name: "x", TableOID: 0, TableAttributeNumber: 0},
	}
	ref, rowID, reason, err := decideEditability(context.Background(), cols, stubIntrospector(nil, nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ref != (models.Ref{}) {
		t.Fatalf("ref = %+v, want zero", ref)
	}
	if rowID != nil {
		t.Fatalf("rowIdentity = %v, want nil", rowID)
	}
	if reason != "result contains computed columns" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_MultipleTableOIDs_SpansMultipleTables(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "a", TableOID: 100, TableAttributeNumber: 1},
		{Name: "b", TableOID: 200, TableAttributeNumber: 1},
	}
	_, _, reason, err := decideEditability(context.Background(), cols, stubIntrospector(nil, nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if reason != "result spans multiple tables" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_CompositePK_BothColumnsInSelectOrder(t *testing.T) {
	// SELECT user_id, role_id FROM app.user_roles
	// PK is (user_id, role_id) → attnums [1, 2]. Both present in SELECT
	// order → RowIdentity = [0, 1].
	cols := []models.ColumnMeta{
		{Name: "user_id", TableOID: 42, TableAttributeNumber: 1},
		{Name: "role_id", TableOID: 42, TableAttributeNumber: 2},
	}
	row := relRow{
		OID: 42, RelKind: "r", Schema: "app", Name: "user_roles",
		PKAttnums: []int32{1, 2},
	}
	ref, rowID, reason, err := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected editable; got reason %q", reason)
	}
	if ref.Schema != "app" || ref.Table != "user_roles" {
		t.Fatalf("ref = %+v", ref)
	}
	if !reflect.DeepEqual(rowID, []int{0, 1}) {
		t.Fatalf("rowIdentity = %v, want [0 1]", rowID)
	}
}

func TestDecideEditability_CompositePK_DuplicatedColumnUsesLowestIndex(t *testing.T) {
	// SELECT user_id, role_id, user_id FROM app.user_roles
	// First occurrence of user_id is index 0; role_id is index 1.
	// The duplicated user_id at index 2 must NOT shift the row identity.
	cols := []models.ColumnMeta{
		{Name: "user_id", TableOID: 42, TableAttributeNumber: 1},
		{Name: "role_id", TableOID: 42, TableAttributeNumber: 2},
		{Name: "user_id_again", TableOID: 42, TableAttributeNumber: 1},
	}
	row := relRow{
		OID: 42, RelKind: "r", Schema: "app", Name: "user_roles",
		PKAttnums: []int32{1, 2},
	}
	_, rowID, reason, err := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected editable; got reason %q", reason)
	}
	if !reflect.DeepEqual(rowID, []int{0, 1}) {
		t.Fatalf("rowIdentity = %v, want [0 1] (lowest occurrence of each PK attnum)", rowID)
	}
}

func TestDecideEditability_View(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "id", TableOID: 7, TableAttributeNumber: 1}}
	row := relRow{OID: 7, RelKind: "v", Schema: "app", Name: "published_posts"}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "base relation is a view" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_MatView(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 8, TableAttributeNumber: 1}}
	row := relRow{OID: 8, RelKind: "m", Schema: "app", Name: "posts_summary"}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "materialized view" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_Foreign(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 9, TableAttributeNumber: 1}}
	row := relRow{OID: 9, RelKind: "f", Schema: "ext", Name: "remote"}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "foreign table" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_Partitioned(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 10, TableAttributeNumber: 1}}
	row := relRow{OID: 10, RelKind: "p", Schema: "app", Name: "events"}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "partitioned table" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_Temp(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 11, TableAttributeNumber: 1}}
	row := relRow{OID: 11, RelKind: "r", Schema: "pg_temp_3", Name: "scratch", IsTempSchema: true}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "temporary table" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_NoRowIdentity_NoPKNoUnique(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 12, TableAttributeNumber: 1}}
	row := relRow{OID: 12, RelKind: "r", Schema: "app", Name: "logs"}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "no row identity" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_NoRowIdentity_PKNotInSelect(t *testing.T) {
	// SELECT name FROM users — name is attnum 3, PK is id (attnum 1)
	// which is NOT in the SELECT list → no row identity.
	cols := []models.ColumnMeta{{Name: "name", TableOID: 13, TableAttributeNumber: 3}}
	row := relRow{
		OID: 13, RelKind: "r", Schema: "app", Name: "users",
		PKAttnums: []int32{1},
	}
	_, _, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "no row identity" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestDecideEditability_UniqueFallback(t *testing.T) {
	// Table has no PK but has a UNIQUE on email — and email is in the
	// SELECT list. Editable via UNIQUE.
	cols := []models.ColumnMeta{
		{Name: "email", TableOID: 14, TableAttributeNumber: 2},
		{Name: "name", TableOID: 14, TableAttributeNumber: 3},
	}
	row := relRow{
		OID: 14, RelKind: "r", Schema: "app", Name: "contacts",
		UniqueAttnums: [][]int32{{2}},
	}
	_, rowID, reason, _ := decideEditability(context.Background(), cols, stubIntrospector([]relRow{row}, nil))
	if reason != "" {
		t.Fatalf("expected editable; got reason %q", reason)
	}
	if !reflect.DeepEqual(rowID, []int{0}) {
		t.Fatalf("rowIdentity = %v, want [0]", rowID)
	}
}

func TestDecideEditability_IntrospectionError(t *testing.T) {
	cols := []models.ColumnMeta{{Name: "n", TableOID: 99, TableAttributeNumber: 1}}
	sqlErr := errors.New("query failed")
	_, _, reason, err := decideEditability(context.Background(), cols, stubIntrospector(nil, sqlErr))
	if err == nil {
		t.Fatalf("expected wrapped err, got nil")
	}
	if !errors.Is(err, sqlErr) {
		t.Fatalf("err does not wrap original: %v", err)
	}
	if !strings.HasPrefix(reason, "introspection failed: ") {
		t.Fatalf("reason = %q", reason)
	}
}

func TestApplyConnectionGate(t *testing.T) {
	tests := []struct {
		name               string
		editable           bool
		currentReason      string
		readOnly           bool
		supportsInlineEdit bool
		wantEditable       bool
		wantReason         string
	}{
		{
			name: "passthrough_editable", editable: true, currentReason: "", readOnly: false, supportsInlineEdit: true,
			wantEditable: true, wantReason: "",
		},
		{
			name: "passthrough_disabled", editable: false, currentReason: "no row identity", readOnly: false, supportsInlineEdit: true,
			wantEditable: false, wantReason: "no row identity",
		},
		{
			name: "read_only_overrides_editable", editable: true, currentReason: "", readOnly: true, supportsInlineEdit: true,
			wantEditable: false, wantReason: "read-only connection",
		},
		{
			name: "read_only_overrides_disabled", editable: false, currentReason: "no row identity", readOnly: true, supportsInlineEdit: true,
			wantEditable: false, wantReason: "read-only connection",
		},
		{
			name: "read_only_beats_no_inline_edit", editable: true, currentReason: "", readOnly: true, supportsInlineEdit: false,
			wantEditable: false, wantReason: "read-only connection",
		},
		{
			name: "no_inline_edit_when_not_read_only", editable: true, currentReason: "", readOnly: false, supportsInlineEdit: false,
			wantEditable: false, wantReason: "driver does not support inline edit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEditable, gotReason := ApplyConnectionGate(tc.editable, tc.currentReason, tc.readOnly, tc.supportsInlineEdit)
			if gotEditable != tc.wantEditable || gotReason != tc.wantReason {
				t.Fatalf("ApplyConnectionGate = (%v,%q), want (%v,%q)",
					gotEditable, gotReason, tc.wantEditable, tc.wantReason)
			}
		})
	}
}
