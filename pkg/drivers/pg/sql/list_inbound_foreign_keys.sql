-- list_inbound_foreign_keys — Postgres 17 catalog. Enumerates every FK
-- whose REFERENCED table is (schema=$1, table=$2). Returned columns match
-- the ListForeignKeys shape (models.ForeignKey scan order); ref_* point at
-- the input table, Schema/Table/Columns point at the REFERENCING side.
-- See pkg/drivers/pg/fk_loader.go (B6) and DESIGN.md §12.6 reverse gD.
SELECT
    con.conname AS name,
    n.nspname AS schema,
    c.relname AS table_name,
    (
        SELECT COALESCE(array_agg(att.attname ORDER BY ord), ARRAY[]::text[])
        FROM unnest(con.conkey) WITH ORDINALITY AS u(attnum, ord)
        JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = u.attnum
    ) AS columns,
    rn.nspname AS ref_schema,
    rc.relname AS ref_table,
    (
        SELECT COALESCE(array_agg(att.attname ORDER BY ord), ARRAY[]::text[])
        FROM unnest(con.confkey) WITH ORDINALITY AS u(attnum, ord)
        JOIN pg_attribute att ON att.attrelid = con.confrelid AND att.attnum = u.attnum
    ) AS ref_columns,
    CASE con.confupdtype
        WHEN 'a' THEN 'NO ACTION'
        WHEN 'r' THEN 'RESTRICT'
        WHEN 'c' THEN 'CASCADE'
        WHEN 'n' THEN 'SET NULL'
        WHEN 'd' THEN 'SET DEFAULT'
        ELSE con.confupdtype::text
    END AS on_update,
    CASE con.confdeltype
        WHEN 'a' THEN 'NO ACTION'
        WHEN 'r' THEN 'RESTRICT'
        WHEN 'c' THEN 'CASCADE'
        WHEN 'n' THEN 'SET NULL'
        WHEN 'd' THEN 'SET DEFAULT'
        ELSE con.confdeltype::text
    END AS on_delete
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_class rc ON rc.oid = con.confrelid
JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.contype = 'f'
  AND rn.nspname = $1
  AND rc.relname = $2
ORDER BY n.nspname, c.relname, con.conname
