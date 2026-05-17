// Package pg implements the Postgres concrete driver: pg.New returns a
// drivers.Factory that yields a *Driver wrapping a session.Prompter, and
// *Connection wraps a pgxpool.Pool with ServerVersion caching plus a
// pg_cancel_backend stub that will be wired in epic E6. See DESIGN.md
// §11.3 and epic dbsavvy-921 (D8, D9, D11, D16-D20).
package pg
