-- list_indexes — Postgres 17 catalog. Maps to models.Index. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT
    ic.relname AS name,
    n.nspname AS schema,
    tc.relname AS table_name,
    (
        SELECT array_agg(att.attname ORDER BY ord)
        FROM unnest(i.indkey) WITH ORDINALITY AS u(attnum, ord)
        JOIN pg_attribute att ON att.attrelid = i.indrelid AND att.attnum = u.attnum
    ) AS columns,
    i.indisunique,
    i.indisprimary,
    am.amname AS method
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_class tc ON tc.oid = i.indrelid
JOIN pg_namespace n ON n.oid = tc.relnamespace
JOIN pg_am am ON am.oid = ic.relam
WHERE n.nspname = $1
  AND tc.relname = $2
ORDER BY ic.relname
