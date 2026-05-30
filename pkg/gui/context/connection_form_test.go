package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func testDrivers() []string { return []string{"postgres", "mysql"} }

// TestForm_RendersAllFunctionalAndSoonRows asserts the form body shows every
// functional field plus the greyed "(soon)" placeholders (AC1).
func TestForm_RendersAllFunctionalAndSoonRows(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	for _, want := range []string{
		"name:", "driver:", "dsn:", "read_only:", "confirm_writes:",
		"confirm_ddl:", "statement_timeout:", "color:", "label:", "tags:",
		"ssh_tunnel:", "keyring:", "pgpass:", "password_command:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form body missing %q\n%s", want, body)
		}
	}
	if !strings.Contains(body, "(soon)") {
		t.Errorf("form body missing greyed (soon) marker\n%s", body)
	}
}

// TestForm_AddSeedsFirstDriver asserts the add form defaults the driver to the
// first registered name so the selector starts valid.
func TestForm_AddSeedsFirstDriver(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	if got := c.form.conn.Driver; got != "postgres" {
		t.Fatalf("default driver = %q, want postgres", got)
	}
}

// TestForm_FieldNavMovesFocusAndSkipsSoonRows asserts j/k/Tab move the field
// cursor across the ten functional rows and never land on a "(soon)" row
// (AC2).
func TestForm_FieldNavMovesFocusAndSkipsSoonRows(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	// 10 functional rows → focusable count is 10.
	if got := len(c.form.focusableSpecs()); got != 10 {
		t.Fatalf("focusable rows = %d, want 10", got)
	}
	// Move far past the end; clamps to the last functional row (tags).
	for range 50 {
		c.FormMoveFocus(1)
	}
	if id := c.form.focusedSpec().id; id != fieldTags {
		t.Fatalf("focus after over-move = %v, want fieldTags", id)
	}
	// Move far up; clamps to name.
	for range 50 {
		c.FormMoveFocus(-1)
	}
	if id := c.form.focusedSpec().id; id != fieldName {
		t.Fatalf("focus after over-move-up = %v, want fieldName", id)
	}
}

// TestForm_ToggleFlipsBoolAndCyclesDriver asserts FormToggleFocused flips the
// focused toggle and cycles the driver selector.
func TestForm_ToggleFlipsBoolAndCyclesDriver(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)

	// Focus driver row (index 1) and cycle.
	c.FormMoveFocus(1)
	c.FormToggleFocused()
	if got := c.form.conn.Driver; got != "mysql" {
		t.Fatalf("driver after cycle = %q, want mysql", got)
	}
	c.FormToggleFocused()
	if got := c.form.conn.Driver; got != "postgres" {
		t.Fatalf("driver after wrap = %q, want postgres", got)
	}

	// Focus read_only (index 3) and flip.
	c.FormMoveFocus(2)
	if c.form.focusedSpec().id != fieldReadOnly {
		t.Fatalf("focus = %v, want fieldReadOnly", c.form.focusedSpec().id)
	}
	c.FormToggleFocused()
	if !c.form.conn.ReadOnly {
		t.Fatal("read_only not flipped on")
	}
}

// TestForm_NameValidation covers non-empty + unique-excluding-self (AC3).
func TestForm_NameValidation(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	c := newTestConnectionManager(&captureDriver{}, nil, nil)

	// Add form: "beta" already taken → reject; "gamma" ok.
	c.OpenAddForm([]string{"alpha", "beta"}, testDrivers)
	if err := c.form.validateName("", tr); err == nil {
		t.Error("empty name accepted")
	}
	if err := c.form.validateName("beta", tr); err == nil {
		t.Error("duplicate name accepted on add")
	}
	if err := c.form.validateName("gamma", tr); err != nil {
		t.Errorf("unique name rejected on add: %v", err)
	}

	// Edit form: renaming onto own original name is allowed.
	c.OpenEditForm(models.Connection{Name: "beta"}, []string{"alpha", "beta"}, testDrivers)
	if err := c.form.validateName("beta", tr); err != nil {
		t.Errorf("keeping own name rejected on edit: %v", err)
	}
	if err := c.form.validateName("alpha", tr); err == nil {
		t.Error("renaming onto another existing name accepted on edit")
	}
}

// TestForm_DSNValidation covers url.Parse + inline-password rejection (AC3).
func TestForm_DSNValidation(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	if err := validateDSN("", tr); err == nil {
		t.Error("empty DSN accepted")
	}
	if err := validateDSN("://bad::url", tr); err == nil {
		t.Error("unparseable DSN accepted")
	}
	if err := validateDSN("postgres://user:secret@host/db", tr); err == nil {
		t.Error("DSN with inline password accepted")
	} else if !strings.Contains(err.Error(), tr.DSNInlinePassword) {
		t.Errorf("inline-password error = %q, want DSNInlinePassword", err)
	}
	if err := validateDSN("postgres://user@host/db", tr); err != nil {
		t.Errorf("valid DSN rejected: %v", err)
	}
}

// TestForm_ValidateAllStampsErrorAndMovesFocus asserts a failing validate-all
// renders the error inline at the offending field (AC3).
func TestForm_ValidateAllStampsErrorAndMovesFocus(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.OpenAddForm([]string{"beta"}, testDrivers)

	// Empty name → validate-all fails, focus snaps to name (index 0).
	_, _, _, ok := c.FormValidateAll(tr)
	if ok {
		t.Fatal("validate-all passed with empty name")
	}
	if c.form.focus != 0 {
		t.Errorf("focus after name failure = %d, want 0", c.form.focus)
	}
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	if !strings.Contains(drv.lastContent, "name must not be empty") {
		t.Errorf("inline name error not rendered\n%s", drv.lastContent)
	}

	// Fix name, leave DSN empty → fail moves focus onto DSN.
	c.form.conn.Name = "ok"
	_, _, _, ok = c.FormValidateAll(tr)
	if ok {
		t.Fatal("validate-all passed with empty DSN")
	}
	if c.form.focusedSpec().id != fieldDSN {
		t.Errorf("focus after DSN failure = %v, want fieldDSN", c.form.focusedSpec().id)
	}
}

// TestForm_ValidateAllSucceedsReturnsConn asserts a fully valid form returns
// the edited connection + add/edit metadata.
func TestForm_ValidateAllSucceedsReturnsConn(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenEditForm(models.Connection{Name: "beta", DSN: "postgres://u@h/db"}, []string{"beta"}, testDrivers)
	conn, isEdit, orig, ok := c.FormValidateAll(tr)
	if !ok {
		t.Fatal("valid edit form failed validate-all")
	}
	if !isEdit || orig != "beta" {
		t.Errorf("isEdit=%v orig=%q, want true/beta", isEdit, orig)
	}
	if conn.Name != "beta" {
		t.Errorf("returned conn name = %q, want beta", conn.Name)
	}
}

// TestForm_TagsRoundTrip asserts comma-separated tag text parses into a
// trimmed slice and renders back joined.
func TestForm_TagsRoundTrip(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	// Focus tags row (last functional, index 9).
	for range 9 {
		c.FormMoveFocus(1)
	}
	if c.form.focusedSpec().id != fieldTags {
		t.Fatalf("focus = %v, want fieldTags", c.form.focusedSpec().id)
	}
	c.FormSetFocusedValue(" prod , , db ")
	got := c.form.conn.Tags
	if len(got) != 2 || got[0] != "prod" || got[1] != "db" {
		t.Fatalf("parsed tags = %v, want [prod db]", got)
	}
	if c.FormFocusedValue() != "prod, db" {
		t.Errorf("tags display = %q, want 'prod, db'", c.FormFocusedValue())
	}
}
