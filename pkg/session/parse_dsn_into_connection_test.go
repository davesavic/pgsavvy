package session

import (
	"strings"
	"testing"
)

// TestParseDSNIntoConnection covers the migration parser that turns a legacy
// DSN into discrete Connection fields. It asserts URL-form and kv-form happy
// paths (including sslmode extraction), the typed error on empty/garbage
// input, and — critically — that the password is NEVER populated.
func TestParseDSNIntoConnection(t *testing.T) {
	t.Run("url form with sslmode=require", func(t *testing.T) {
		c, err := ParseDSNIntoConnection("postgres://app:secret@db.example.com:5433/appdb?sslmode=require")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if c.Host != "db.example.com" {
			t.Errorf("Host = %q, want db.example.com", c.Host)
		}
		if c.Port != 5433 {
			t.Errorf("Port = %d, want 5433", c.Port)
		}
		if c.User != "app" {
			t.Errorf("User = %q, want app", c.User)
		}
		if c.Database != "appdb" {
			t.Errorf("Database = %q, want appdb", c.Database)
		}
		if c.SSLMode != "require" {
			t.Errorf("SSLMode = %q, want require", c.SSLMode)
		}
		if c.Password != "" {
			t.Errorf("Password = %q, want empty (must be dropped)", c.Password)
		}
	})

	t.Run("kv form", func(t *testing.T) {
		c, err := ParseDSNIntoConnection("host=myhost port=6000 user=x dbname=mydb password=y sslmode=verify-full")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if c.Host != "myhost" {
			t.Errorf("Host = %q, want myhost", c.Host)
		}
		if c.Port != 6000 {
			t.Errorf("Port = %d, want 6000", c.Port)
		}
		if c.User != "x" {
			t.Errorf("User = %q, want x", c.User)
		}
		if c.Database != "mydb" {
			t.Errorf("Database = %q, want mydb", c.Database)
		}
		if c.SSLMode != "verify-full" {
			t.Errorf("SSLMode = %q, want verify-full", c.SSLMode)
		}
		if c.Password != "" {
			t.Errorf("Password = %q, want empty (must be dropped)", c.Password)
		}
	})

	t.Run("empty dsn returns typed error", func(t *testing.T) {
		_, err := ParseDSNIntoConnection("")
		if err == nil {
			t.Fatal("expected error for empty dsn")
		}
	})

	t.Run("garbage dsn returns error without leaking password", func(t *testing.T) {
		_, err := ParseDSNIntoConnection("postgres://app:topsecret@h:notaport/db")
		if err == nil {
			t.Fatal("expected parse error")
		}
		if strings.Contains(err.Error(), "topsecret") {
			t.Fatalf("password leaked in error: %v", err)
		}
	})
}

// TestParseDSNIntoConnection_RoundTripsThroughBuildKVDSN asserts the parser is
// the inverse of buildKVDSN for the discrete fields (sans password).
func TestParseDSNIntoConnection_RoundTripsThroughBuildKVDSN(t *testing.T) {
	c, err := ParseDSNIntoConnection("postgres://app@db.example.com:5432/appdb?sslmode=disable")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	kv := buildKVDSN(c)
	back, err := ParseDSNIntoConnection(kv)
	if err != nil {
		t.Fatalf("re-parse of %q: %v", kv, err)
	}
	if back.Host != c.Host || back.Port != c.Port || back.User != c.User ||
		back.Database != c.Database || back.SSLMode != c.SSLMode {
		t.Errorf("round-trip mismatch: %+v vs %+v (via %q)", c, back, kv)
	}
}
