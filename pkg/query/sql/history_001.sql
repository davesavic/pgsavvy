CREATE TABLE IF NOT EXISTS history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    executed_at     INTEGER NOT NULL,
    sql             TEXT    NOT NULL,
    duration_ms     INTEGER NOT NULL,
    rows_affected   INTEGER NOT NULL,
    succeeded       INTEGER NOT NULL,
    connection_id   TEXT    NOT NULL DEFAULT ''
);

CREATE VIRTUAL TABLE IF NOT EXISTS history_fts USING fts5(
    sql,
    content='history',
    content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS history_ai AFTER INSERT ON history BEGIN
    INSERT INTO history_fts(rowid, sql) VALUES (new.id, new.sql);
END;

CREATE TRIGGER IF NOT EXISTS history_ad AFTER DELETE ON history BEGIN
    INSERT INTO history_fts(history_fts, rowid, sql) VALUES('delete', old.id, old.sql);
END;

CREATE TRIGGER IF NOT EXISTS history_au AFTER UPDATE ON history BEGIN
    INSERT INTO history_fts(history_fts, rowid, sql) VALUES('delete', old.id, old.sql);
    INSERT INTO history_fts(history_fts, rowid, sql) VALUES(new.id, new.sql);
END;
