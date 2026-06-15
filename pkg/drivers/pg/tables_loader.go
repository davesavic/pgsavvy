package pg

import (
	"context"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TableLoader is the package-private machinery behind Session.ListTables. It
// mirrors lazygit's branch_loader.go: a cheap synchronous first query plus an
// asynchronous enrichment hook (onWorker) that fans out the expensive
// table_stats query. See DESIGN.md §11.3.
//
// External callers MUST go through Session.ListTables; only same-package
// async-enrichment glue should construct or invoke a TableLoader directly.
// See Arch-5 of the review-plan resolutions.
type TableLoader struct {
	session *Session
}

// newTableLoader binds a fresh loader to s. It is package-private by
// convention; the Session method that invokes it has already taken the
// inFlight guard.
func newTableLoader(s *Session) *TableLoader { return &TableLoader{session: s} }

// Load runs the cheap list_tables.sql query, scans the rows into
// []*models.Table, preserves prior EstimatedRows/SizeBytes counters from
// oldTables to avoid UI flicker, and schedules the expensive table_stats
// enrichment via onWorker. Returns the fast-path table slice immediately;
// the enrichment fills in stats out-of-band and calls renderFunc when done.
func (l *TableLoader) Load(
	ctx context.Context,
	schema string,
	oldTables []*models.Table,
	onWorker func(func() error),
	renderFunc func(),
) ([]*models.Table, error) {
	rows, err := l.session.Conn().Query(ctx, sqlListTables, schema)
	if err != nil {
		return nil, wrapPgError(err)
	}
	defer rows.Close()

	tables := []*models.Table{}
	for rows.Next() {
		t := &models.Table{}
		var owner *string
		var desc *string
		if err := rows.Scan(&t.Schema, &t.Name, &t.Kind, &owner, &desc); err != nil {
			return nil, wrapPgError(err)
		}
		if owner != nil {
			t.Owner = *owner
		}
		if desc != nil {
			t.Description = *desc
		}
		if old := findTable(oldTables, t.Schema, t.Name); old != nil {
			t.EstimatedRows.Store(old.EstimatedRows.Load())
			t.SizeBytes.Store(old.SizeBytes.Load())
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPgError(err)
	}

	onWorker(func() error {
		return l.enrichWithStats(ctx, tables, renderFunc)
	})

	return tables, nil
}

// enrichWithStats runs table_stats.sql for every (schema, name) pair in
// tables and atomically populates EstimatedRows/SizeBytes. renderFunc is
// invoked once after the scan loop so the UI repaints exactly once per
// enrichment pass.
func (l *TableLoader) enrichWithStats(ctx context.Context, tables []*models.Table, renderFunc func()) error {
	if len(tables) == 0 {
		renderFunc()
		return nil
	}
	rows, err := l.session.Conn().Query(ctx, sqlTableStats, schemaList(tables), nameList(tables))
	if err != nil {
		return wrapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, name string
		var estRows, sizeBytes int64
		if err := rows.Scan(&schema, &name, &estRows, &sizeBytes); err != nil {
			return wrapPgError(err)
		}
		if t := findTable(tables, schema, name); t != nil {
			t.EstimatedRows.Store(estRows)
			t.SizeBytes.Store(sizeBytes)
		}
	}
	if err := rows.Err(); err != nil {
		return wrapPgError(err)
	}
	renderFunc()
	return nil
}

// findTable returns the first table in tables whose (Schema, Name) matches.
// Linear scan; expected slice length is bounded by user-visible tables.
func findTable(tables []*models.Table, schema, name string) *models.Table {
	for _, t := range tables {
		if t.Schema == schema && t.Name == name {
			return t
		}
	}
	return nil
}

// schemaList extracts the Schema field of every table into a flat string
// slice for passing as a text[] bind parameter to table_stats.sql.
func schemaList(tables []*models.Table) []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.Schema
	}
	return out
}

// nameList extracts the Name field of every table into a flat string slice
// for passing as a text[] bind parameter to table_stats.sql.
func nameList(tables []*models.Table) []string {
	out := make([]string, len(tables))
	for i, t := range tables {
		out[i] = t.Name
	}
	return out
}
