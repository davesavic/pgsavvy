package helpers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// DefaultCellApplyTimeout is the per-Apply context timeout. Used when the
// caller-supplied ctx has no Deadline; an existing deadline is respected
// verbatim. Mirrors the 30s window quoted in the epic Amendments.
const DefaultCellApplyTimeout = 30 * time.Second

// SessionAcquirer is the narrow surface CellApplyHelper consumes for
// pulling a dedicated drivers.Session out of the pool. *pg.Connection
// (and any other drivers.Connection) satisfies it; tests inject a fake
// that hands back a recorder session.
//
// AcquireSession MUST return a session NOT shared with the user's main
// SQLSession — Apply runs its own BEGIN/COMMIT cycle and a shared
// session would entangle that transaction with the user's ongoing
// streams.
type SessionAcquirer interface {
	AcquireSession(ctx context.Context) (drivers.Session, error)
}

// CellApplyHelper drives the "apply pending edits" flow: acquire a
// dedicated session, wrap a BEGIN, issue one parameterized UPDATE per
// original row with IS NOT DISTINCT FROM old-value predicates to detect
// concurrent overwrites, collect conflicts (RowsAffected==0), and on
// success re-fetch the touched rows by PK so the caller can refresh the
// grid.
//
// The helper is stateless beyond its construction arguments; every Apply
// call is independent. Concurrency is bounded by the underlying driver
// pool (one in-flight session per Apply); the caller is expected to
// serialise Apply calls per table.
//
// Known limitation (per epic Amendments): IS NOT DISTINCT FROM uses the
// default equality operator for each column type. For citext columns
// case differences will hash-equal, and for char/text columns trailing
// whitespace may collapse depending on the column type — both edge
// cases match Postgres semantics, but a user staring at a visually
// different cell may be surprised. This is documented behaviour, not a
// bug; conflict resolution in A6 surfaces the server's view.
type CellApplyHelper struct {
	acquirer SessionAcquirer
	// timeout overrides DefaultCellApplyTimeout when non-zero. Zero falls
	// back to the constant; a caller-supplied ctx.Deadline always wins
	// (we only WithTimeout when no deadline is set).
	timeout time.Duration
}

// CellApplyDeps bundles CellApplyHelper's collaborators. Acquirer is
// required; Timeout is optional (DefaultCellApplyTimeout used when
// zero).
type CellApplyDeps struct {
	Acquirer SessionAcquirer
	Timeout  time.Duration
}

// NewCellApplyHelper constructs a helper. Returns nil when the required
// Acquirer is missing — Apply nil-checks and returns an error in that
// case as well, so wiring mistakes surface as runtime errors rather than
// nil-deref panics.
func NewCellApplyHelper(deps CellApplyDeps) *CellApplyHelper {
	return &CellApplyHelper{
		acquirer: deps.Acquirer,
		timeout:  deps.Timeout,
	}
}

// errors surfaced by Apply.
var (
	// ErrNoAcquirer is returned when the helper has no SessionAcquirer
	// wired. Indicates a construction mistake.
	ErrNoAcquirer = errors.New("cell apply: no session acquirer wired")
	// ErrPoolExhausted wraps the AcquireSession failure when the pool
	// cannot hand out an additional connection. Mentions "pool" so the
	// user sees a clear hint to raise MinConns/MaxConns.
	ErrPoolExhausted = errors.New("cell apply: connection pool exhausted (raise pool size)")
	// ErrMissingPKColumns is returned when pkCols is empty/nil. A
	// rowIdentity is required to address rows safely.
	ErrMissingPKColumns = errors.New("cell apply: pk column names missing")
	// ErrPKLengthMismatch is returned when a PendingEdit's PrimaryKey
	// slice does not match the supplied pkCols length.
	ErrPKLengthMismatch = errors.New("cell apply: pk value count does not match pk column count")
	ErrCommitUnknown    = models.ErrCommitOutcomeUnknown
)

// ApplyResult captures the outcome of a successful (or dry-run) Apply.
// RowsAffected[i] mirrors edits[i] (the order returned by
// PendingEditSet.Edits()). RefetchedRows holds the post-commit row data
// for the PK tuples touched, keyed by index into the edits slice's
// distinct-PK list (Apply collapses by PK). RefetchedColumns names the
// projection used for the refetch SELECT. Empty in dry-run mode.
type ApplyResult struct {
	Identity models.EditIdentity
	// Committed distinguishes a durable write from a later display-refresh error.
	Committed     bool
	CommitUnknown bool
	Edits         []models.PendingEdit
	OriginalPKs   [][]any
	ResultPKs     [][]any
	// RefetchedOriginalPKs is aligned with RefetchedRows, independent of SELECT order.
	RefetchedOriginalPKs [][]any
	RowsAffected         []int
	RefetchedRows        []models.Row
	RefetchedColumns     []models.ColumnMeta
	// SQL contains one statement per original row, in first-seen order.
	SQL []string
}

// Apply stages and commits the edits in set against the table identified
// by set.Table, using pkCols as the row-identity column names. On any
// conflict (RowsAffected==0 on a statement) the helper rolls back and
// returns a non-empty conflicts slice with the current server values;
// on a server error mid-apply (constraint, schema-change, network) it
// rolls back and returns a wrapped error. dryRun==true wraps the apply
// in BEGIN/ROLLBACK so callers can preview rows-affected without
// committing — conflict collection and refetch are skipped in dry-run.
//
// Behavioural contract:
//   - Empty set returns (zero ApplyResult, nil, nil) without issuing
//     BEGIN.
//   - Identifiers (schema/table/column) flow through pg.QuoteIdent /
//     pg.QuoteQualified — no string-formatted identifiers reach the
//     server.
//   - Literal edits parameterize NewValue; Expression edits inject
//     NewExpr verbatim into the SET clause.
//   - WHERE uses captured OldValue (NOT NewValue) so a PK edit cannot
//     match the post-edit row.
//   - Refetch SELECT uses a parameterized IN-list ($1, $2, …) keyed by
//     PK — no string-formatted PK values.
//   - Bounded context: when ctx has no Deadline, h.timeout (or
//     DefaultCellApplyTimeout) is applied.
func (h *CellApplyHelper) Apply(
	ctx context.Context,
	set *models.PendingEditSet,
	pkCols []string,
	dryRun bool,
) (ApplyResult, []models.ConflictedEdit, error) {
	return h.apply(ctx, set, pkCols, dryRun, false)
}

//nolint:gocyclo // linear transaction pipeline with explicit outcome boundaries
func (h *CellApplyHelper) apply(ctx context.Context, set *models.PendingEditSet, pkCols []string, dryRun, overwrite bool) (ApplyResult, []models.ConflictedEdit, error) {
	if h == nil || h.acquirer == nil {
		return ApplyResult{}, nil, ErrNoAcquirer
	}
	if set != nil && set.CommitOutcomeUnknown() {
		return ApplyResult{}, nil, models.ErrCommitOutcomeUnknown
	}
	if set == nil || set.IsEmpty() {
		return ApplyResult{}, nil, nil
	}
	if len(pkCols) == 0 {
		return ApplyResult{}, nil, ErrMissingPKColumns
	}
	identity := set.Identity()
	if err := identity.Validate(); err != nil {
		return ApplyResult{}, nil, err
	}
	if !slices.Equal(pkCols, identity.Names()) {
		return ApplyResult{}, nil, models.ErrEditIdentityMismatch
	}

	edits := set.Edits()
	batch, batchErr := BuildUpdateBatch(set.Table, pkCols, edits, SQLModeParameterized, !overwrite)
	if batchErr != nil {
		return ApplyResult{}, nil, batchErr
	}

	// Bounded context. Only wrap when caller did not set a deadline so
	// callers retain control over the upper bound (a 1s test ctx must
	// not be silently widened to 30s).
	if _, ok := ctx.Deadline(); !ok {
		timeout := h.timeout
		if timeout <= 0 {
			timeout = DefaultCellApplyTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	sess, err := h.acquirer.AcquireSession(ctx)
	if err != nil {
		// We can't reliably distinguish pool-saturation from a
		// transport-level open failure at this layer — wrap with the
		// pool-exhausted sentinel so callers get the actionable hint
		// alongside the underlying error.
		return ApplyResult{}, nil, fmt.Errorf("%w: %w", ErrPoolExhausted, err)
	}
	defer func() { _ = sess.Close() }()

	schema := set.Table.Schema
	table := set.Table.Table
	qualified := pg.QuoteQualified(schema, table)

	// BEGIN.
	if _, err := sess.Execute(ctx, models.Query{SQL: "BEGIN"}); err != nil {
		return ApplyResult{}, nil, fmt.Errorf("cell apply: BEGIN: %w", err)
	}

	rowsAffected := make([]int, len(edits))
	conflicts := make([]models.ConflictedEdit, 0)
	sqlTexts := make([]string, len(batch))
	out := ApplyResult{Identity: identity, Edits: edits, RowsAffected: rowsAffected, SQL: sqlTexts}
	for bi, stmt := range batch {
		e := edits[stmt.EditIndexes[0]]
		sqlTexts[bi] = stmt.SQL
		res, execErr := sess.Execute(ctx, models.Query{SQL: stmt.SQL, Args: stmt.Args})
		if execErr != nil {
			rollback(ctx, sess)
			return ApplyResult{}, nil, fmt.Errorf("cell apply: UPDATE %s.%s.%s: %w",
				schema, table, e.Column, execErr)
		}
		if res.RowsAffected > 1 || res.RowsAffected < 0 {
			rollback(ctx, sess)
			return ApplyResult{}, nil, fmt.Errorf("cell apply: unsafe row identity: UPDATE affected %d rows (expected at most one)", res.RowsAffected)
		}
		for _, i := range stmt.EditIndexes {
			rowsAffected[i] = int(res.RowsAffected)
		}
		if int64(len(res.Rows)) != res.RowsAffected {
			rollback(ctx, sess)
			return ApplyResult{}, nil, errors.New("cell apply: UPDATE returned inconsistent row identities")
		}
		if res.RowsAffected == 1 {
			if len(res.Rows) != 1 || res.Rows[0] == nil || len(res.Rows[0].Values) != len(pkCols) {
				rollback(ctx, sess)
				return ApplyResult{}, nil, errors.New("cell apply: UPDATE did not return one complete row identity")
			}
			out.OriginalPKs = append(out.OriginalPKs, append([]any(nil), stmt.OriginalPK...))
			out.ResultPKs = append(out.ResultPKs, append([]any(nil), res.Rows[0].Values...))
		}
		if overwrite && res.RowsAffected == 0 {
			rollback(ctx, sess)
			return ApplyResult{}, nil, fmt.Errorf("cell overwrite: row not found for %s (deleted by another session?)", e.Column)
		}

		if !dryRun && res.RowsAffected == 0 {
			// A failed row guard leaves all its assignments unapplied. Show
			// every affected column, including guards that individually matched.
			for _, i := range stmt.EditIndexes {
				e = edits[i]
				srvVal, srvErr := fetchServerValue(ctx, sess, qualified, pkCols, e)
				if srvErr != nil {
					rollback(ctx, sess)
					return ApplyResult{}, nil, fmt.Errorf("cell apply: refetch server value for %s: %w",
						e.Column, srvErr)
				}
				conflicts = append(conflicts, models.ConflictedEdit{
					Edit:        e,
					ServerValue: srvVal,
					LoadedAt:    time.Now(),
				})
			}
		}
	}

	if len(conflicts) > 0 {
		rollback(ctx, sess)
		return ApplyResult{}, conflicts, nil
	}

	if dryRun {
		rollback(ctx, sess)
		return out, nil, nil
	}

	if _, err := sess.Execute(ctx, models.Query{SQL: "COMMIT"}); err != nil {
		out.CommitUnknown = true
		set.MarkCommitUnknown(edits, out.OriginalPKs, out.ResultPKs)
		return out, nil, fmt.Errorf("%w: %w", ErrCommitUnknown, err)
	}
	out.Committed = true

	// Refetch the touched rows by PK so the caller can update the grid.
	// Distinct-PK list preserves first-seen order.
	refRes, err := refetchByPK(ctx, sess, qualified, pkCols, out.ResultPKs)
	if err != nil {
		return out, nil,
			fmt.Errorf("cell apply: refetch after commit: %w", err)
	}
	for _, pk := range out.ResultPKs {
		row, ok := refetchedRow(refRes, pkCols, pk)
		if !ok {
			return out, nil, errors.New("cell apply: committed, but refreshed row identity is missing or ambiguous")
		}
		out.RefetchedRows = append(out.RefetchedRows, row)
	}
	out.RefetchedColumns = refRes.cols
	out.RefetchedOriginalPKs = out.OriginalPKs
	return out, nil, nil
}

func refetchedRow(ref refetchResult, pkCols []string, pk []any) (models.Row, bool) {
	indexes := make([]int, len(pkCols))
	for i, name := range pkCols {
		indexes[i] = -1
		for j, col := range ref.cols {
			if col.Name == name {
				indexes[i] = j
				break
			}
		}
		if indexes[i] < 0 {
			return models.Row{}, false
		}
	}
	var found models.Row
	matches := 0
	for _, row := range ref.rows {
		key := make([]any, len(pkCols))
		valid := true
		for i, j := range indexes {
			if j >= len(row.Values) {
				valid = false
				break
			}
			key[i] = row.Values[j]
		}
		if valid && pkSliceEqual(key, pk) {
			found = row
			matches++
		}
	}
	return found, matches == 1
}

// Overwrite is like Apply but issues PK-only UPDATEs (no IS NOT DISTINCT
// FROM guard). Used by the conflict dialog's `[o]` handler on
// non-confirm_writes connections — the user has explicitly chosen to let
// the staged NewValues land over server drift.
//
// Behavioural differences from Apply:
//   - No dry-run mode (callers reach here only after the user accepted
//     the overwrite).
//   - No conflict collection: a RowsAffected==0 statement means the row
//     was deleted between conflict display and overwrite. We rollback and
//     surface it as an error so the caller can re-fetch.
//   - Same refetch-by-PK on success so the grid can update.
func (h *CellApplyHelper) Overwrite(
	ctx context.Context,
	set *models.PendingEditSet,
	pkCols []string,
) (ApplyResult, error) {
	out, _, err := h.apply(ctx, set, pkCols, false, true)
	return out, err
}

// fetchServerValue issues a parameterized SELECT for a single column on a
// single PK tuple. Returns the current value, or an error if the row
// has been deleted (zero rows) — the deletion case still produces a
// nil ServerValue (NULL semantics) so the conflict dialog can render
// "row missing".
func fetchServerValue(
	ctx context.Context,
	sess drivers.Session,
	qualified string,
	pkCols []string,
	e models.PendingEdit,
) (any, error) {
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(pg.QuoteIdent(e.Column))
	b.WriteString(" FROM ")
	b.WriteString(qualified)
	b.WriteString(" WHERE ")
	args := make([]any, 0, len(pkCols))
	for i, pkc := range pkCols {
		if i > 0 {
			b.WriteString(" AND ")
		}
		args = append(args, e.PrimaryKey[i])
		b.WriteString(pg.QuoteIdent(pkc))
		fmt.Fprintf(&b, " = $%d", len(args))
	}

	res, err := sess.Execute(ctx, models.Query{SQL: b.String(), Args: args})
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	if len(res.Rows[0].Values) == 0 {
		return nil, nil
	}
	return res.Rows[0].Values[0], nil
}

// refetchResult is the projection returned by refetchByPK.
type refetchResult struct {
	cols []models.ColumnMeta
	rows []models.Row
}

// refetchByPK issues a single parameterized SELECT * WHERE (pkCols) IN
// (($1,$2,…), ($3,$4,…), …) so the grid can update the touched rows
// without a full reload. For single-column PKs we use IN ($1,$2,…);
// for composite PKs we use the row-constructor form (pkCols) IN
// ((...), (...)) — both forms are parameter-bound, never string-
// concatenated.
func refetchByPK(
	ctx context.Context,
	sess drivers.Session,
	qualified string,
	pkCols []string,
	pks [][]any,
) (refetchResult, error) {
	if len(pks) == 0 {
		return refetchResult{}, nil
	}

	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(qualified)
	b.WriteString(" WHERE ")

	args := make([]any, 0, len(pks)*len(pkCols))
	if len(pkCols) == 1 {
		b.WriteString(pg.QuoteIdent(pkCols[0]))
		b.WriteString(" IN (")
		for i, pk := range pks {
			if i > 0 {
				b.WriteString(", ")
			}
			args = append(args, pk[0])
			fmt.Fprintf(&b, "$%d", len(args))
		}
		b.WriteString(")")
	} else {
		b.WriteString("(")
		for i, pkc := range pkCols {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pg.QuoteIdent(pkc))
		}
		b.WriteString(") IN (")
		for i, pk := range pks {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("(")
			for j, v := range pk {
				if j > 0 {
					b.WriteString(", ")
				}
				args = append(args, v)
				fmt.Fprintf(&b, "$%d", len(args))
			}
			b.WriteString(")")
		}
		b.WriteString(")")
	}

	res, err := sess.Execute(ctx, models.Query{SQL: b.String(), Args: args})
	if err != nil {
		return refetchResult{}, err
	}

	cols := make([]models.ColumnMeta, 0, len(res.Columns))
	for _, c := range res.Columns {
		if c == nil {
			continue
		}
		cols = append(cols, models.ColumnMeta{
			Name:     c.Name,
			TypeName: c.DataType,
		})
	}

	rows := make([]models.Row, 0, len(res.Rows))
	for _, r := range res.Rows {
		if r == nil {
			continue
		}
		rows = append(rows, *r)
	}
	return refetchResult{cols: cols, rows: rows}, nil
}

// Identity values originate in the same driver projection. Text formatting
// would conflate distinct array/string keys; keep their typed structure.
func pkSliceEqual(a, b []any) bool {
	return models.RowIdentityEqual(a, b)
}

// rollback issues a best-effort ROLLBACK on sess. Errors are swallowed —
// the caller is already returning an error or conflicts to the user;
// the rollback failure (e.g. connection dead) does not change the
// outcome semantically (the transaction is gone either way).
func rollback(ctx context.Context, sess drivers.Session) {
	_, _ = sess.Execute(ctx, models.Query{SQL: "ROLLBACK"})
}
