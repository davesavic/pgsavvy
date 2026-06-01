-- Resolve a set of relation OIDs to their unqualified relation names.
-- $1 is an oid[]; each row is a pg_class entry whose oid is in the set.
-- OIDs with no matching relation simply yield no row. Consumed by the
-- hide-columns overlay to qualify ambiguous join column labels.
SELECT c.oid,
       c.relname
FROM pg_catalog.pg_class c
WHERE c.oid = ANY ($1::oid[]);
