package context

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func testDrivers() []string { return []string{"postgres", "mysql"} }

// TestForm_RendersAllFunctionalRows asserts the form body shows every
// functional field, including the now-editable icon/keyring/password_command
// credential rows (AC1).
func TestForm_RendersAllFunctionalRows(t *testing.T) {
	drv := &captureDriver{}
	c := newTestConnectionManager(drv, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	if err := c.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}
	body := drv.lastContent
	for _, want := range []string{
		"name:", "driver:", "dsn:", "read_only:", "confirm_writes:",
		"confirm_ddl:", "statement_timeout:", "color:", "label:", "icon:", "tags:",
		"ssh_host:", "ssh_user:", "ssh_port:", "identity_file:",
		"identity_from_agent:", "known_hosts:",
		"keyring:", "pgpass:", "password_command:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("form body missing %q\n%s", want, body)
		}
	}
	// keyring/password_command are now editable text rows, not "(soon)".
	if strings.Contains(body, "(soon)") {
		t.Errorf("form body still renders a greyed (soon) marker\n%s", body)
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

// TestForm_FieldNavMovesFocus asserts j/k/Tab move the field cursor across
// every functional row, with no rows skipped (AC2).
func TestForm_FieldNavMovesFocus(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	// 11 base functional rows (now incl. icon) + 6 SSH rows + keyring + pgpass
	// + password_command → focusable count is 20.
	if got := len(c.form.focusableSpecs()); got != 20 {
		t.Fatalf("focusable rows = %d, want 20", got)
	}
	// Move far past the end; clamps to the last functional row (password_command).
	for range 50 {
		c.FormMoveFocus(1)
	}
	if id := c.form.focusedSpec().id; id != fieldPasswordCommand {
		t.Fatalf("focus after over-move = %v, want fieldPasswordCommand", id)
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
	// Focus tags row (index 10, after icon was inserted at 9).
	for range 10 {
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

// TestForm_PgpassIsFocusable asserts the pgpass row is now an editable text
// field the cursor lands on, not a greyed "(soon)" placeholder.
func TestForm_PgpassIsFocusable(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	for _, s := range c.form.focusableSpecs() {
		if s.id == fieldPgpass {
			return
		}
	}
	t.Error("fieldPgpass not focusable")
}

// TestForm_PgpassTextRoundTrip asserts the pgpass row reads/writes
// conn.PgpassPath.
func TestForm_PgpassTextRoundTrip(t *testing.T) {
	f := &connForm{}
	f.setTextValue(fieldPgpass, "~/.pgpass")
	if f.conn.PgpassPath != "~/.pgpass" {
		t.Errorf("PgpassPath = %q, want ~/.pgpass", f.conn.PgpassPath)
	}
	if got := f.textValue(fieldPgpass); got != "~/.pgpass" {
		t.Errorf("textValue(pgpass) = %q, want ~/.pgpass", got)
	}
}

// TestForm_PgpassValidator rejects control chars/newlines, allows empty and
// normal paths.
func TestForm_PgpassValidator(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	f := &connForm{}
	v := f.validatorFor(fieldPgpass, tr)
	if v == nil {
		t.Fatal("no validator for pgpass")
	}
	if err := v(""); err != nil {
		t.Errorf("empty pgpass rejected: %v", err)
	}
	if err := v("~/.pgpass"); err != nil {
		t.Errorf("normal path rejected: %v", err)
	}
	if err := v("/path/with\nnewline"); err == nil {
		t.Error("path with newline accepted")
	}
}

// focusable reports whether id is a focusable form row.
func focusable(f *connForm, id connFieldID) bool {
	for _, s := range f.focusableSpecs() {
		if s.id == id {
			return true
		}
	}
	return false
}

// TestForm_CredentialRowsAreFocusable asserts icon/keyring/password_command are
// editable (focusable) rows, not skipped "(soon)" placeholders.
func TestForm_CredentialRowsAreFocusable(t *testing.T) {
	f := &connForm{}
	for _, id := range []connFieldID{fieldIcon, fieldKeyring, fieldPasswordCommand} {
		if !focusable(f, id) {
			t.Errorf("field %v not focusable", id)
		}
	}
}

// TestForm_IconTextRoundTrip asserts the icon row reads/writes conn.Icon and
// clearing it saves an empty value.
func TestForm_IconTextRoundTrip(t *testing.T) {
	f := &connForm{}
	f.setTextValue(fieldIcon, "ICON")
	if f.conn.Icon != "ICON" {
		t.Errorf("Icon = %q, want ICON", f.conn.Icon)
	}
	if got := f.textValue(fieldIcon); got != "ICON" {
		t.Errorf("textValue(icon) = %q, want ICON", got)
	}
	f.setTextValue(fieldIcon, "")
	if f.conn.Icon != "" {
		t.Errorf("Icon after clear = %q, want empty", f.conn.Icon)
	}
}

// TestForm_KeyringTextRoundTrip asserts the keyring row reads/writes
// conn.KeyringRef.
func TestForm_KeyringTextRoundTrip(t *testing.T) {
	f := &connForm{}
	f.setTextValue(fieldKeyring, "prod/db")
	if f.conn.KeyringRef != "prod/db" {
		t.Errorf("KeyringRef = %q, want prod/db", f.conn.KeyringRef)
	}
	if got := f.textValue(fieldKeyring); got != "prod/db" {
		t.Errorf("textValue(keyring) = %q, want prod/db", got)
	}
}

// TestForm_PasswordCommandTextRoundTrip asserts the password_command row
// reads/writes conn.PasswordCommand.
func TestForm_PasswordCommandTextRoundTrip(t *testing.T) {
	f := &connForm{}
	f.setTextValue(fieldPasswordCommand, "pass show db")
	if f.conn.PasswordCommand != "pass show db" {
		t.Errorf("PasswordCommand = %q, want 'pass show db'", f.conn.PasswordCommand)
	}
	if got := f.textValue(fieldPasswordCommand); got != "pass show db" {
		t.Errorf("textValue(password_command) = %q, want 'pass show db'", got)
	}
}

// TestForm_CredentialRowsDisplayValueNotSoon asserts displayValue renders the
// stored value for the credential rows rather than "(soon)", and never returns
// "(soon)" even when empty.
func TestForm_CredentialRowsDisplayValueNotSoon(t *testing.T) {
	specByID := func(id connFieldID) connFieldSpec {
		for _, s := range connFormSpecs {
			if s.id == id {
				return s
			}
		}
		t.Fatalf("no spec for %v", id)
		return connFieldSpec{}
	}
	f := &connForm{conn: models.Connection{
		Icon:            "ICON",
		KeyringRef:      "prod/db",
		PasswordCommand: "pass show db",
	}}
	cases := map[connFieldID]string{
		fieldIcon:            "ICON",
		fieldKeyring:         "prod/db",
		fieldPasswordCommand: "pass show db",
	}
	for id, want := range cases {
		if got := f.displayValue(specByID(id)); got != want {
			t.Errorf("displayValue(%v) = %q, want %q", id, got, want)
		}
	}
	// Empty values render "(empty)", never "(soon)".
	empty := &connForm{}
	for id := range cases {
		if got := empty.displayValue(specByID(id)); got == "(soon)" {
			t.Errorf("displayValue(%v) still renders (soon)", id)
		}
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
