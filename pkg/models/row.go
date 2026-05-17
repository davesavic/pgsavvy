package models

// Row is a single result row. Values are driver-populated in column order.
type Row struct {
	Values []any
}
