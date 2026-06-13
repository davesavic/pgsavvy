-- list_functions — returns user-visible FUNCTION names from
-- information_schema.routines limited to the session's current_schemas
-- (non-implicit, i.e. excludes pg_catalog). Powers the completion
-- engine's function source (epic dbsavvy-bwq §13.3, child .20).
-- Names are returned sorted alphabetically; overloaded signatures yield one
-- information_schema.routines row each, so SELECT DISTINCT collapses them to a
-- single entry per name.
SELECT DISTINCT routine_name
FROM information_schema.routines
WHERE routine_schema IN (SELECT unnest(current_schemas(false)))
  AND routine_type = 'FUNCTION'
ORDER BY routine_name
