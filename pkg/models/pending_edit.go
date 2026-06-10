package models

import (
	"errors"
	"reflect"
	"sync"
	"time"
)

// EditKind discriminates between a literal value edit and an expression
// edit. See DESIGN.md §11 / ADR-14.
type EditKind int

const (
	// Literal is a value-substitution edit: NewValue holds the typed value
	// to write.
	Literal EditKind = iota
	// Expression is a SQL-expression edit: NewExpr holds a raw expression
	// to splice into UPDATE without parameter binding.
	Expression
)

// ErrEmptyPrimaryKey is returned by PendingEditSet.Add when the edit's
// PrimaryKey is empty or nil. A row cannot be identified without a PK.
var ErrEmptyPrimaryKey = errors.New("pending edit: primary key is empty")

// PendingEdit captures one staged column-level mutation on a single row.
// PrimaryKey identifies the row; Column the field. Kind selects whether
// NewValue or NewExpr is the source of the new content.
type PendingEdit struct {
	// PrimaryKey holds the row's PK values, in PK column order.
	PrimaryKey []any
	// Column is the name of the column being edited.
	Column string
	// ColumnType is the column's SQL type name (e.g. "jsonb"), captured at
	// staging time so the commit preview can render OldValue/NewValue with
	// the same type-aware formatting as the grid and cell editor rather
	// than guessing from the Go value's shape. Display-only; not used for
	// SQL generation or conflict detection.
	ColumnType string
	// OldValue is the server-loaded value at the time of staging; used for
	// optimistic-concurrency conflict detection.
	OldValue any
	// NewValue is the staged literal value (Kind == Literal).
	NewValue any
	// NewExpr is the staged raw SQL expression (Kind == Expression).
	NewExpr string
	// Kind selects the active payload (NewValue vs NewExpr).
	Kind EditKind
	// LoadedAt is the timestamp at which OldValue was sampled.
	LoadedAt time.Time
}

// ConflictedEdit pairs a staged PendingEdit with the current server value
// observed at conflict-check time. Produced when OldValue no longer
// matches the server.
type ConflictedEdit struct {
	// Edit is the staged edit that conflicted.
	Edit PendingEdit
	// ServerValue is the current value observed on the server.
	ServerValue any
	// LoadedAt is the timestamp at which ServerValue was sampled.
	LoadedAt time.Time
}

// PendingEditSet is a per-table collection of staged edits. All methods
// are safe for concurrent use (ADR-20).
//
// PendingEditSet contains a sync.RWMutex and must not be copied after
// first use; pass by pointer.
type PendingEditSet struct {
	// Table identifies the schema-qualified target table.
	Table Ref

	mu    sync.RWMutex
	edits []PendingEdit
}

// Add stages an edit. If an edit already exists for the same PrimaryKey
// and Column, it is replaced in place: the existing OldValue and
// LoadedAt are preserved, while NewValue, NewExpr and Kind are taken
// from e. Returns ErrEmptyPrimaryKey if e.PrimaryKey is empty.
func (s *PendingEditSet) Add(e PendingEdit) error {
	if len(e.PrimaryKey) == 0 {
		return ErrEmptyPrimaryKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.edits {
		existing := &s.edits[i]
		if existing.Column == e.Column && pkEqual(existing.PrimaryKey, e.PrimaryKey) {
			existing.NewValue = e.NewValue
			existing.NewExpr = e.NewExpr
			existing.Kind = e.Kind
			return nil
		}
	}
	s.edits = append(s.edits, e)
	return nil
}

// Remove deletes the edit for (pk, col) if present; no-op otherwise.
func (s *PendingEditSet) Remove(pk []any, col string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.edits {
		if s.edits[i].Column == col && pkEqual(s.edits[i].PrimaryKey, pk) {
			s.edits = append(s.edits[:i], s.edits[i+1:]...)
			return
		}
	}
}

// Clear removes all staged edits.
func (s *PendingEditSet) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits = nil
}

// IsEmpty reports whether there are no staged edits.
func (s *PendingEditSet) IsEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.edits) == 0
}

// Count returns the number of staged edits.
func (s *PendingEditSet) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.edits)
}

// HasEdit reports whether (pk, col) already has a staged edit. Drives
// the CellEditor "dirty cell" branch where re-entering a cell that was
// previously staged surfaces the pending edit instead of the server
// value.
func (s *PendingEditSet) HasEdit(pk []any, col string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range s.edits {
		if s.edits[i].Column == col && pkEqual(s.edits[i].PrimaryKey, pk) {
			return true
		}
	}
	return false
}

// Edits returns a defensive copy of the staged edits. The returned slice
// may be mutated by the caller without affecting the set.
func (s *PendingEditSet) Edits() []PendingEdit {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PendingEdit, len(s.edits))
	copy(out, s.edits)
	return out
}

// pkEqual compares two primary-key value slices by element using
// reflect.DeepEqual; lengths must match.
func pkEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}
