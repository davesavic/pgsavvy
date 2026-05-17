package models

// Connection describes a single database connection profile loaded from
// configuration. It is plain data; behavior lives in the drivers/session layers.
type Connection struct {
	Name string
	// Driver names the backend driver (e.g. "postgres", "mysql").
	Driver string
	// DSN is the data source name used to dial the database.
	DSN string
	// Password is a plaintext fallback for development only. Production deployments MUST prefer PasswordCommand, KeyringRef, or PgpassPath. Never log this value; never serialize back to config files written by the app. See DESIGN.md §11.2 / §15.6.
	Password         string           `yaml:"password,omitempty"`
	PasswordCommand  string           `yaml:"password_command,omitempty"`
	KeyringRef       string           `yaml:"keyring,omitempty"`
	PgpassPath       string           `yaml:"pgpass,omitempty"`
	SSHTunnel        *SSHTunnelConfig `yaml:"ssh_tunnel,omitempty"`
	Tags             []string         `yaml:"tags,omitempty"`
	Color            string
	Label            string
	Icon             string
	ReadOnly         bool
	ConfirmWrites    bool
	ConfirmDDL       bool
	StatementTimeout string   `yaml:"statement_timeout,omitempty"`
	HiddenSchemas    []string `yaml:"hidden_schemas,omitempty"`
	Role             string   `yaml:"role,omitempty"`
}

// SSHTunnelConfig describes an SSH tunnel used to reach the database host.
type SSHTunnelConfig struct {
	Host         string
	User         string
	Port         int
	IdentityFile string
}
