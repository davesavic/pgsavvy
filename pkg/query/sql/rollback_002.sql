-- OFFLINE ONLY: stop every writer and first make a WAL-consistent backup
-- (SQLite backup/VACUUM INTO, not a copy of only the live main database).
-- Keep that backup: CLI/TUI source and blocked/failed distinctions are LOST.
-- With sqlite3, enable .bail on before .read; on any error ROLLBACK or close
-- the connection, never continue to COMMIT. No automatic downgrade uses this.
BEGIN IMMEDIATE;

CREATE TEMP TABLE _history_v2_rollback_guard (ok INTEGER);
CREATE TEMP TRIGGER _history_v2_rollback_check
BEFORE INSERT ON _history_v2_rollback_guard WHEN NEW.ok IS NOT 1
BEGIN
    SELECT RAISE(ROLLBACK, 'history rollback requires the exact v2 schema');
END;
INSERT INTO _history_v2_rollback_guard
SELECT
    (SELECT COUNT(*) = 2 AND MIN(version) = 1 AND MAX(version) = 2 FROM _migrations)
    AND (SELECT COUNT(*) = 9 AND SUM(name IN (
        'id', 'executed_at', 'sql', 'duration_ms', 'rows_affected', 'succeeded',
        'connection_id', 'source', 'blocked'
    )) = 9 FROM pragma_table_info('history'));
DROP TRIGGER _history_v2_rollback_check;
DROP TABLE _history_v2_rollback_guard;

ALTER TABLE history DROP COLUMN blocked;
ALTER TABLE history DROP COLUMN source;
DELETE FROM _migrations WHERE version = 2;
COMMIT;
