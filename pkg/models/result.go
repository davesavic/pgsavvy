package models

import "time"

// Result captures the outcome of a query execution.
type Result struct {
	Columns      []*Column
	Rows         []*Row
	RowsAffected int64
	Duration     time.Duration
	Notices      []string
	HasMore      bool
	Error        error
}
