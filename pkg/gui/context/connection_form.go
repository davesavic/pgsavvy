package context

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/davesavic/dbsavvy/pkg/drivers"
	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
	"github.com/davesavic/dbsavvy/pkg/session"
	"github.com/davesavic/dbsavvy/pkg/theme"
)

// connFieldKind classifies how a form row is edited (dbsavvy-dyf):
//   - fieldText: edited through the single-line PROMPT popup.
//   - fieldDriver: cycled through drivers.Names() in place.
//   - fieldToggle: flipped in place.
//   - fieldSoon: greyed, non-editable "(soon)" placeholder.
type connFieldKind int

const (
	fieldText connFieldKind = iota
	fieldDriver
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
	fieldDSN
	fieldReadOnly
	fieldConfirmWrites
	fieldConfirmDDL
	fieldStatementTimeout
	fieldColor
	fieldLabel
	fieldTags
	// SSH tunnel rows — editable, mapping to Connection.SSHTunnel (T6).
	fieldSSHHost
	fieldSSHUser
	fieldSSHPort
	fieldSSHIdentityFile
	fieldSSHIdentityFromAgent
	fieldSSHKnownHosts
	// "(soon)" rows — rendered, never focusable.
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
	{fieldDSN, "dsn", fieldText},
	{fieldReadOnly, "read_only", fieldToggle},
	{fieldConfirmWrites, "confirm_writes", fieldToggle},
	{fieldConfirmDDL, "confirm_ddl", fieldToggle},
	{fieldStatementTimeout, "statement_timeout", fieldText},
	{fieldColor, "color", fieldText},
	{fieldLabel, "label", fieldText},
	{fieldTags, "tags", fieldText},
	{fieldSSHHost, "ssh_host", fieldText},
	{fieldSSHUser, "ssh_user", fieldText},
	{fieldSSHPort, "ssh_port", fieldText},
	{fieldSSHIdentityFile, "identity_file", fieldText},
	{fieldSSHIdentityFromAgent, "identity_from_agent", fieldToggle},
	{fieldSSHKnownHosts, "known_hosts", fieldText},
	{fieldKeyring, "keyring", fieldSoon},
	{fieldPgpass, "pgpass", fieldText},
	{fieldPasswordCommand, "password_command", fieldSoon},
}

// connForm is the in-memory add/edit form state (dbsavvy-dyf). It owns the
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
		if s.kind == fieldSoon {
			continue
		}
		out = append(out, s)
	}
	return out
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
	case fieldSSHIdentityFromAgent:
		f.ensureSSHTunnel().IdentityFromAgent = !f.conn.SSHTunnel.IdentityFromAgent
		f.normalizeSSHTunnel()
	case fieldDriverSel:
		f.cycleDriver()
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
	case fieldDSN:
		return f.conn.DSN
	case fieldStatementTimeout:
		return f.conn.StatementTimeout
	case fieldColor:
		return f.conn.Color
	case fieldLabel:
		return f.conn.Label
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
	case fieldDSN:
		f.conn.DSN = v
	case fieldStatementTimeout:
		f.conn.StatementTimeout = v
	case fieldColor:
		f.conn.Color = v
	case fieldLabel:
		f.conn.Label = v
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

// validateDSN enforces url.Parse-able + no inline password. Shared by the
// popup validator and validate-all.
func validateDSN(raw string, tr *i18n.TranslationSet) error {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fmt.Errorf("%s", tr.InvalidDSN)
	}
	u, err := url.Parse(v)
	if err != nil {
		return fmt.Errorf("%s", tr.InvalidDSN)
	}
	if u.User != nil {
		if _, hasPwd := u.User.Password(); hasPwd {
			return fmt.Errorf("%s", tr.DSNInlinePassword)
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
	case fieldDSN:
		return func(s string) error { return validateDSN(s, tr) }
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
	if err := validateDSN(f.conn.DSN, tr); err != nil {
		return err.Error(), f.focusIndexOf(fieldDSN), false
	}
	// Drop a tunnel whose every input is blank, then reuse the session-layer
	// rule as the single source of truth for host/user-required.
	f.normalizeSSHTunnel()
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
		marker := "  "
		if s.kind != fieldSoon && s.id == focused.id {
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
	case fieldSSHIdentityFromAgent:
		return f.conn.SSHTunnel != nil && f.conn.SSHTunnel.IdentityFromAgent
	}
	return false
}

func boolDisplay(v bool) string {
	if v {
		return "[x]"
	}
	return "[ ]"
}
