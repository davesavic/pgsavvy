package context

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestForm_SSHAuthVisibility asserts the auth method gates identity_file: it is
// shown only for key-file, hidden for agent and password.
func TestForm_SSHAuthVisibility(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	c.form.toggle(fieldUseSSHTunnel) // enable

	idFile := connFieldSpec{id: fieldSSHIdentityFile, kind: fieldText}
	knownHosts := connFieldSpec{id: fieldSSHKnownHosts, kind: fieldText}

	c.form.sshAuth = sshAuthAgent
	if c.form.visible(idFile) {
		t.Error("identity_file visible for agent auth")
	}
	if !c.form.visible(knownHosts) {
		t.Error("known_hosts hidden for agent auth")
	}

	c.form.sshAuth = sshAuthKeyFile
	if !c.form.visible(idFile) {
		t.Error("identity_file hidden for key-file auth")
	}

	c.form.sshAuth = sshAuthPassword
	if c.form.visible(idFile) {
		t.Error("identity_file visible for password auth")
	}
}

// TestForm_SSHFocusClampOnToggleOff asserts that disabling the tunnel while the
// cursor is parked on a now-hidden SSH row re-pins focus into the visible range.
func TestForm_SSHFocusClampOnToggleOff(t *testing.T) {
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	c.form.toggle(fieldUseSSHTunnel) // enable → SSH rows appear

	// Park the cursor on a late SSH row.
	c.form.focus = c.form.focusIndexOf(fieldSSHPort)
	if c.form.focusedSpec().id != fieldSSHPort {
		t.Fatalf("setup: focus not on ssh_port, got %v", c.form.focusedSpec().id)
	}

	c.form.toggle(fieldUseSSHTunnel) // disable → SSH rows vanish, focus must clamp
	n := len(c.form.focusableSpecs())
	if c.form.focus < 0 || c.form.focus >= n {
		t.Fatalf("focus %d out of range [0,%d) after toggle-off", c.form.focus, n)
	}
	// The focused row must be one that is actually visible now.
	if !c.form.visible(c.form.focusedSpec()) {
		t.Errorf("focus landed on a hidden row: %v", c.form.focusedSpec().id)
	}
}

// TestForm_DeriveSSHAuthOnEdit asserts OpenEditForm reflects how the loaded
// tunnel authenticates, with no persisted auth-method field.
func TestForm_DeriveSSHAuthOnEdit(t *testing.T) {
	cases := []struct {
		name        string
		tunnel      *models.SSHTunnelConfig
		wantEnabled bool
		wantAuth    string
	}{
		{"nil tunnel", nil, false, sshAuthAgent},
		{"agent", &models.SSHTunnelConfig{Host: "h", User: "u", IdentityFromAgent: true}, true, sshAuthAgent},
		{"key-file", &models.SSHTunnelConfig{Host: "h", User: "u", IdentityFile: "/k"}, true, sshAuthKeyFile},
		{"password", &models.SSHTunnelConfig{Host: "h", User: "u"}, true, sshAuthPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			c.OpenEditForm(models.Connection{Name: "x", Driver: "postgres", SSHTunnel: tc.tunnel}, nil, testDrivers)
			if c.form.sshEnabled != tc.wantEnabled {
				t.Errorf("sshEnabled = %v, want %v", c.form.sshEnabled, tc.wantEnabled)
			}
			if c.form.sshAuth != tc.wantAuth {
				t.Errorf("sshAuth = %q, want %q", c.form.sshAuth, tc.wantAuth)
			}
		})
	}
}

// TestForm_SSHOffSavesNilTunnel asserts that toggling the tunnel off after
// entering SSH data drops it: validate-all produces a connection with no tunnel.
func TestForm_SSHOffSavesNilTunnel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	c := newTestConnectionManager(&captureDriver{}, nil, nil)
	c.OpenAddForm(nil, testDrivers)
	c.form.conn.Name = "x"

	c.form.toggle(fieldUseSSHTunnel)          // on
	c.form.setTextValue(fieldSSHHost, "bast") // enter data
	c.form.toggle(fieldUseSSHTunnel)          // off again

	conn, _, _, ok := c.FormValidateAll(tr)
	if !ok {
		t.Fatal("validate-all failed for a disabled tunnel")
	}
	if conn.SSHTunnel != nil {
		t.Errorf("SSHTunnel persisted after disabling: %+v", conn.SSHTunnel)
	}
}
