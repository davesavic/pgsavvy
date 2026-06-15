-- pgsavvy integration-test fixture.
--
-- Exercises the introspection surface (pkg/drivers/pg loaders):
--   schemas, tables, materialized view, view, columns w/ text[]+jsonb,
--   B-tree / partial / GIN indexes, PK / UNIQUE / FK / CHECK / NOT NULL,
--   COMMENT ON TABLE.
--
-- Fixture is versioned via app._fixture_meta.version. Integration tests in
-- test/integration/ assert the version matches a constant they pin; bump
-- both when the fixture schema changes.

SET client_min_messages = WARNING;

CREATE SCHEMA app;
CREATE SCHEMA reporting;

COMMENT ON SCHEMA app IS 'Primary application objects used by integration tests';
COMMENT ON SCHEMA reporting IS 'Reserved for cross-schema introspection tests';

-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

CREATE TABLE app.users (
    id          BIGINT PRIMARY KEY,
    email       TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    tags        TEXT[],
    data        JSONB
);
COMMENT ON TABLE app.users IS 'Users fixture (text[] + jsonb columns).';

CREATE TABLE app.roles (
    id    BIGINT PRIMARY KEY,
    name  TEXT NOT NULL UNIQUE
);
COMMENT ON TABLE app.roles IS 'Roles fixture (UNIQUE constraint on name).';

CREATE TABLE app.user_roles (
    user_id  BIGINT NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
    role_id  BIGINT NOT NULL REFERENCES app.roles(id),
    PRIMARY KEY (user_id, role_id)
);
COMMENT ON TABLE app.user_roles IS 'Join table (composite PK + two FKs, one CASCADE).';

CREATE TABLE app.posts (
    id            BIGINT PRIMARY KEY,
    user_id       BIGINT NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
    title         TEXT NOT NULL,
    body          TEXT,
    published     BOOLEAN NOT NULL DEFAULT false,
    published_at  TIMESTAMPTZ,
    CHECK (published = false OR published_at IS NOT NULL)
);
COMMENT ON TABLE app.posts IS 'Posts fixture (CHECK constraint + FK CASCADE).';

-- ---------------------------------------------------------------------------
-- Indexes (B-tree, partial, GIN)
-- ---------------------------------------------------------------------------

CREATE INDEX idx_posts_user_published ON app.posts (user_id, published);
CREATE INDEX idx_posts_published_at   ON app.posts (published_at) WHERE published = true;
CREATE INDEX idx_users_data_gin       ON app.users USING gin (data);

-- ---------------------------------------------------------------------------
-- View + materialized view
-- ---------------------------------------------------------------------------

CREATE VIEW app.published_posts AS
    SELECT id, user_id, title, published_at
    FROM app.posts
    WHERE published = true;
COMMENT ON VIEW app.published_posts IS 'Plain view over published posts.';

CREATE MATERIALIZED VIEW app.posts_summary AS
    SELECT u.id, u.email, COUNT(p.id) AS post_count
    FROM app.users u
    LEFT JOIN app.posts p ON p.user_id = u.id
    GROUP BY u.id, u.email;
COMMENT ON MATERIALIZED VIEW app.posts_summary IS 'Per-user post count (materialized).';

-- ---------------------------------------------------------------------------
-- Seed data (≤10 rows per table)
-- ---------------------------------------------------------------------------

INSERT INTO app.users (id, email, name, tags, data) VALUES
    (1, 'alice@example.com', 'Alice', ARRAY['admin','founder'], '{"plan":"pro"}'::jsonb),
    (2, 'bob@example.com',   'Bob',   ARRAY['editor'],          '{"plan":"free"}'::jsonb),
    (3, 'carol@example.com', 'Carol', NULL,                     NULL);

INSERT INTO app.roles (id, name) VALUES
    (1, 'admin'),
    (2, 'editor'),
    (3, 'viewer');

INSERT INTO app.user_roles (user_id, role_id) VALUES
    (1, 1),
    (1, 2),
    (2, 2),
    (3, 3);

INSERT INTO app.posts (id, user_id, title, body, published, published_at) VALUES
    (1, 1, 'Hello world',   'first post',  true,  '2026-01-02 10:00:00+00'),
    (2, 1, 'Draft thoughts', NULL,         false, NULL),
    (3, 2, 'On editing',    'thoughts...', true,  '2026-02-14 12:00:00+00');

REFRESH MATERIALIZED VIEW app.posts_summary;

-- ---------------------------------------------------------------------------
-- Overloaded function pair for DescribeFunction introspection (pgsavvy-ko4m.5.1)
-- Same schema+name, distinct argument types -> two pg_proc rows. Name is new
-- and not used by ListFunctions de-dup assertions (which use *_marker names).
-- ---------------------------------------------------------------------------

CREATE FUNCTION app.fn_overload(a int) RETURNS int
    LANGUAGE sql IMMUTABLE AS $$ SELECT a $$;

CREATE FUNCTION app.fn_overload(a text, b text) RETURNS text
    LANGUAGE sql STABLE AS $$ SELECT a || b $$;

-- ---------------------------------------------------------------------------
-- Fixture version stamp (read by test/integration TestMain)
-- ---------------------------------------------------------------------------

CREATE TABLE app._fixture_meta (version INT NOT NULL);
INSERT INTO app._fixture_meta (version) VALUES (2);
COMMENT ON TABLE app._fixture_meta IS 'Fixture schema version; bump in lockstep with test constants.';
