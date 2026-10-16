SELECT 'schemas'::text,
       pg_catalog.jsonb_build_object('name', n.nspname::text)
FROM pg_catalog.pg_namespace AS n
WHERE n.nspname::text COLLATE "C" > $1::text COLLATE "C"
ORDER BY n.nspname::text COLLATE "C"
LIMIT $2::integer
