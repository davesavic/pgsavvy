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
		"ssh_host:", "ssh_user:", "ssh_port:", "identity_file:",
		"identity_from_agent:", "known_hosts:",
		"keyring:", "pgpass:", "password_command:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form body missing %q\n%s", want, body)
		}
	}
	if !strings.Contains(body, "(soon)") {
		t.Errorf("form body missing greyed (soon) marker\n%s", body)
	}
	// ssh_tunnel was reclassified from a "(soon)" placeholder into the six
	// editable sub-rows above; its old label must no longer render.
	if strings.Contains(body, "ssh_tunnel:") {
		t.Errorf("ssh_tunnel: still rendered as a row; expected the 6 SSH sub-rows\n%s", body)
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
	// 10 base functional rows + 6 SSH rows → focusable count is 16.
	if got := len(c.form.focusableSpecs()); got != 16 {
		t.Fatalf("focusable rows = %d, want 16", got)
	}
	// Move far past the end; clamps to the last functional row (known_hosts).
	for range 50 {
		c.FormMoveFocus(1)
	}
	if id := c.form.focusedSpec().id; id != fieldSSHKnownHosts {
		t.Fatalf("focus after over-move = %v, want fieldSSHKnownHosts", id)
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

// TestForm_SSHRowsAreFocusable asserts the six new SSH rows are focusable
// (not "(soon)") so the cursor lands on them (T6).
func TestForm_SSHRowsAreFocusable(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)

	got := map[connFieldID]bool{}
	for _, s := range c.form.focusableSpecs() {
		got[s.id] = true
	}
	for _, id := range []connFieldID{
		fieldSSHHost, fieldSSHUser, fieldSSHPort,
		fieldSSHIdentityFile, fieldSSHIdentityFromAgent, fieldSSHKnownHosts,
	} {
		if !got[id] {
			t.Errorf("SSH field %v not focusable", id)
		}
	}
}

// TestForm_SSHTextRoundTrip asserts each SSH text field reads back what was
// set, allocating SSHTunnel on first write (T6).
func TestForm_SSHTextRoundTrip(t *testing.T) {
	f := &connForm{}

	f.setTextValue(fieldSSHHost, "bastion.prod")
	f.setTextValue(fieldSSHUser, "deploy")
	f.setTextValue(fieldSSHPort, "2222")
	f.setTextValue(fieldSSHIdentityFile, "~/.ssh/id_ed25519")
	f.setTextValue(fieldSSHKnownHosts, "~/.ssh/known_hosts")

	if f.conn.SSHTunnel == nil {
		t.Fatal("SSHTunnel nil after SSH edits")
	}
	if f.textValue(fieldSSHHost) != "bastion.prod" {
		t.Errorf("host = %q, want bastion.prod", f.textValue(fieldSSHHost))
	}
	if f.textValue(fieldSSHUser) != "deploy" {
		t.Errorf("user = %q, want deploy", f.textValue(fieldSSHUser))
	}
	if f.textValue(fieldSSHPort) != "2222" {
		t.Errorf("port = %q, want 2222", f.textValue(fieldSSHPort))
	}
	if f.textValue(fieldSSHIdentityFile) != "~/.ssh/id_ed25519" {
		t.Errorf("identity_file = %q", f.textValue(fieldSSHIdentityFile))
	}
	if f.textValue(fieldSSHKnownHosts) != "~/.ssh/known_hosts" {
		t.Errorf("known_hosts = %q", f.textValue(fieldSSHKnownHosts))
	}
}

// TestForm_SSHPortNilSafeAndZero asserts port reads empty when unset (nil
// tunnel or Port==0) (T6).
func TestForm_SSHPortNilSafeAndZero(t *testing.T) {
	f := &connForm{}
	if got := f.textValue(fieldSSHPort); got != "" {
		t.Errorf("port on nil tunnel = %q, want empty", got)
	}
	f.setTextValue(fieldSSHHost, "h")
	if got := f.textValue(fieldSSHPort); got != "" {
		t.Errorf("port with Port==0 = %q, want empty", got)
	}
}

// TestForm_SSHAgentToggle asserts the agent toggle flips and reads back, and
// allocates the tunnel on toggle-on (T6).
func TestForm_SSHAgentToggle(t *testing.T) {
	f := &connForm{}
	if f.toggleValue(fieldSSHIdentityFromAgent) {
		t.Fatal("agent toggle on for nil tunnel")
	}
	f.toggle(fieldSSHIdentityFromAgent)
	if f.conn.SSHTunnel == nil {
		t.Fatal("SSHTunnel nil after agent toggle-on")
	}
	if !f.toggleValue(fieldSSHIdentityFromAgent) {
		t.Fatal("agent toggle not on after toggle")
	}
	f.toggle(fieldSSHIdentityFromAgent)
	if f.conn.SSHTunnel != nil {
		t.Fatal("SSHTunnel not normalized to nil after toggle-off")
	}
}

// TestForm_SSHNormalizeToNil asserts that clearing all SSH inputs drops the
// SSHTunnel pointer so yaml omitempty omits the key (T6).
func TestForm_SSHNormalizeToNil(t *testing.T) {
	f := &connForm{}
	f.setTextValue(fieldSSHHost, "h")
	if f.conn.SSHTunnel == nil {
		t.Fatal("SSHTunnel nil after host set")
	}
	f.setTextValue(fieldSSHHost, "")
	if f.conn.SSHTunnel != nil {
		t.Fatal("SSHTunnel not nil after clearing the only field")
	}
}

// TestForm_SSHPortValidator covers the prompt-popup port validator: empty
// allowed (unset), out-of-range rejected, in-range accepted (T6).
func TestForm_SSHPortValidator(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	f := &connForm{}
	v := f.validatorFor(fieldSSHPort, tr)
	if v == nil {
		t.Fatal("no validator for ssh_port")
	}
	if err := v(""); err != nil {
		t.Errorf("empty port rejected: %v", err)
	}
	if err := v("5432"); err != nil {
		t.Errorf("in-range port rejected: %v", err)
	}
	if err := v("70000"); err == nil {
		t.Error("out-of-range port 70000 accepted")
	}
	if err := v("0"); err == nil {
		t.Error("port 0 accepted (out of 1-65535)")
	}
	if err := v("abc"); err == nil {
		t.Error("non-numeric port accepted")
	}
}

// TestForm_SSHIdentityFileValidator rejects control chars/newlines, allows
// empty and normal paths (T6).
func TestForm_SSHIdentityFileValidator(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	f := &connForm{}
	v := f.validatorFor(fieldSSHIdentityFile, tr)
	if v == nil {
		t.Fatal("no validator for identity_file")
	}
	if err := v(""); err != nil {
		t.Errorf("empty identity_file rejected: %v", err)
	}
	if err := v("~/.ssh/id_ed25519"); err != nil {
		t.Errorf("normal path rejected: %v", err)
	}
	if err := v("/path/with\nnewline"); err == nil {
		t.Error("path with newline accepted")
	}
}

// TestForm_ValidateAllSSHHostRequired asserts that an SSH tunnel with agent
// auth but no host fails validate-all (host required, reusing
// session.ValidateSSHTunnel) (T6).
func TestForm_ValidateAllSSHHostRequired(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	c.form.conn.Name = "ok"
	c.form.conn.DSN = "postgres://u@h/db"
	// Agent on, host empty → tunnel non-nil, host missing.
	c.form.toggle(fieldSSHIdentityFromAgent)

	_, _, _, ok := c.FormValidateAll(tr)
	if ok {
		t.Fatal("validate-all passed with SSH agent on but no host")
	}
	if c.form.focusedSpec().id != fieldSSHHost {
		t.Errorf("focus after SSH-host failure = %v, want fieldSSHHost", c.form.focusedSpec().id)
	}
}

// colorSpec returns the static spec for the colour row.
func colorSpec() connFieldSpec {
	for _, s := range connFormSpecs {
		if s.id == fieldColor {
			return s
		}
	}
	panic("color spec missing")
}

// TestForm_ColorValueTintedWhenRecognized asserts the colour field's value is
// wrapped in the matching ANSI escape when the name is a standard colour, and
// (or 24-bit truecolor for hex codes), and left untinted for unknown names /
// empty.
func TestForm_ColorValueTintedWhenRecognized(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	spec := colorSpec()

	c.form.conn.Color = "red"
	if got, want := c.form.displayValue(spec), "\x1b[31mred\x1b[0m"; got != want {
		t.Errorf("red display = %q, want %q", got, want)
	}

	c.form.conn.Color = "#ff4d4d"
	if got, want := c.form.displayValue(spec), "\x1b[38;2;255;77;77m#ff4d4d\x1b[0m"; got != want {
		t.Errorf("hex display = %q, want %q", got, want)
	}

	c.form.conn.Color = "#abc"
	if got, want := c.form.displayValue(spec), "\x1b[38;2;170;187;204m#abc\x1b[0m"; got != want {
		t.Errorf("short-hex display = %q, want %q", got, want)
	}

	c.form.conn.Color = "notacolour"
	if got := c.form.displayValue(spec); got != "notacolour" {
		t.Errorf("unknown display = %q, want untinted %q", got, "notacolour")
	}

	c.form.conn.Color = ""
	if got := c.form.displayValue(spec); got != "(empty)" {
		t.Errorf("empty display = %q, want %q", got, "(empty)")
	}
}
