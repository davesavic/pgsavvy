package models

// Connection describes a single database connection profile loaded from
// configuration. It is plain data; behavior lives in the drivers/session layers.
type Connection struct {
	Name   string `yaml:"name"`
	Driver string `yaml:"driver"`
	DSN    string `yaml:"dsn"`
	// Password is a plaintext fallback for development only. Production deployments MUST prefer PasswordCommand, KeyringRef, or PgpassPath. Never log this value; never serialize back to config files written by the app. See DESIGN.md §11.2 / §15.6.
	Password         string           `yaml:"password,omitempty"`
	PasswordCommand  string           `yaml:"password_command,omitempty"`
	KeyringRef       string           `yaml:"keyring,omitempty"`
	PgpassPath       string           `yaml:"pgpass,omitempty"`
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
	Host         string `yaml:"host"`
	User         string `yaml:"user"`
	Port         int    `yaml:"port"`
	IdentityFile string `yaml:"identity_file"`
}
