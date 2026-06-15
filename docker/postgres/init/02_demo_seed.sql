-- Demo seed data for manual testing of the result grid (pagination,
-- sort, filter, hide-cols, expanded view, export).
--
-- Idempotent: every INSERT uses ON CONFLICT DO NOTHING so the file is
-- safe to re-run against an already-seeded database via:
--   docker exec -i pgsavvy-postgres psql -U pgsavvy -d pgsavvy_test \
--       < docker/postgres/init/02_demo_seed.sql
--
-- Does NOT bump app._fixture_meta.version (no schema changes, only data).
-- Integration tests do not assert row counts, so the larger dataset is
-- transparent to test/integration.

SET client_min_messages = WARNING;

-- 50 users total (3 already seeded by 01_fixture.sql).
INSERT INTO app.users (id, email, name, created_at, tags, data)
SELECT
    i,
    'user' || i || '@example.com',
    CASE i % 5
        WHEN 0 THEN 'Alex ' || i
        WHEN 1 THEN 'Blair ' || i
        WHEN 2 THEN 'Casey ' || i
        WHEN 3 THEN 'Drew ' || i
        ELSE       'Erin ' || i
    END,
    TIMESTAMPTZ '2025-01-01 00:00:00+00' + (i || ' hours')::interval,
    CASE i % 4
        WHEN 0 THEN ARRAY['viewer']
        WHEN 1 THEN ARRAY['editor','contributor']
        WHEN 2 THEN ARRAY['admin','founder','beta']
        ELSE       NULL
    END,
    CASE i % 3
        WHEN 0 THEN jsonb_build_object('plan', 'free',  'seats', 1)
        WHEN 1 THEN jsonb_build_object('plan', 'pro',   'seats', i % 10 + 1)
        ELSE       NULL
    END
FROM generate_series(4, 50) AS i
ON CONFLICT (id) DO NOTHING;

-- 500 posts total (3 already seeded). user_id cycles 1..50 so every
-- demo user owns ~10 posts. Mix of published / unpublished.
INSERT INTO app.posts (id, user_id, title, body, published, published_at)
SELECT
    i,
    ((i - 1) % 50) + 1,
    'Post #' || i || ' — ' ||
        CASE i % 7
            WHEN 0 THEN 'On performance'
            WHEN 1 THEN 'Notes from the field'
            WHEN 2 THEN 'A short essay'
            WHEN 3 THEN 'Quick thoughts'
            WHEN 4 THEN 'Retro'
            WHEN 5 THEN 'Deep dive'
            ELSE       'Misc'
        END,
    CASE WHEN i % 4 = 0 THEN NULL
         ELSE 'Body for post ' || i || '. ' ||
              repeat('Lorem ipsum dolor sit amet. ', (i % 3) + 1)
    END,
    (i % 3 <> 0),
    CASE WHEN (i % 3 <> 0)
         THEN TIMESTAMPTZ '2026-01-01 00:00:00+00' + (i || ' minutes')::interval
         ELSE NULL
    END
FROM generate_series(4, 500) AS i
ON CONFLICT (id) DO NOTHING;

-- Spread role assignments across the new users (every 5th user gets a role).
INSERT INTO app.user_roles (user_id, role_id)
SELECT i, ((i - 1) % 3) + 1
FROM generate_series(4, 50) AS i
WHERE i % 5 = 0
ON CONFLICT (user_id, role_id) DO NOTHING;

REFRESH MATERIALIZED VIEW app.posts_summary;
