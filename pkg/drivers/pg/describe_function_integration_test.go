//go:build integration

// Integration tests for Session.DescribeFunction against the docker/postgres
// fixture. The fixture seeds an overloaded pair app.fn_overload(int) and
// app.fn_overload(text, text). Mirrors the
// openIntegrationSession pattern. Skipped (not failed) when DBSAVVY_TEST_PG
// is unset.

package pg_test

import (
	"context"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestDescribeFunction_OverloadedReturnsAllOverloads(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	got, err := sess.DescribeFunction(ctx, "app", "fn_overload")
	if err != nil {
		t.Fatalf("DescribeFunction: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 overloads for app.fn_overload, got %d: %+v", len(got), got)
	}

	// Index the overloads by arg count so the test is order-independent.
	byArity := map[int]models.FunctionDetail{}
	for _, fd := range got {
		byArity[len(fd.Args)] = fd
	}

	one, ok := byArity[1]
	if !ok {
		t.Fatalf("missing single-arg overload; got %+v", got)
	}
	if one.ReturnType != "integer" {
		t.Errorf("single-arg overload return type = %q, want integer", one.ReturnType)
	}
	if one.Volatility != "IMMUTABLE" {
		t.Errorf("single-arg overload volatility = %q, want IMMUTABLE", one.Volatility)
	}
	if one.Language != "sql" {
		t.Errorf("single-arg overload language = %q, want sql", one.Language)
	}
	if one.Args[0].Name != "a" || one.Args[0].Type != "integer" || one.Args[0].Mode != "IN" {
		t.Errorf("single-arg overload arg = %+v, want {a integer IN}", one.Args[0])
	}

	two, ok := byArity[2]
	if !ok {
		t.Fatalf("missing two-arg overload; got %+v", got)
	}
	if two.ReturnType != "text" {
		t.Errorf("two-arg overload return type = %q, want text", two.ReturnType)
	}
	if two.Volatility != "STABLE" {
		t.Errorf("two-arg overload volatility = %q, want STABLE", two.Volatility)
	}
	for i, a := range two.Args {
		if a.Type != "text" || a.Mode != "IN" {
			t.Errorf("two-arg overload arg %d = %+v, want type text mode IN", i, a)
		}
	}
}

func TestDescribeFunction_NonExistentReturnsEmptyNilError(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	got, err := sess.DescribeFunction(ctx, "app", "does_not_exist_fn")
	if err != nil {
		t.Fatalf("DescribeFunction non-existent: unexpected error %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice for non-existent function, got %+v", got)
	}
}

func TestDescribeFunction_ZeroArgReturnsEmptyArgs(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	stmts := []string{
		`DROP SCHEMA IF EXISTS describefn_zeroarg CASCADE`,
		`CREATE SCHEMA describefn_zeroarg`,
		`CREATE FUNCTION describefn_zeroarg.noargs() RETURNS int LANGUAGE sql AS $$ SELECT 1 $$`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS describefn_zeroarg CASCADE`})
	})

	got, err := sess.DescribeFunction(ctx, "describefn_zeroarg", "noargs")
	if err != nil {
		t.Fatalf("DescribeFunction zero-arg: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 overload, got %d", len(got))
	}
	if len(got[0].Args) != 0 {
		t.Fatalf("expected empty Args for zero-arg function, got %+v", got[0].Args)
	}
}

func TestDescribeFunction_UnnamedArgHasEmptyName(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// SQL functions declared with bare types (no parameter names) leave
	// proargnames NULL, which must surface as an empty FunctionArg.Name.
	stmts := []string{
		`DROP SCHEMA IF EXISTS describefn_unnamed CASCADE`,
		`CREATE SCHEMA describefn_unnamed`,
		`CREATE FUNCTION describefn_unnamed.unnamed(int) RETURNS int LANGUAGE sql AS $$ SELECT $1 $$`,
	}
	for _, s := range stmts {
		if _, err := sess.Execute(ctx, models.Query{SQL: s}); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	t.Cleanup(func() {
		_, _ = sess.Execute(ctx, models.Query{SQL: `DROP SCHEMA IF EXISTS describefn_unnamed CASCADE`})
	})

	got, err := sess.DescribeFunction(ctx, "describefn_unnamed", "unnamed")
	if err != nil {
		t.Fatalf("DescribeFunction unnamed-arg: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 overload, got %d", len(got))
	}
	if len(got[0].Args) != 1 {
		t.Fatalf("expected 1 arg, got %+v", got[0].Args)
	}
	if got[0].Args[0].Name != "" {
		t.Errorf("unnamed arg Name = %q, want empty", got[0].Args[0].Name)
	}
	if got[0].Args[0].Type != "integer" || got[0].Args[0].Mode != "IN" {
		t.Errorf("unnamed arg = %+v, want type integer mode IN", got[0].Args[0])
	}
}

func TestDescribeFunction_SingleQuoteNameIsInert(t *testing.T) {
	sess := openIntegrationSession(t)
	ctx := context.Background()

	// A name containing a single quote must be bound as a param ($2) and is
	// therefore inert — it simply matches nothing. No injection, no error.
	got, err := sess.DescribeFunction(ctx, "app", "fn'; DROP TABLE app.users; --")
	if err != nil {
		t.Fatalf("DescribeFunction with quote in name: unexpected error %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice for bogus quoted name, got %+v", got)
	}
}
