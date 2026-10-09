package context

import (
	"errors"
	"reflect"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
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

func TestReview_form_copy(t *testing.T) {
	profile := func() models.Connection {
		return models.Connection{
			Name: "original", Driver: "postgres", Host: "db", User: "db-user",
			ReadOnly: true, ConfirmWrites: true, ConfirmDDL: true,
			SSHTunnel: &models.SSHTunnelConfig{
				Host: "bastion", User: "ssh-user", Port: 22,
				IdentityFile: "/original-key", KnownHosts: "/original-hosts",
				PassphraseCommand: "original-passphrase-helper", SSHPasswordCommand: "original-password-helper",
			},
		}
	}
	tr := i18n.EnglishTranslationSet()

	for _, tc := range []struct {
		name string
		edit func(*connForm)
		want func(*models.SSHTunnelConfig)
	}{
		{"host", func(f *connForm) { f.setTextValue(fieldSSHHost, "edited-host") }, func(s *models.SSHTunnelConfig) { s.Host = "edited-host" }},
		{"user", func(f *connForm) { f.setTextValue(fieldSSHUser, "edited-user") }, func(s *models.SSHTunnelConfig) { s.User = "edited-user" }},
		{"port", func(f *connForm) { f.setTextValue(fieldSSHPort, "2222") }, func(s *models.SSHTunnelConfig) { s.Port = 2222 }},
		{"identity file", func(f *connForm) { f.setTextValue(fieldSSHIdentityFile, "/edited-key") }, func(s *models.SSHTunnelConfig) { s.IdentityFile = "/edited-key" }},
		{"known hosts", func(f *connForm) { f.setTextValue(fieldSSHKnownHosts, "/edited-hosts") }, func(s *models.SSHTunnelConfig) { s.KnownHosts = "/edited-hosts" }},
		{"agent normalization", func(f *connForm) { f.sshAuth = sshAuthAgent }, func(s *models.SSHTunnelConfig) { s.IdentityFromAgent = true; s.IdentityFile = "" }},
		{"password normalization", func(f *connForm) { f.sshAuth = sshAuthPassword }, func(s *models.SSHTunnelConfig) { s.IdentityFromAgent = false; s.IdentityFile = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := profile()
			want := profile()
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			c.OpenEditForm(original, nil, testDrivers)
			if c.form.conn.SSHTunnel == original.SSHTunnel {
				t.Error("edit draft aliases stored tunnel")
			}
			if !reflect.DeepEqual(c.form.conn, want) {
				t.Fatal("opening edit changed untouched fields")
			}
			tc.edit(&c.form)
			conn, isEdit, name, ok := c.FormValidateAll(tr)
			if !ok || !isEdit || name != original.Name {
				t.Fatal("edited form did not validate with original metadata")
			}
			if !reflect.DeepEqual(original, want) {
				t.Error("editing or validation mutated stored profile")
			}
			expected := profile()
			tc.want(expected.SSHTunnel)
			if !reflect.DeepEqual(conn, expected) {
				t.Error("validated output lost edited or untouched fields")
			}
			if conn.SSHTunnel.PassphraseCommand != want.SSHTunnel.PassphraseCommand ||
				conn.SSHTunnel.SSHPasswordCommand != want.SSHTunnel.SSHPasswordCommand ||
				!conn.ReadOnly || !conn.ConfirmWrites || !conn.ConfirmDDL {
				t.Error("edited output lost credential helpers or safety flags")
			}
			if conn.SSHTunnel == c.form.conn.SSHTunnel || conn.SSHTunnel == original.SSHTunnel {
				t.Error("validated output aliases form or stored tunnel")
			}
			c.SetMode(ModeList)
			c.OpenEditForm(original, nil, testDrivers)
			if !reflect.DeepEqual(c.FormConnection(), want) {
				t.Error("reopening cancelled draft did not restore original values")
			}
		})
	}

	for _, tc := range []struct {
		name string
		edit func(*models.SSHTunnelConfig)
	}{
		{"host", func(s *models.SSHTunnelConfig) { s.Host = "snapshot-host" }},
		{"user", func(s *models.SSHTunnelConfig) { s.User = "snapshot-user" }},
		{"port", func(s *models.SSHTunnelConfig) { s.Port = 2222 }},
		{"identity file", func(s *models.SSHTunnelConfig) { s.IdentityFile = "/snapshot-key" }},
		{"agent", func(s *models.SSHTunnelConfig) { s.IdentityFromAgent = true }},
		{"known hosts", func(s *models.SSHTunnelConfig) { s.KnownHosts = "/snapshot-hosts" }},
		{"passphrase command", func(s *models.SSHTunnelConfig) { s.PassphraseCommand = "snapshot-passphrase-helper" }},
		{"password command", func(s *models.SSHTunnelConfig) { s.SSHPasswordCommand = "snapshot-password-helper" }},
	} {
		t.Run("snapshot/"+tc.name, func(t *testing.T) {
			original := profile()
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			c.OpenEditForm(original, nil, testDrivers)
			snapshot := c.FormConnection()
			tc.edit(snapshot.SSHTunnel)
			if !reflect.DeepEqual(c.FormConnection(), profile()) || !reflect.DeepEqual(original, profile()) {
				t.Error("snapshot mutation leaked into draft or stored profile")
			}
		})
	}

	t.Run("snapshot survives later edits and normalization", func(t *testing.T) {
		c := newTestConnectionManager(&captureDriver{}, nil, nil)
		c.OpenEditForm(profile(), nil, testDrivers)
		snapshot := c.FormConnection()
		c.form.setTextValue(fieldSSHHost, "later-host")
		c.form.sshAuth = sshAuthAgent
		conn, _, _, ok := c.FormValidateAll(tr)
		if !ok || !conn.SSHTunnel.IdentityFromAgent || conn.SSHTunnel.IdentityFile != "" {
			t.Fatal("agent normalization not reflected in saved output")
		}
		if !reflect.DeepEqual(snapshot, profile()) {
			t.Error("later form edits mutated earlier snapshot")
		}
		conn.SSHTunnel.Host = "save-consumer-host"
		if c.FormConnection().SSHTunnel.Host != "later-host" {
			t.Error("save consumer mutated form draft")
		}
	})

	t.Run("failed validation normalizes only draft", func(t *testing.T) {
		original := profile()
		c := newTestConnectionManager(&captureDriver{}, nil, nil)
		c.OpenEditForm(original, nil, testDrivers)
		c.form.setTextValue(fieldSSHHost, "")
		c.form.sshAuth = sshAuthAgent
		if _, _, _, ok := c.FormValidateAll(tr); ok || c.form.err == "" {
			t.Fatal("incomplete SSH draft did not retain visible validation failure")
		}
		if !reflect.DeepEqual(original, profile()) {
			t.Error("failed validation mutated original profile")
		}
	})

	t.Run("nil and disabled tunnels", func(t *testing.T) {
		original := models.Connection{Name: "no-ssh", Driver: "postgres"}
		c := newTestConnectionManager(&captureDriver{}, nil, nil)
		c.OpenEditForm(original, nil, testDrivers)
		if c.FormConnection().SSHTunnel != nil || c.form.sshEnabled || c.form.sshAuth != sshAuthAgent {
			t.Fatal("nil tunnel was not preserved with default derived auth")
		}
		conn, _, _, ok := c.FormValidateAll(tr)
		if !ok || conn.SSHTunnel != nil {
			t.Fatal("nil tunnel changed on validation")
		}
		c.form.toggle(fieldUseSSHTunnel)
		c.form.setTextValue(fieldSSHHost, "new-host")
		c.form.setTextValue(fieldSSHUser, "new-user")
		if original.SSHTunnel != nil {
			t.Error("enabling draft tunnel modified original nil tunnel")
		}
		c.OpenEditForm(profile(), nil, testDrivers)
		c.form.toggle(fieldUseSSHTunnel)
		conn, _, _, ok = c.FormValidateAll(tr)
		if !ok || conn.SSHTunnel != nil {
			t.Error("disabled tunnel not dropped from saved output")
		}
	})

	for _, auth := range []string{sshAuthAgent, sshAuthKeyFile, sshAuthPassword} {
		t.Run("derived auth/"+auth, func(t *testing.T) {
			original := profile()
			original.SSHTunnel.IdentityFromAgent = auth == sshAuthAgent
			if auth == sshAuthPassword {
				original.SSHTunnel.IdentityFile = ""
			}
			want := *original.SSHTunnel
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			c.OpenEditForm(original, nil, testDrivers)
			if !c.form.sshEnabled || c.form.sshAuth != auth || !reflect.DeepEqual(*c.form.conn.SSHTunnel, want) {
				t.Fatal("opening draft lost derived auth or untouched SSH fields")
			}
			if _, _, _, ok := c.FormValidateAll(tr); !ok || !reflect.DeepEqual(*original.SSHTunnel, want) {
				t.Error("derived auth validation changed stored tunnel")
			}
		})
	}
}

func TestFormConnectionMaterializesSSHWithoutMutatingDraft(t *testing.T) {
	for _, tc := range []struct {
		name, auth, host, user string
		fresh, disabled        bool
		wantError              error
	}{
		{name: "disable existing", disabled: true},
		{name: "enable agent default", fresh: true, auth: sshAuthAgent, host: "new-host", user: "new-user"},
		{name: "agent selection", auth: sshAuthAgent, host: "edited-host", user: "edited-user"},
		{name: "password selection", auth: sshAuthPassword, host: "edited-host", user: "edited-user"},
		{name: "key selection", auth: sshAuthKeyFile, host: "edited-host", user: "edited-user"},
		{name: "enabled empty tunnel", fresh: true, auth: sshAuthAgent, wantError: session.ErrSSHTunnelHostMissing},
		{name: "missing user", fresh: true, auth: sshAuthAgent, host: "new-host", wantError: session.ErrSSHTunnelUserMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := models.Connection{Name: "profile", Driver: "postgres", Host: "db", ReadOnly: true, ConfirmWrites: true, ConfirmDDL: true}
			if !tc.fresh {
				original.SSHTunnel = &models.SSHTunnelConfig{Host: "old-host", User: "old-user", IdentityFile: "/key", KnownHosts: "/hosts", PassphraseCommand: "synthetic-key-helper", SSHPasswordCommand: "synthetic-password-helper"}
			}
			stored := copyFormConnection(original)
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			c.OpenEditForm(original, nil, testDrivers)
			if tc.fresh || tc.disabled {
				c.form.toggle(fieldUseSSHTunnel)
			}
			if !tc.disabled {
				for range len(sshAuthOptions) {
					if c.form.sshAuth == tc.auth {
						break
					}
					c.form.toggle(fieldSSHAuthMethod)
				}
				c.form.setTextValue(fieldSSHHost, tc.host)
				c.form.setTextValue(fieldSSHUser, tc.user)
			}
			draft := copyFormConnection(c.form.conn)
			revision := c.FormRevision()
			probe := c.FormConnection()
			if !reflect.DeepEqual(c.form.conn, draft) || !reflect.DeepEqual(original, stored) || !c.FormMatches(revision) {
				t.Fatal("reading a probe snapshot mutated the draft, stored profile, or probe revision")
			}
			if tc.disabled {
				if probe.SSHTunnel != nil {
					t.Fatal("disabled tunnel was included in the probe")
				}
			} else {
				if probe.SSHTunnel == nil || probe.SSHTunnel.IdentityFromAgent != (tc.auth == sshAuthAgent) {
					t.Fatal("probe does not reflect the selected SSH authentication method")
				}
				if tc.auth != sshAuthKeyFile && probe.SSHTunnel.IdentityFile != "" {
					t.Fatal("unselected identity file leaked into probe configuration")
				}
				if probe.SSHTunnel == c.form.conn.SSHTunnel || probe.SSHTunnel == original.SSHTunnel {
					t.Fatal("probe aliases mutable tunnel state")
				}
			}
			if err := session.ValidateSSHTunnel(probe.SSHTunnel); !errors.Is(err, tc.wantError) {
				t.Error("probe snapshot did not retain the expected SSH validation outcome")
			}
			saved, _, _, ok := c.FormValidateAll(i18n.EnglishTranslationSet())
			if ok != (tc.wantError == nil) {
				t.Fatal("save and probe validation disagree")
			}
			if ok && !reflect.DeepEqual(probe, saved) {
				t.Error("save and probe snapshots differ")
			}
			if !reflect.DeepEqual(original, stored) {
				t.Error("saving the draft mutated the stored profile")
			}
		})
	}
}
