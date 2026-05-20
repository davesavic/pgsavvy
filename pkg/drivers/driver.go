package drivers

import (
	"context"

	"github.com/davesavic/dbsavvy/pkg/models"
)

// ConnectionProfile is the connection-profile shape consumed by Driver.Open.
// Per epic dbsavvy-921 D10 it is an alias of models.Connection (no separate
// type) so that drivers receive plain configuration data via the same struct
// the config loader produces.
type ConnectionProfile = models.Connection

// Factory constructs a Driver. Implementations live in concrete driver
// packages (e.g. pkg/drivers/pg) and are passed to Register from main.go;
// pkg/drivers itself imports no concrete driver (epic dbsavvy-921 D9).
type Factory func(ctx context.Context) (Driver, error)

// Driver is the per-engine entry point. See DESIGN.md §11.1.
type Driver interface {
	Name() string
	Capabilities() Capabilities
	Open(ctx context.Context, profile ConnectionProfile) (Connection, error)
}

// Connection is a live handle to a database server. A Connection owns a
// connection pool; sessions check out from it via AcquireSession. Cancel is
// served from a separate connection so it works while a query is in flight.
// See DESIGN.md §11.1.
type Connection interface {
	Close() error
	Ping(ctx context.Context) error
	ServerVersion() string

	AcquireSession(ctx context.Context) (Session, error)

	Cancel(ctx context.Context, queryID models.QueryID) error
}

// Session is a stateful checkout of a Connection that holds transaction
// state, search_path, prepared statements, etc. Session methods are NOT safe
// for concurrent use by multiple goroutines — callers must serialize. See
// DESIGN.md §11.1 and epic dbsavvy-921 D18.
type Session interface {
	Close() error
	ID() models.SessionID

	ListDatabases(ctx context.Context) ([]models.Database, error)
	ListSchemas(ctx context.Context, db string) ([]models.Schema, error)
	ListTables(ctx context.Context, schema string) ([]*models.Table, error)
	ListColumns(ctx context.Context, schema, table string) ([]models.Column, error)
	ListIndexes(ctx context.Context, schema, table string) ([]models.Index, error)
	ListConstraints(ctx context.Context, schema, table string) ([]models.Constraint, error)
	DescribeFunction(ctx context.Context, schema, name string) (models.FunctionDetail, error)

	Execute(ctx context.Context, q models.Query) (models.Result, error)
	Stream(ctx context.Context, q models.Query) (RowStream, error)
	Explain(ctx context.Context, q models.Query, analyze bool) (models.Plan, error)

	Begin(ctx context.Context, opts models.TxOptions) (Transaction, error)
	InTransaction() bool
	CurrentTransaction() Transaction

	// Encoder returns the literal encoder for this session. It is a
	// singleton owned by the session; the returned value is safe to retain
	// for the session's lifetime. Encoder() lives here (per epic
	// dbsavvy-uv0 AD-3) rather than on Driver because literal encoding
	// can depend on session-scoped GUCs (standard_conforming_strings,
	// server_encoding).
	Encoder() Encoder
}

// Transaction is an in-progress transaction on a Session. See DESIGN.md §11.1.
type Transaction interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
	Savepoint(ctx context.Context, name string) error
	Status() models.TxStatus
}

// RowStream is a forward-only iterator over a result set. See DESIGN.md §11.1.
type RowStream interface {
	Columns() []models.ColumnMeta
	Next(ctx context.Context) (models.Row, bool, error)
	Close() error
	QueryID() models.QueryID
}

// Capabilities advertises the static feature flags a driver exposes; UI code
// branches on these (and NEVER on the driver name). HasLiveCancel starts
// false in the v1 Postgres driver and flips true when pg_cancel_backend
// lands in epic E6 (see dbsavvy-921 D17). See DESIGN.md §11.1.
type Capabilities struct {
	HasSchemas           bool
	HasMaterializedViews bool
	HasArrayTypes        bool
	HasJSONTypes         bool
	HasLiveCancel        bool
	HasExplainAnalyze    bool
	HasNotice            bool
	HasListenNotify      bool
	SupportsCursor       bool
	MaxIdentifierLen     int
}
