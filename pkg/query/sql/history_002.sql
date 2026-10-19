-- Defaults preserve both existing rows and inserts by already-running v1 writers.
-- No CHECK constraints, metadata backfill or FTS rewrite: ALTER is schema-only.
ALTER TABLE history ADD COLUMN source TEXT NOT NULL DEFAULT 'tui';
ALTER TABLE history ADD COLUMN blocked INTEGER NOT NULL DEFAULT 0;
