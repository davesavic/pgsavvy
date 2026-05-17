-- list_databases — Postgres 17 catalog. Maps to models.Database. See pkg/drivers/pg/sql_test.go and DESIGN.md §11.3.
SELECT d.datname, r.rolname, pg_encoding_to_char(d.encoding) AS encoding
FROM pg_database d
LEFT JOIN pg_roles r ON d.datdba = r.oid
WHERE d.datname NOT IN ('template0','template1')
ORDER BY d.datname
