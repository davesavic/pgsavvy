-- list_constraints — Postgres 17 catalog. Maps to models.Constraint. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT
    con.conname AS name,
    n.nspname AS schema,
    c.relname AS table_name,
    CASE con.contype
        WHEN 'p' THEN 'PRIMARY KEY'
        WHEN 'u' THEN 'UNIQUE'
        WHEN 'f' THEN 'FOREIGN KEY'
        WHEN 'c' THEN 'CHECK'
        WHEN 'n' THEN 'NOT NULL'
        ELSE con.contype::text
    END AS kind,
    (
        SELECT COALESCE(array_agg(att.attname ORDER BY ord), ARRAY[]::text[])
        FROM unnest(con.conkey) WITH ORDINALITY AS u(attnum, ord)
        JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = u.attnum
    ) AS columns,
    pg_get_constraintdef(con.oid) AS definition
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
ORDER BY con.conname
