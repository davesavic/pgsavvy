-- list_schemas — Postgres 17 catalog. Maps to models.Schema. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT n.nspname, r.rolname
FROM pg_namespace n
LEFT JOIN pg_roles r ON n.nspowner = r.oid
ORDER BY n.nspname
