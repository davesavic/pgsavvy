package models

import "time"

// Query is a SQL statement plus its bound arguments and optional timeout.
type Query struct {
	SQL     string
	Args    []any
	Timeout time.Duration
}
