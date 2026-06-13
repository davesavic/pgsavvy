//go:build integration

// Integration tests for Session.ListFunctions against the docker/postgres
// fixture. Mirrors the openIntegrationSession pattern from
// fk_loader_integration_test.go. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset.

package pg_test

import (
	"context"
	"sort"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestListFunctions_ReturnsSortedAndIncludesSeeded(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// Stand up a dedicated schema with two functions and add it to the
	// session's search_path so they appear in current_schemas(false). We
	// drop the schema on cleanup so the live fixture stays pristine.
	stmts := []string{
		`DROP SCHEMA IF EXISTS funcloader_test CASCADE`,
		`CREATE SCHEMA funcloader_test`,
		`CREATE FUNCTION funcloader_test.zzz_marker() RETURNS int AS $$ SELECT 1 $$ LANGUAGE sql`,
		`CREATE FUNCTION funcloader_test.aaa_marker(a int) RETURNS int AS $$ SELECT $1 $$ LANGUAGE sql`,
		`SET search_path TO funcloader_test, public`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `RESET search_path`})
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS funcloader_test CASCADE`})
	})

	names, err := sess.ListFunctions(ctx)
	if err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}
	if names == nil {
		t.Fatal("ListFunctions returned nil slice; want non-nil")
	}

	// Sorted alphabetically.
	if !sort.StringsAreSorted(names) {
		t.Errorf("names not sorted: %v", names)
	}

	// Both seeded functions present.
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	if !got["aaa_marker"] {
		t.Errorf("expected aaa_marker in result; got %v", names)
	}
	if !got["zzz_marker"] {
		t.Errorf("expected zzz_marker in result; got %v", names)
	}
}

func TestListFunctions_OverloadedNameCollapsesToOneEntry(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// The fixture seeds app.fn_overload with two distinct signatures
	// (dbsavvy-ko4m.5.1). ListFunctions projects routine_name alone, so the
	// overloaded pair must collapse to exactly one entry — DescribeFunction is
	// the surface that fans the overloads back out.
	if _, err := sess.Execute(ctx, models.Query{SQL: `SET search_path TO app, public`}); err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `RESET search_path`})
	})

	names, err := sess.ListFunctions(ctx)
	if err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}
	count := 0
	for _, n := range names {
		if n == "fn_overload" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected fn_overload exactly once (overloads de-duped), got %d in %v", count, names)
	}
}

func TestListFunctions_EmptyWhenSearchPathHasNoFunctions(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// pg_temp_* and an empty user schema both reliably contain zero
	// functions; isolate via a fresh empty schema on search_path.
	stmts := []string{
		`DROP SCHEMA IF EXISTS funcloader_empty CASCADE`,
		`CREATE SCHEMA funcloader_empty`,
		`SET search_path TO funcloader_empty`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `RESET search_path`})
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS funcloader_empty CASCADE`})
	})

	names, err := sess.ListFunctions(ctx)
	if err != nil {
		t.Fatalf("ListFunctions: %v", err)
	}
	if names == nil {
		t.Fatal("ListFunctions returned nil slice; want empty non-nil")
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 functions on empty schema; got %v", names)
	}
}
