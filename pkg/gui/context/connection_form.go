package context

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
	"github.com/davesavic/pgsavvy/pkg/theme"
)

// connFieldKind classifies how a form row is edited:
//   - fieldText: edited through the single-line PROMPT popup.
//   - fieldDriver: cycled through drivers.Names() in place.
//   - fieldToggle: flipped in place.
//   - fieldSoon: greyed, non-editable "(soon)" placeholder.
type connFieldKind int

const (
	fieldText connFieldKind = iota
	fieldDriver
	fieldSelector
	fieldToggle
	fieldSoon
)

// connFieldID enumerates the form rows in render order. Only the functional
// rows are focusable; the "(soon)" rows render greyed but the cursor skips
// them so j/k/Tab never land on an uneditable row.
type connFieldID int

const (
	fieldName connFieldID = iota
	fieldDriverSel
	fieldHost
	fieldPort
	fieldUser
	fieldDatabase
	fieldSSLMode
	fieldReadOnly
	fieldConfirmWrites
	fieldConfirmDDL
	fieldStatementTimeout
	fieldColor
	fieldLabel
	fieldIcon
	fieldTags
	// SSH tunnel rows. fieldUseSSHTunnel and fieldSSHAuthMethod are TRANSIENT
	// form state (connForm.sshEnabled / sshAuth), NOT persisted Connection
	// fields: the auth picker derives Connection.SSHTunnel.IdentityFromAgent /
	// IdentityFile at save time. The detail rows are hidden until the tunnel is
	// enabled, and identity_file only shows for the key-file auth method.
	fieldUseSSHTunnel
	fieldSSHAuthMethod
	fieldSSHHost
	fieldSSHUser
	fieldSSHPort
	fieldSSHIdentityFile
	fieldSSHKnownHosts
	// Credential rows — editable text mapping to the credential waterfall
	// (pkg/session/credentials.go).
	fieldKeyring
	fieldPgpass
	fieldPasswordCommand
)

// connFieldSpec is the static descriptor for one form row.
type connFieldSpec struct {
	id    connFieldID
	label string
	kind  connFieldKind
}

// connFormSpecs is the ordered row layout. The first ten rows plus the six SSH
// rows are functional; the trailing three are greyed "(soon)" placeholders.
var connFormSpecs = []connFieldSpec{
	{fieldName, "name", fieldText},
	{fieldDriverSel, "driver", fieldDriver},
	{fieldHost, "host", fieldText},
	{fieldPort, "port", fieldText},
	{fieldUser, "user", fieldText},
	{fieldDatabase, "database", fieldText},
	{fieldSSLMode, "sslmode", fieldSelector},
	{fieldReadOnly, "read_only", fieldToggle},
	{fieldConfirmWrites, "confirm_writes", fieldToggle},
	{fieldConfirmDDL, "confirm_ddl", fieldToggle},
	{fieldStatementTimeout, "statement_timeout", fieldText},
	{fieldColor, "color", fieldText},
	{fieldLabel, "label", fieldText},
	{fieldIcon, "icon", fieldText},
	{fieldTags, "tags", fieldText},
	{fieldUseSSHTunnel, "use_ssh_tunnel", fieldToggle},
	{fieldSSHAuthMethod, "ssh_auth", fieldSelector},
	{fieldSSHHost, "ssh_host", fieldText},
	{fieldSSHUser, "ssh_user", fieldText},
	{fieldSSHPort, "ssh_port", fieldText},
	{fieldSSHIdentityFile, "identity_file", fieldText},
	{fieldSSHKnownHosts, "known_hosts", fieldText},
	{fieldKeyring, "keyring", fieldText},
	{fieldPgpass, "pgpass", fieldText},
	{fieldPasswordCommand, "password_command", fieldText},
}

// connForm is the in-memory add/edit form state. It owns the
// edited models.Connection, the focused-field index, and the inline error
// string. It performs NO persistence — the controller's save callback owns
// that (the seam zod populates).
//
// State is transient and not goroutine-safe (mirrors the rest of the modal):
// the controller mutates it on the UI thread.
type connForm struct {
	conn  models.Connection
	focus int // index into the FOCUSABLE field list (functional rows only)
	err   string

	// sshEnabled and sshAuth are TRANSIENT (never persisted): they gate the
	// SSH section's visibility and drive how the auth picker maps onto
	// Connection.SSHTunnel at save time. Derived from the loaded tunnel on
	// edit-open (deriveSSHAuth).
	sshEnabled bool
	sshAuth    string

	isEdit       bool
	originalName string

	// existingNames is the snapshot of all profile names at open time, used
	// for the uniqueness check. For an edit, originalName is excluded so a
	// rename onto its own name passes.
	existingNames []string

	// driversFn returns the registered driver names for the selector. Defaults
	// to drivers.Names; overridable for tests.
	driversFn func() []string
}

// focusableFields is the slice of specs the cursor can land on (functional
// rows). The "(soon)" rows are excluded.
func (f *connForm) focusableSpecs() []connFieldSpec {
	out := make([]connFieldSpec, 0, len(connFormSpecs))
	for _, s := range connFormSpecs {
		if !f.visible(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// visible reports whether a row is part of the focusable set given the current
// transient SSH state: the SSH detail rows appear only when the tunnel is
// enabled, and identity_file only for the key-file auth method.
func (f *connForm) visible(s connFieldSpec) bool {
	switch s.id {
	case fieldSSHAuthMethod, fieldSSHHost, fieldSSHUser, fieldSSHPort, fieldSSHKnownHosts:
		return f.sshEnabled
	case fieldSSHIdentityFile:
		return f.sshEnabled && f.sshAuth == sshAuthKeyFile
	}
	return s.kind != fieldSoon
}

// clampFocus re-pins the field cursor into the current focusable range after a
// visibility change (SSH toggle / auth-method) shrinks the set, so the cursor
// never lands on a now-hidden row.
func (f *connForm) clampFocus() {
	n := len(f.focusableSpecs())
	if n == 0 {
		f.focus = 0
		return
	}
	if f.focus >= n {
		f.focus = n - 1
	}
	if f.focus < 0 {
		f.focus = 0
	}
}

// focusedSpec returns the spec under the field cursor.
func (f *connForm) focusedSpec() connFieldSpec {
	specs := f.focusableSpecs()
	if f.focus < 0 || f.focus >= len(specs) {
		return specs[0]
	}
	return specs[f.focus]
}

// moveFocus shifts the field cursor by delta, clamping into range.
func (f *connForm) moveFocus(delta int) {
	n := len(f.focusableSpecs())
	f.focus += delta
	if f.focus < 0 {
		f.focus = 0
	}
	if f.focus >= n {
		f.focus = n - 1
	}
}

// names returns the driver-name list for the selector.
func (f *connForm) names() []string {
	if f.driversFn != nil {
		return f.driversFn()
	}
	return drivers.Names()
}

// cycleDriver advances the driver to the next registered name, wrapping. A
// no-op when no drivers are registered.
func (f *connForm) cycleDriver() {
	names := f.names()
	if len(names) == 0 {
		return
	}
	idx := 0
	for i, n := range names {
		if n == f.conn.Driver {
			idx = i + 1
			break
		}
	}
	f.conn.Driver = names[idx%len(names)]
}

// sslModeOptions is the fixed cycle for the sslmode selector, in libpq order.
var sslModeOptions = []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}

// cycleSSLMode advances SSLMode to the next option, wrapping last→first. An
// empty or unrecognized current value starts the cycle at the first option.
func (f *connForm) cycleSSLMode() {
	idx := 0
	for i, m := range sslModeOptions {
		if m == f.conn.SSLMode {
			idx = i + 1
			break
		}
	}
	f.conn.SSLMode = sslModeOptions[idx%len(sslModeOptions)]
}

// SSH auth methods. These are TRANSIENT selector values (connForm.sshAuth),
// never persisted: applySSHAuth maps them onto Connection.SSHTunnel at save.
const (
	sshAuthAgent    = "agent"
	sshAuthKeyFile  = "key-file"
	sshAuthPassword = "password"
)

var sshAuthOptions = []string{sshAuthAgent, sshAuthKeyFile, sshAuthPassword}

// cycleSSHAuth advances the transient auth method, wrapping last→first, then
// re-clamps focus because identity_file's visibility depends on it.
func (f *connForm) cycleSSHAuth() {
	idx := 0
	for i, m := range sshAuthOptions {
		if m == f.sshAuth {
			idx = i + 1
			break
		}
	}
	f.sshAuth = sshAuthOptions[idx%len(sshAuthOptions)]
	f.clampFocus()
}

// toggleSSHEnabled flips the "Use SSH tunnel?" gate. Turning it on allocates a
// tunnel so host/user edits have a target. Turning it off KEEPS any in-progress
// tunnel data in memory so a re-enable restores it — validateAll drops the
// tunnel at save when the gate is off, so a disabled section never persists.
// Focus is re-clamped because the SSH rows appear/disappear.
func (f *connForm) toggleSSHEnabled() {
	f.sshEnabled = !f.sshEnabled
	if f.sshEnabled {
		f.ensureSSHTunnel()
	}
	f.clampFocus()
}

// applySSHAuth maps the transient auth method onto the tunnel config at save
// time so IdentityFromAgent/IdentityFile reflect the picker without a persisted
// auth-method key (A3). No-op when the tunnel is nil.
func (f *connForm) applySSHAuth() {
	t := f.conn.SSHTunnel
	if t == nil {
		return
	}
	switch f.sshAuth {
	case sshAuthAgent:
		t.IdentityFromAgent = true
		t.IdentityFile = ""
	case sshAuthPassword:
		t.IdentityFromAgent = false
		t.IdentityFile = ""
	default: // sshAuthKeyFile — keep the entered IdentityFile
		t.IdentityFromAgent = false
	}
}

// deriveSSHAuth reads the transient (enabled, auth) state from a loaded
// connection so the edit form reflects how the existing tunnel authenticates,
// with no persisted auth-method key. A nil tunnel defaults to agent so toggling
// the gate on starts from a sensible method.
func deriveSSHAuth(c models.Connection) (enabled bool, auth string) {
	t := c.SSHTunnel
	if t == nil {
		return false, sshAuthAgent
	}
	if t.IdentityFromAgent {
		return true, sshAuthAgent
	}
	if t.IdentityFile != "" {
		return true, sshAuthKeyFile
	}
	return true, sshAuthPassword
}

// toggleFocused flips the focused toggle, or cycles the driver when the
// driver row is focused. No-op on text rows.
func (f *connForm) toggleFocused() { f.toggle(f.focusedSpec().id) }

// toggle flips the toggle row identified by id (or cycles the driver). No-op
// on text rows. Split out from toggleFocused so save-time / test code can flip
// a field by id without moving the cursor onto it.
func (f *connForm) toggle(id connFieldID) {
	switch id {
	case fieldReadOnly:
		f.conn.ReadOnly = !f.conn.ReadOnly
	case fieldConfirmWrites:
		f.conn.ConfirmWrites = !f.conn.ConfirmWrites
	case fieldConfirmDDL:
		f.conn.ConfirmDDL = !f.conn.ConfirmDDL
	case fieldUseSSHTunnel:
		f.toggleSSHEnabled()
	case fieldSSHAuthMethod:
		f.cycleSSHAuth()
	case fieldDriverSel:
		f.cycleDriver()
	case fieldSSLMode:
		f.cycleSSLMode()
	}
}

// ensureSSHTunnel lazily allocates the SSHTunnel pointer so a first edit has a
// struct to write into. Returns the (now non-nil) config.
func (f *connForm) ensureSSHTunnel() *models.SSHTunnelConfig {
	if f.conn.SSHTunnel == nil {
		f.conn.SSHTunnel = &models.SSHTunnelConfig{}
	}
	return f.conn.SSHTunnel
}

// normalizeSSHTunnel drops the SSHTunnel pointer back to nil when every
// form-editable input is empty/false, so yaml omitempty omits the key. The
// YAML-only secret commands (PassphraseCommand/SSHPasswordCommand) are not form
// inputs, so they intentionally do NOT keep the struct alive here.
func (f *connForm) normalizeSSHTunnel() {
	t := f.conn.SSHTunnel
	if t == nil {
		return
	}
	if t.Host == "" && t.User == "" && t.Port == 0 &&
		t.IdentityFile == "" && !t.IdentityFromAgent && t.KnownHosts == "" {
		f.conn.SSHTunnel = nil
	}
}

// textValue returns the current string value of a text row for seeding the
// PROMPT popup.
func (f *connForm) textValue(id connFieldID) string {
	switch id {
	case fieldName:
		return f.conn.Name
	case fieldHost:
		return f.conn.Host
	case fieldUser:
		return f.conn.User
	case fieldDatabase:
		return f.conn.Database
	case fieldPort:
		if f.conn.Port == 0 {
			return ""
		}
		return strconv.Itoa(f.conn.Port)
	case fieldStatementTimeout:
		return f.conn.StatementTimeout
	case fieldColor:
		return f.conn.Color
	case fieldLabel:
		return f.conn.Label
	case fieldIcon:
		return f.conn.Icon
	case fieldKeyring:
		return f.conn.KeyringRef
	case fieldPasswordCommand:
		return f.conn.PasswordCommand
	case fieldTags:
		return strings.Join(f.conn.Tags, ", ")
	case fieldSSHHost:
		return f.sshString(func(t *models.SSHTunnelConfig) string { return t.Host })
	case fieldSSHUser:
		return f.sshString(func(t *models.SSHTunnelConfig) string { return t.User })
	case fieldSSHIdentityFile:
		return f.sshString(func(t *models.SSHTunnelConfig) string { return t.IdentityFile })
	case fieldSSHKnownHosts:
		return f.sshString(func(t *models.SSHTunnelConfig) string { return t.KnownHosts })
	case fieldSSHPort:
		if f.conn.SSHTunnel == nil || f.conn.SSHTunnel.Port == 0 {
			return ""
		}
		return strconv.Itoa(f.conn.SSHTunnel.Port)
	case fieldPgpass:
		return f.conn.PgpassPath
	}
	return ""
}

// sshString reads a string field from the tunnel config, nil-safe.
func (f *connForm) sshString(get func(*models.SSHTunnelConfig) string) string {
	if f.conn.SSHTunnel == nil {
		return ""
	}
	return get(f.conn.SSHTunnel)
}

// setTextValue stores a validated string value into the edited connection.
func (f *connForm) setTextValue(id connFieldID, v string) {
	v = strings.TrimSpace(v)
	switch id {
	case fieldName:
		f.conn.Name = v
	case fieldHost:
		f.conn.Host = v
	case fieldUser:
		f.conn.User = v
	case fieldDatabase:
		f.conn.Database = v
	case fieldPort:
		// The port validator already rejected non-numeric input at the popup;
		// an empty string clears the port to 0 (unset, pgx default applies).
		port, _ := strconv.Atoi(v)
		f.conn.Port = port
	case fieldStatementTimeout:
		f.conn.StatementTimeout = v
	case fieldColor:
		f.conn.Color = v
	case fieldLabel:
		f.conn.Label = v
	case fieldIcon:
		f.conn.Icon = v
	case fieldKeyring:
		f.conn.KeyringRef = v
	case fieldPasswordCommand:
		f.conn.PasswordCommand = v
	case fieldTags:
		f.conn.Tags = parseTags(v)
	case fieldSSHHost:
		f.ensureSSHTunnel().Host = v
		f.normalizeSSHTunnel()
	case fieldSSHUser:
		f.ensureSSHTunnel().User = v
		f.normalizeSSHTunnel()
	case fieldSSHIdentityFile:
		f.ensureSSHTunnel().IdentityFile = v
		f.normalizeSSHTunnel()
	case fieldSSHKnownHosts:
		f.ensureSSHTunnel().KnownHosts = v
		f.normalizeSSHTunnel()
	case fieldSSHPort:
		// The port validator already rejected non-numeric input at the popup;
		// an empty string clears the port to 0 (unset).
		port, _ := strconv.Atoi(v)
		f.ensureSSHTunnel().Port = port
		f.normalizeSSHTunnel()
	case fieldPgpass:
		f.conn.PgpassPath = v
	}
}

// parseTags splits a comma-separated string into a trimmed, non-empty slice.
// Returns nil for an empty string so the yaml omitempty tag drops the key.
func parseTags(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// connHasDiscreteFields reports whether any discrete connection field is set.
func connHasDiscreteFields(c models.Connection) bool {
	return c.Host != "" || c.Port != 0 || c.User != "" || c.Database != "" || c.SSLMode != ""
}

// migrateLegacyDSN lazily parses a legacy dsn-only profile into discrete fields
// so the Edit form renders populated discrete rows. The raw DSN is left intact;
// the config save normalizer (A2) drops it once discrete fields are present. A
// parse failure leaves the discrete fields empty so the raw DSN remains the
// connect-time fallback.
func migrateLegacyDSN(conn models.Connection) models.Connection {
	if conn.DSN == "" || connHasDiscreteFields(conn) {
		return conn
	}
	parsed, err := session.ParseDSNIntoConnection(conn.DSN)
	if err != nil {
		return conn
	}
	conn.Host = parsed.Host
	conn.Port = parsed.Port
	conn.User = parsed.User
	conn.Database = parsed.Database
	conn.SSLMode = parsed.SSLMode
	return conn
}

// applyPastedDSN parses a clipboard DSN into the form's discrete fields. On a
// parse failure it sets the inline error and leaves every field untouched
// (ok=false). On success it returns whether the DSN carried an inline password
// — which is DROPPED, never stored — so the caller can warn the user.
func (f *connForm) applyPastedDSN(dsn string) (hadPassword, ok bool) {
	parsed, err := session.ParseDSNIntoConnection(dsn)
	if err != nil {
		f.err = "clipboard does not contain a valid Postgres DSN"
		return false, false
	}
	f.conn.Host = parsed.Host
	f.conn.Port = parsed.Port
	f.conn.User = parsed.User
	f.conn.Database = parsed.Database
	f.conn.SSLMode = parsed.SSLMode
	return session.DSNHasInlinePassword(dsn), true
}

// validateName enforces non-empty + unique (excluding the edited row's own
// original name). Used both by the PROMPT popup validator and validate-all.
func (f *connForm) validateName(raw string, tr *i18n.TranslationSet) error {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fmt.Errorf("name must not be empty")
	}
	for _, existing := range f.existingNames {
		if f.isEdit && existing == f.originalName {
			continue
		}
		if existing == v {
			return fmt.Errorf("%s", tr.DuplicateConnectionName)
		}
	}
	return nil
}

// validatorFor returns the popup validator for a text field, or nil if the
// field has no validation (free text).
func (f *connForm) validatorFor(id connFieldID, tr *i18n.TranslationSet) func(string) error {
	switch id {
	case fieldName:
		return func(s string) error { return f.validateName(s, tr) }
	case fieldPort:
		return validateSSHPort
	case fieldSSHPort:
		return validateSSHPort
	case fieldSSHIdentityFile:
		return validateIdentityFile
	case fieldPgpass:
		return validatePgpassPath
	}
	return nil
}

// validateSSHPort accepts an empty string (port unset) or an integer in the
// 1-65535 TCP range. Anything else is rejected at the prompt popup.
func validateSSHPort(raw string) error {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil
	}
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}

// validateIdentityFile applies a light path-shape check: empty is allowed and
// control characters / newlines are rejected. It does NOT touch the filesystem.
func validateIdentityFile(raw string) error {
	for _, r := range raw {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') {
			return fmt.Errorf("identity file path must not contain control characters")
		}
	}
	return nil
}

// validatePgpassPath applies the same light path-shape check as
// validateIdentityFile: empty is allowed and control characters / newlines are
// rejected. It does NOT touch the filesystem; the session layer reads and
// permission-checks the file at connect time.
func validatePgpassPath(raw string) error {
	for _, r := range raw {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') {
			return fmt.Errorf("pgpass path must not contain control characters")
		}
	}
	return nil
}

// validateAll runs every save-time rule and returns the first failure plus
// the focusable index of the failing field so the controller can move the
// cursor onto it. ok is true when the form is valid.
func (f *connForm) validateAll(tr *i18n.TranslationSet) (msg string, fieldIdx int, ok bool) {
	if err := f.validateName(f.conn.Name, tr); err != nil {
		return err.Error(), f.focusIndexOf(fieldName), false
	}
	// Discrete connection fields are not required at save time (drafts are
	// allowed); an empty Host is handled at connect time. Only name uniqueness
	// and SSH-tunnel completeness are enforced here.
	//
	// When the SSH gate is on, the tunnel must exist and be complete: apply the
	// picker's auth choice, then let the session-layer rule require host+user.
	// When off, drop the tunnel entirely so a disabled section never persists.
	if f.sshEnabled {
		f.ensureSSHTunnel()
		f.applySSHAuth()
	} else {
		f.conn.SSHTunnel = nil
	}
	if err := session.ValidateSSHTunnel(f.conn.SSHTunnel); err != nil {
		return err.Error(), f.focusIndexOf(fieldSSHHost), false
	}
	return "", 0, true
}

// focusIndexOf returns the focusable-list index for a field id.
func (f *connForm) focusIndexOf(id connFieldID) int {
	for i, s := range f.focusableSpecs() {
		if s.id == id {
			return i
		}
	}
	return 0
}

// render produces the full form body: every field on its own line, the
// focused row prefixed with "> ", the "(soon)" rows greyed, and the inline
// error (if any) on its own line under the focused field's label.
func (f *connForm) render() string {
	var b strings.Builder
	focused := f.focusedSpec()
	for _, s := range connFormSpecs {
		if !f.visible(s) {
			continue
		}
		marker := "  "
		if s.id == focused.id {
			marker = "> "
		}
		fmt.Fprintf(&b, "%s%-18s %s\n", marker, s.label+":", f.displayValue(s))
		if f.err != "" && s.id == focused.id {
			fmt.Fprintf(&b, "    %s\n", f.err)
		}
	}
	return b.String()
}

// colorPreviewSGR returns the ANSI foreground escape used to tint the colour
// field's own value, accepting both standard names ("red") and hex codes
// ("#ff4d4d", "#abc"). Returns "" when the token is neither, so the value is
// shown untinted.
func colorPreviewSGR(v string) string {
	if sgr := theme.AnsiFgSGR(v); sgr != "" {
		return sgr
	}
	return theme.AnsiFgHexSGR(v)
}

// displayValue renders the right-hand value column for a row.
func (f *connForm) displayValue(s connFieldSpec) string {
	switch s.kind {
	case fieldSoon:
		return "(soon)"
	case fieldToggle:
		return boolDisplay(f.toggleValue(s.id))
	case fieldDriver:
		if f.conn.Driver == "" {
			return "(none)"
		}
		return f.conn.Driver
	case fieldSelector:
		if s.id == fieldSSHAuthMethod {
			return f.sshAuth
		}
		if f.conn.SSLMode == "" {
			return "(default)"
		}
		return f.conn.SSLMode
	default:
		v := f.textValue(s.id)
		if v == "" {
			return "(empty)"
		}
		if s.id == fieldColor {
			if sgr := colorPreviewSGR(v); sgr != "" {
				return sgr + v + theme.AnsiReset
			}
		}
		return v
	}
}

func (f *connForm) toggleValue(id connFieldID) bool {
	switch id {
	case fieldReadOnly:
		return f.conn.ReadOnly
	case fieldConfirmWrites:
		return f.conn.ConfirmWrites
	case fieldConfirmDDL:
		return f.conn.ConfirmDDL
	case fieldUseSSHTunnel:
		return f.sshEnabled
	}
	return false
}

func boolDisplay(v bool) string {
	if v {
		return "[x]"
	}
	return "[ ]"
}
