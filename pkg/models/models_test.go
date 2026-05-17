package models

import (
	"testing"
	"time"
)

func TestModelsCompile(t *testing.T) {
	_ = Connection{}
	_ = SSHTunnelConfig{}
	_ = Database{}
	_ = Schema{}
	var tbl Table
	_ = &tbl
	_ = Column{}
	_ = Index{}
	_ = Constraint{}
	_ = Row{}
	_ = Result{}
	_ = Query{Timeout: time.Second}
	_ = Plan{}
	_ = PlanNode{}
}

func TestTableAtomicZeroValue(t *testing.T) {
	var tbl Table
	if got := tbl.EstimatedRows.Load(); got != 0 {
		t.Fatalf("EstimatedRows zero value = %d, want 0", got)
	}
	if got := tbl.SizeBytes.Load(); got != 0 {
		t.Fatalf("SizeBytes zero value = %d, want 0", got)
	}
}
