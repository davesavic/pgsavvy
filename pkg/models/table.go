package models

import "sync/atomic"

// Table describes a relation (table, view, materialized view, etc.). Because it
// embeds atomic counters, Table values must be passed by *pointer downstream;
// copying a Table after first use is unsafe. See DESIGN.md §11.1.
type Table struct {
	Schema        string
	Name          string
	Kind          string
	Owner         string
	EstimatedRows atomic.Int64
	SizeBytes     atomic.Int64
	Description   string
}
