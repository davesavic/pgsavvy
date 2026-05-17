-- list_tables — Postgres 17 catalog. Maps to models.Table. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT
    n.nspname AS schema,
    c.relname AS name,
    CASE c.relkind
        WHEN 'r' THEN 'table'
        WHEN 'm' THEN 'materialized_view'
        WHEN 'v' THEN 'view'
        WHEN 'p' THEN 'partitioned_table'
    END AS kind,
    r.rolname AS owner,
    obj_description(c.oid, 'pg_class') AS description
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_roles r ON r.oid = c.relowner
WHERE c.relkind IN ('r','m','v','p')
ORDER BY n.nspname, c.relname
