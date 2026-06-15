package models

// Connection describes a single database connection profile loaded from
// configuration. It is plain data; behavior lives in the drivers/session layers.
type Connection struct {
	Name   string `yaml:"name"`
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn" log:"redact"`
	// Discrete connection fields. When DSN is empty, BuildPgxConfig assembles
	// the connection from these. When DSN is non-empty it wins and these are
	// ignored. A zero-value Connection emits none of these keys (omitempty).
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	User     string `yaml:"user,omitempty"`
	Database string `yaml:"database,omitempty"`
	SSLMode  string `yaml:"sslmode,omitempty"`
	// Password is a plaintext fallback for development only. Production deployments MUST prefer PasswordCommand, KeyringRef, or PgpassPath. Never log this value; never serialize back to config files written by the app. See DESIGN.md §11.2 / §15.6.
	Password         string           `yaml:"password,omitempty" log:"redact"`
	PasswordCommand  string           `yaml:"password_command,omitempty" log:"redact"`
	KeyringRef       string           `yaml:"keyring,omitempty" log:"redact"`
	PgpassPath       string           `yaml:"pgpass,omitempty" log:"redact"`
	SSHTunnel        *SSHTunnelConfig `yaml:"ssh_tunnel,omitempty"`
	Tags             []string         `yaml:"tags,omitempty"`
	Color            string           `yaml:"color,omitempty"`
	Label            string           `yaml:"label,omitempty"`
	Icon             string           `yaml:"icon,omitempty"`
	ReadOnly         bool             `yaml:"read_only,omitempty"`
	ConfirmWrites    bool             `yaml:"confirm_writes,omitempty"`
	ConfirmDDL       bool             `yaml:"confirm_ddl,omitempty"`
	StatementTimeout string           `yaml:"statement_timeout,omitempty"`
	HiddenSchemas    []string         `yaml:"hidden_schemas,omitempty"`
	Role             string           `yaml:"role,omitempty"`
}

// SSHTunnelConfig describes an SSH tunnel used to reach the database host.
type SSHTunnelConfig struct {
	Host              string `yaml:"host"`
	User              string `yaml:"user"`
	Port              int    `yaml:"port,omitempty"`
	IdentityFile      string `yaml:"identity_file,omitempty" log:"redact"`
	IdentityFromAgent bool   `yaml:"identity_from_agent,omitempty"`
	PassphraseCommand string `yaml:"passphrase_command,omitempty" log:"redact"`
	// SSHPasswordCommand resolves the SSH password (ssh.Password auth) by
	// running a command non-interactively. When empty and a SecretPrompter is
	// available, the password is prompted interactively instead.
	SSHPasswordCommand string `yaml:"ssh_password_command,omitempty" log:"redact"`
	KnownHosts         string `yaml:"known_hosts,omitempty"`
}
