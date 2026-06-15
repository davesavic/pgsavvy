package context

import (
	"testing"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestForm_AddSeedsHostAndPortDefaults asserts the Add form pre-fills the host
// and port so a newcomer can connect to a local Postgres by only supplying
// User/Database (T2 / decision 1).
func TestForm_AddSeedsHostAndPortDefaults(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	if c.form.conn.Host != "localhost" {
		t.Errorf("default Host = %q, want localhost", c.form.conn.Host)
	}
	if c.form.conn.Port != 5432 {
		t.Errorf("default Port = %d, want 5432", c.form.conn.Port)
	}
	// No other discrete value is seeded.
	if c.form.conn.User != "" || c.form.conn.Database != "" || c.form.conn.SSLMode != "" {
		t.Errorf("unexpected discrete seed: %+v", c.form.conn)
	}
}

// TestForm_DiscreteFieldRoundTrip asserts the discrete text rows store onto the
// model and read back, and that an empty/invalid port clears to 0.
func TestForm_DiscreteFieldRoundTrip(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)

	c.form.setTextValue(fieldHost, "db.example.com")
	c.form.setTextValue(fieldUser, "app")
	c.form.setTextValue(fieldDatabase, "appdb")
	c.form.setTextValue(fieldPort, "6000")

	if c.form.conn.Host != "db.example.com" || c.form.conn.User != "app" ||
		c.form.conn.Database != "appdb" || c.form.conn.Port != 6000 {
		t.Fatalf("discrete fields not stored: %+v", c.form.conn)
	}
	if got := c.form.textValue(fieldPort); got != "6000" {
		t.Errorf("textValue(fieldPort) = %q, want 6000", got)
	}

	// Empty port clears to 0 (unset → pgx default at connect time).
	c.form.setTextValue(fieldPort, "")
	if c.form.conn.Port != 0 {
		t.Errorf("empty port = %d, want 0", c.form.conn.Port)
	}
	if got := c.form.textValue(fieldPort); got != "" {
		t.Errorf("textValue(fieldPort) for 0 = %q, want empty", got)
	}
}

// TestForm_PortValidatorRejectsNonNumeric asserts the port row is gated by the
// 1-65535 validator (shared with the SSH port row); empty is allowed.
func TestForm_PortValidatorRejectsNonNumeric(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	v := c.form.validatorFor(fieldPort, tr)
	if v == nil {
		t.Fatal("fieldPort has no validator")
	}
	if err := v("abc"); err == nil {
		t.Error("non-numeric port accepted")
	}
	if err := v("70000"); err == nil {
		t.Error("out-of-range port accepted")
	}
	if err := v(""); err != nil {
		t.Errorf("empty port rejected: %v", err)
	}
	if err := v("5432"); err != nil {
		t.Errorf("valid port rejected: %v", err)
	}
}

// TestForm_SSLModeSelectorCycles asserts the sslmode selector cycles the libpq
// option list and wraps last→first, starting from the "(default)" empty state.
func TestForm_SSLModeSelectorCycles(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)

	if c.form.conn.SSLMode != "" {
		t.Fatalf("initial SSLMode = %q, want empty", c.form.conn.SSLMode)
	}
	spec := connFieldSpec{id: fieldSSLMode, kind: fieldSelector}
	if got := c.form.displayValue(spec); got != "(default)" {
		t.Errorf("empty sslmode display = %q, want (default)", got)
	}

	want := []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}
	for _, w := range want {
		c.form.toggle(fieldSSLMode)
		if c.form.conn.SSLMode != w {
			t.Fatalf("cycle to %q, got %q", w, c.form.conn.SSLMode)
		}
	}
	// Wrap from the last option back to the first.
	c.form.toggle(fieldSSLMode)
	if c.form.conn.SSLMode != "disable" {
		t.Errorf("after wrap SSLMode = %q, want disable", c.form.conn.SSLMode)
	}
}

// TestForm_OpenEditLazyParsesLegacyDSN asserts that opening a legacy dsn-only
// profile for edit populates the discrete rows from the parsed DSN (decision 8).
func TestForm_OpenEditLazyParsesLegacyDSN(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	legacy := models.Connection{Name: "legacy", Driver: "postgres", DSN: "postgres://app@db.example.com:5432/appdb?sslmode=require"}
	c.OpenEditForm(legacy, nil, testDrivers)

	got := c.form.conn
	if got.Host != "db.example.com" || got.Port != 5432 || got.User != "app" ||
		got.Database != "appdb" || got.SSLMode != "require" {
		t.Fatalf("lazy parse did not populate discrete fields: %+v", got)
	}
}

// TestForm_BackwardCompatDemoFixtureLoads loads the repo's real demo
// connections.yml (a dsn-only profile) through the production loader and the
// edit form, proving legacy files still load and migrate into discrete rows.
func TestForm_BackwardCompatDemoFixtureLoads(t *testing.T) {
	const fixture = "../../../.demo/config/pgsavvy/connections.yml"
	conns, err := config.LoadConnections(afero.NewOsFs(), fixture)
	if err != nil {
		t.Fatalf("LoadConnections(%s): %v", fixture, err)
	}
	if len(conns) == 0 {
		t.Fatalf("demo fixture %s has no connections", fixture)
	}

	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenEditForm(conns[0], nil, testDrivers)
	got := c.form.conn
	if got.Host == "" && got.DSN == "" {
		t.Fatalf("demo profile opened with neither host nor dsn: %+v", got)
	}
	// The demo dsn is postgres://pgsavvy@localhost:5432/pgsavvy_test?sslmode=disable.
	if got.Host != "localhost" || got.Database != "pgsavvy_test" {
		t.Errorf("demo discrete migration = host %q db %q, want localhost/pgsavvy_test", got.Host, got.Database)
	}
}
