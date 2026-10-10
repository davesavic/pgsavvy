package models

import (
	"errors"
	"math"
	"math/big"
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

var ErrCommitOutcomeUnknown = errors.New("pending edit: COMMIT outcome unknown; reconcile server state before retrying")

var ErrCommitIdentityUnresolved = errors.New("pending edit: resulting COMMIT identities missing or ambiguous; reconciliation remains blocked")

var ErrCommitInFlight = errors.New("pending edit: commit operation in progress; wait for settlement")

var ErrCommitSnapshotStale = errors.New("pending edit: selected commit snapshot is stale; reopen the dialog")

// PendingEdit captures one staged column-level mutation on a single row.
// PrimaryKey identifies the row; Column the field. Kind selects whether
// NewValue or NewExpr is the source of the new content.
type PendingEdit struct {
	revision uint64
	owner    *PendingEditSet
	identity EditIdentity
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
	// TypeOID is the column's PostgreSQL type OID (e.g. 114 for json, 3802
	// for jsonb), captured at staging time. Used by the encoder to
	// disambiguate types whose Go representation alone is insufficient
	// (notably json vs jsonb vs text). Zero means "infer from value's
	// Go type".
	TypeOID uint32
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

	mu                 sync.RWMutex
	edits              []PendingEdit
	nextRevision       uint64
	unknown            []PendingEdit
	unknownOriginalPKs [][]any
	unknownResultPKs   [][]any
	commitAttempt      *PendingEditSet
	commitEdits        []PendingEdit
	identity           EditIdentity
}

// BeginCommit atomically captures owned versions and reserves their rows until
// settlement. A nil selection means all edits; overwrite supplies its selection.
func (s *PendingEditSet) BeginCommit(selected []PendingEdit) (*PendingEditSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitAttempt != nil {
		return nil, ErrCommitInFlight
	}
	if len(s.unknown) != 0 {
		return nil, ErrCommitOutcomeUnknown
	}
	if selected == nil {
		selected = s.edits
	}
	if len(selected) == 0 {
		return nil, ErrCommitSnapshotStale
	}
	snapshot := &PendingEditSet{Table: s.Table, identity: s.identity.Clone()}
	for _, attempt := range selected {
		found := false
		for _, current := range s.edits {
			if attempt.owner == s && attempt.revision != 0 && attempt.revision == current.revision {
				snapshot.edits = append(snapshot.edits, clonePendingEdit(current))
				found = true
				break
			}
		}
		if !found {
			return nil, ErrCommitSnapshotStale
		}
	}
	s.commitAttempt = snapshot
	s.commitEdits = snapshot.Edits()
	return snapshot, nil
}

func (s *PendingEditSet) CommitInFlight() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.commitAttempt != nil
}

// SettleCommit is mandatory even when the originating view is gone. Receipt
// identity makes duplicate/stale delivery harmless; discard never releases rows.
func (s *PendingEditSet) SettleCommit(snapshot *PendingEditSet, committed, unknown bool, originalPKs, resultPKs [][]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snapshot == nil || s.commitAttempt != snapshot {
		return
	}
	if committed {
		s.retireCommitted(s.commitEdits, originalPKs, resultPKs)
	} else if unknown {
		s.unknown = append(s.unknown, s.commitEdits...)
		s.unknownOriginalPKs = clonePendingValue(reflect.ValueOf(originalPKs)).Interface().([][]any)
		s.unknownResultPKs = clonePendingValue(reflect.ValueOf(resultPKs)).Interface().([][]any)
	}
	s.commitAttempt = nil
	s.commitEdits = nil
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
	return s.addLocked(e)
}

func (s *PendingEditSet) Identity() EditIdentity {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.identity.Clone()
}

// BindIdentity supports explicit non-editor callers before any edits exist.
func (s *PendingEditSet) BindIdentity(id EditIdentity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.edits) != 0 || s.commitAttempt != nil || len(s.commitEdits) != 0 || len(s.unknown) != 0 {
		return ErrEditIdentityMismatch
	}
	s.identity = id.Clone()
	return nil
}

// AddWithIdentity binds the first successful stage and checks all subsequent
// tuples under the same lock as edit replacement and commit reservations.
func (s *PendingEditSet) AddWithIdentity(id EditIdentity, e PendingEdit) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if len(e.PrimaryKey) != len(id.Columns) {
		return ErrEditIdentityMismatch
	}
	if e.Column == "" {
		return ErrEditIdentityUnresolved
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	quiescent := len(s.edits) == 0 && s.commitAttempt == nil && len(s.commitEdits) == 0 && len(s.unknown) == 0
	if !s.identity.Compatible(id) {
		if !quiescent {
			return ErrEditIdentityMismatch
		}
		previous := s.identity
		s.identity = id.Clone()
		if err := s.addLocked(e); err != nil {
			s.identity = previous
			return err
		}
		return nil
	}
	pk, err := s.identity.Normalize(id, e.PrimaryKey)
	if err != nil {
		return err
	}
	e.PrimaryKey = pk
	e.identity = s.identity.Clone()
	return s.addLocked(e)
}

func (e PendingEdit) Identity() EditIdentity { return e.identity.Clone() }

func (s *PendingEditSet) addLocked(e PendingEdit) error {
	if s.identity.RelationOID != 0 {
		if len(e.PrimaryKey) != len(s.identity.Columns) {
			return ErrEditIdentityMismatch
		}
		e.identity = s.identity.Clone()
	}
	for _, attempt := range s.commitEdits {
		if pkEqual(attempt.PrimaryKey, e.PrimaryKey) {
			return ErrCommitInFlight
		}
	}
	for _, attempt := range s.unknown {
		if pkEqual(attempt.PrimaryKey, e.PrimaryKey) {
			return ErrCommitOutcomeUnknown
		}
	}
	e = clonePendingEdit(e)
	e.owner = s
	s.nextRevision++
	e.revision = s.nextRevision
	for i := range s.edits {
		existing := &s.edits[i]
		if existing.Column == e.Column && pkEqual(existing.PrimaryKey, e.PrimaryKey) {
			existing.NewValue = e.NewValue
			existing.NewExpr = e.NewExpr
			existing.Kind = e.Kind
			existing.revision = e.revision
			existing.owner = s
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
	s.removeLocked(pk, col)
}

func (s *PendingEditSet) RemoveWithIdentity(id EditIdentity, pk []any, col string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	pk, err := s.identity.Normalize(id, pk)
	if err != nil {
		return err
	}
	s.removeLocked(pk, col)
	return nil
}

func (s *PendingEditSet) FindWithIdentity(id EditIdentity, pk []any, col string) (PendingEdit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pk, err := s.identity.Normalize(id, pk)
	if err != nil {
		return PendingEdit{}, false
	}
	for _, e := range s.edits {
		if e.Column == col && pkEqual(e.PrimaryKey, pk) {
			return clonePendingEdit(e), true
		}
	}
	return PendingEdit{}, false
}

func (s *PendingEditSet) removeLocked(pk []any, col string) {
	for i := range s.edits {
		if s.edits[i].Column == col && pkEqual(s.edits[i].PrimaryKey, pk) {
			s.edits = append(s.edits[:i], s.edits[i+1:]...)
			return
		}
	}
}

// DiscardOwned checks all captured versions before changing any pending edits.
func (s *PendingEditSet) DiscardOwned(selected []PendingEdit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitAttempt != nil {
		return ErrCommitInFlight
	}
	if len(s.unknown) != 0 {
		return ErrCommitOutcomeUnknown
	}
	for _, e := range selected {
		found := false
		for _, current := range s.edits {
			if e.owner == s && e.revision != 0 && e.revision == current.revision {
				found = true
				break
			}
		}
		if !found {
			return ErrCommitSnapshotStale
		}
	}
	for _, e := range selected {
		s.removeLocked(e.PrimaryKey, e.Column)
	}
	return nil
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
	for i, e := range s.edits {
		out[i] = clonePendingEdit(e)
	}
	return out
}

// Retire removes only unchanged versions from a confirmed committed snapshot.
// A replacement staged while I/O was running remains pending.
func (s *PendingEditSet) Retire(snapshot []PendingEdit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retire(snapshot)
}

// RetireCommitted also carries surviving same-row edits to the resulting
// identity. Keeping an obsolete key could address a subsequently reused key.
func (s *PendingEditSet) RetireCommitted(snapshot []PendingEdit, originalPKs, resultPKs [][]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retireCommitted(snapshot, originalPKs, resultPKs)
}

func (s *PendingEditSet) retireCommitted(snapshot []PendingEdit, originalPKs, resultPKs [][]any) {
	s.retire(snapshot)
	if len(originalPKs) != len(resultPKs) {
		return
	}
	for i := range s.edits {
		for j, original := range originalPKs {
			if pkEqual(s.edits[i].PrimaryKey, original) && !pkEqual(original, resultPKs[j]) {
				s.edits[i].PrimaryKey = clonePendingValue(reflect.ValueOf(resultPKs[j])).Interface().([]any)
				s.nextRevision++
				s.edits[i].revision = s.nextRevision
				break
			}
		}
	}
}

func (s *PendingEditSet) retire(snapshot []PendingEdit) {
	kept := s.edits[:0]
	for _, current := range s.edits {
		matched := false
		for _, attempt := range snapshot {
			// Owned versions identify immutable staged intent. Comparing payloads
			// would fail for unchanged NaN values, which are not reflexively equal.
			if current.revision != 0 && current.revision == attempt.revision && attempt.owner == s {
				matched = true
				break
			}
		}
		if !matched {
			kept = append(kept, current)
		}
	}
	clear(s.edits[len(kept):])
	s.edits = kept
}

// MarkCommitUnknown preserves a receipt independently of Clear/Remove. Neither
// discarding local edits nor closing a view establishes the server's outcome.
func (s *PendingEditSet) MarkCommitUnknown(snapshot []PendingEdit, originalPKs, resultPKs [][]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range snapshot {
		s.unknown = append(s.unknown, clonePendingEdit(e))
	}
	s.unknownOriginalPKs = append(s.unknownOriginalPKs, clonePendingValue(reflect.ValueOf(originalPKs)).Interface().([][]any)...)
	s.unknownResultPKs = append(s.unknownResultPKs, clonePendingValue(reflect.ValueOf(resultPKs)).Interface().([][]any)...)
}

// CommitUnknownIdentities returns owned copies for explicit server verification.
func (s *PendingEditSet) CommitUnknownIdentities() ([][]any, [][]any) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clonePendingValue(reflect.ValueOf(s.unknownOriginalPKs)).Interface().([][]any), clonePendingValue(reflect.ValueOf(s.unknownResultPKs)).Interface().([][]any)
}

func (s *PendingEditSet) CommitOutcomeUnknown() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.unknown) > 0
}

// ReconcileCommitOutcome may be called ONLY after explicitly checking server
// state, including the captured original/resulting identities. committed retires
// matching versions and rekeys survivors; false leaves them staged. This
// never executes SQL or replays an expression. No automatic reconciliation UI.
func (s *PendingEditSet) ReconcileCommitOutcome(committed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitAttempt != nil {
		return ErrCommitInFlight
	}
	if committed {
		if !validCommitIdentities(s.unknown, s.unknownOriginalPKs, s.unknownResultPKs) {
			return ErrCommitIdentityUnresolved
		}
		s.retireCommitted(s.unknown, s.unknownOriginalPKs, s.unknownResultPKs)
	}
	s.unknown = nil
	s.unknownOriginalPKs = nil
	s.unknownResultPKs = nil
	return nil
}

func validCommitIdentities(edits []PendingEdit, originalPKs, resultPKs [][]any) bool {
	if len(originalPKs) != len(resultPKs) {
		return false
	}
	for i, pk := range originalPKs {
		if len(pk) == 0 || len(pk) != len(resultPKs[i]) {
			return false
		}
		attempted := false
		for _, edit := range edits {
			if RowIdentityEqual(pk, edit.PrimaryKey) {
				attempted = true
				break
			}
		}
		if !attempted {
			return false
		}
		for _, v := range resultPKs[i] {
			if v == nil {
				return false
			}
		}
		for _, other := range originalPKs[:i] {
			if RowIdentityEqual(pk, other) {
				return false
			}
		}
		for _, other := range resultPKs[:i] {
			if RowIdentityEqual(resultPKs[i], other) {
				return false
			}
		}
	}
	for _, edit := range edits {
		found := false
		for _, pk := range originalPKs {
			if RowIdentityEqual(pk, edit.PrimaryKey) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func clonePendingEdit(e PendingEdit) PendingEdit {
	e.identity = e.identity.Clone()
	e.PrimaryKey = clonePendingValue(reflect.ValueOf(e.PrimaryKey)).Interface().([]any)
	if e.OldValue != nil {
		e.OldValue = clonePendingValue(reflect.ValueOf(e.OldValue)).Interface()
	}
	if e.NewValue != nil {
		e.NewValue = clonePendingValue(reflect.ValueOf(e.NewValue)).Interface()
	}
	return e
}

// Driver values can contain typed arrays, JSON maps, byte slices and numeric
// pointers. Preserve concrete types rather than round-trip through JSON.
func clonePendingValue(v reflect.Value) reflect.Value {
	if v.CanInterface() {
		if n, ok := v.Interface().(*big.Int); ok {
			if n == nil {
				return v
			}
			return reflect.ValueOf(new(big.Int).Set(n))
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		if v.Kind() == reflect.Pointer {
			out.Set(reflect.New(v.Type().Elem()))
			out.Elem().Set(clonePendingValue(v.Elem()))
		} else {
			out.Set(clonePendingValue(v.Elem()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(clonePendingValue(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), clonePendingValue(it.Value()))
		}
		return out
	case reflect.Array, reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		if v.Kind() == reflect.Array {
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(clonePendingValue(v.Index(i)))
			}
		} else {
			for i := 0; i < v.NumField(); i++ {
				if out.Field(i).CanSet() && v.Field(i).CanInterface() {
					out.Field(i).Set(clonePendingValue(v.Field(i)))
				}
			}
		}
		return out
	default:
		return v
	}
}

// pkEqual compares two primary-key value slices by element using
// reflect.DeepEqual; lengths must match.
func pkEqual(a, b []any) bool {
	return RowIdentityEqual(a, b)
}

// RowIdentityEqual preserves typed structure and PostgreSQL's NaN identity
// equality. Formatting keys as text would conflate distinct array/string keys.
func RowIdentityEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !identityValueEqual(reflect.ValueOf(a[i]), reflect.ValueOf(b[i])) {
			return false
		}
	}
	return true
}

func identityValueEqual(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.CanInterface() && b.CanInterface() && reflect.DeepEqual(a.Interface(), b.Interface()) {
		return true
	}
	switch a.Kind() {
	case reflect.Float32, reflect.Float64:
		return math.IsNaN(a.Float()) && math.IsNaN(b.Float())
	case reflect.Interface, reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return false
		}
		return identityValueEqual(a.Elem(), b.Elem())
	case reflect.Slice, reflect.Array:
		if a.Kind() == reflect.Slice && a.IsNil() != b.IsNil() {
			return false
		}
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !identityValueEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !identityValueEqual(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	default:
		if a.Comparable() {
			return a.Equal(b)
		}
		return false
	}
}
