package session

import (
	"strings"
	"testing"
)

// TestParseDSNEndpoint covers the display-only host/database extraction used
// to enrich connection-picker rows. It asserts the happy
// paths (URL-form and keyword/value-form) and the safe fallbacks, and — for
// the URL-form case — that NEITHER the password NOR the user leaks into the
// returned fields.
func TestParseDSNEndpoint(t *testing.T) {
	t.Run("url form returns host and db without credentials", func(t *testing.T) {
		host, db := ParseDSNEndpoint("postgres://u:secret@h:5432/db")
		if host != "h" {
			t.Fatalf("host = %q, want %q", host, "h")
		}
		if db != "db" {
			t.Fatalf("db = %q, want %q", db, "db")
		}
		if strings.Contains(host+db, "secret") {
			t.Fatalf("endpoint %q/%q leaked the password", host, db)
		}
		if strings.Contains(host+db, "u") {
			t.Fatalf("endpoint %q/%q leaked the user", host, db)
		}
	})

	t.Run("malformed dsn returns empty without panic", func(t *testing.T) {
		host, db := ParseDSNEndpoint("postgres://{bad}")
		if host != "" || db != "" {
			t.Fatalf("malformed dsn -> (%q,%q), want empty", host, db)
		}
	})

	t.Run("kv form returns host and db", func(t *testing.T) {
		host, db := ParseDSNEndpoint("host=myhost dbname=mydb user=x password=y sslmode=disable")
		if host != "myhost" {
			t.Fatalf("host = %q, want %q", host, "myhost")
		}
		if db != "mydb" {
			t.Fatalf("db = %q, want %q", db, "mydb")
		}
	})

	t.Run("empty dsn returns empty", func(t *testing.T) {
		host, db := ParseDSNEndpoint("")
		if host != "" || db != "" {
			t.Fatalf("empty dsn -> (%q,%q), want empty", host, db)
		}
	})
}
