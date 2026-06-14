package ui

import (
	"reflect"
	"testing"
)

// TestAttachActiveTabOriginRoundTrip verifies the originating statement,
// its bound args, and the DefaultSchema captured at tab-open time round
// trip unchanged through the Tab.Origin accessor for a parameterized,
// schema-qualified result tab.
func TestAttachActiveTabOriginRoundTrip(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	const sql = "SELECT * FROM s.t WHERE c=$1"
	args := []any{42}
	const defaultSchema = "s"

	_ = h.openTab(sql, nil)
	h.AttachActiveTabOrigin(sql, args, defaultSchema)

	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after openTab")
	}

	gotSQL, gotArgs, gotSchema := active.Origin()
	if gotSQL != sql {
		t.Errorf("Origin sql = %q, want %q", gotSQL, sql)
	}
	if !reflect.DeepEqual(gotArgs, args) {
		t.Errorf("Origin args = %v, want %v", gotArgs, args)
	}
	if gotSchema != defaultSchema {
		t.Errorf("Origin defaultSchema = %q, want %q", gotSchema, defaultSchema)
	}
}

// TestOriginSQLAndErrorSQLShareCanonicalField verifies SetOrigin and
// SetErrorSQL write the same canonical origSQL field (no parallel SQL
// store).
func TestOriginSQLAndErrorSQLShareCanonicalField(t *testing.T) {
	h, _ := newTestHelper(t, nil)
	_ = h.openTab("placeholder", nil)
	active := h.Active()
	if active == nil {
		t.Fatal("Active = nil after openTab")
	}

	active.SetErrorSQL("ERR SQL")
	if got := active.errSQLSnapshot(); got != "ERR SQL" {
		t.Fatalf("errSQLSnapshot = %q, want %q", got, "ERR SQL")
	}
	if sql, _, _ := active.Origin(); sql != "ERR SQL" {
		t.Errorf("Origin sql = %q, want %q (errSQL must read from canonical field)", sql, "ERR SQL")
	}

	active.SetOrigin("ORIG SQL", nil, "")
	if got := active.errSQLSnapshot(); got != "ORIG SQL" {
		t.Errorf("errSQLSnapshot = %q, want %q (origin SQL must write canonical field)", got, "ORIG SQL")
	}
}
