package models

import "time"

// Query is a SQL statement plus its bound arguments and optional timeout.
//
// DefaultSchema, when non-empty, is the schema that unqualified object names
// in SQL should resolve against (qualified names like sales.orders always win).
// It is a driver hint: the pg driver realises it as a SET search_path issued
// before the statement; drivers that lack the concept ignore it. Empty means
// "leave name resolution to the session's existing default" (dbsavvy-u1n).
type Query struct {
	SQL           string
	Args          []any
	Timeout       time.Duration
	DefaultSchema string
}
