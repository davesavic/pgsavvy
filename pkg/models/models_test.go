package models

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestModelsCompile(t *testing.T) {
	_ = Connection{}
	_ = SSHTunnelConfig{}
	_ = Database{}
	_ = Schema{}
	var tbl Table
	_ = &tbl
	_ = Column{}
	_ = Index{}
	_ = Constraint{}
	_ = Row{}
	_ = Result{}
	_ = Query{Timeout: time.Second}
	_ = Plan{}
	_ = PlanNode{}
	_ = QueryID{SessionID: SessionID(1), BackendPID: 42, Started: time.Now(), Nonce: 7}
	_ = ColumnMeta{}
	_ = TxOptions{}
	_ = TxStatus(TxActive)
	_ = FunctionDetail{Args: []FunctionArg{{}}}
}

func TestTableAtomicZeroValue(t *testing.T) {
	var tbl Table
	if got := tbl.EstimatedRows.Load(); got != 0 {
		t.Fatalf("EstimatedRows zero value = %d, want 0", got)
	}
	if got := tbl.SizeBytes.Load(); got != 0 {
		t.Fatalf("SizeBytes zero value = %d, want 0", got)
	}
}

func TestConnectionYAMLRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   Connection
	}{
		{
			name: "full profile with ssh tunnel",
			in: Connection{
				Name:             "prod-pg",
				Driver:           "postgres",
				DSN:              "postgres://app@db.prod:5432/app",
				PasswordCommand:  "op read 'op://Prod/db/password'",
				Role:             "app_readonly",
				SSHTunnel:        &SSHTunnelConfig{Host: "bastion.prod", User: "deploy", Port: 22, IdentityFile: "~/.ssh/id_ed25519"},
				Tags:             []string{"production", "postgres"},
				Color:            "#ff4d4d",
				Label:            "PROD",
				Icon:             "⚠",
				ReadOnly:         true,
				ConfirmWrites:    true,
				ConfirmDDL:       true,
				StatementTimeout: "30s",
				HiddenSchemas:    []string{"audit_*", "_partitions_*"},
			},
		},
		{
			name: "minimal profile, no ssh tunnel",
			in: Connection{
				Name:   "local-pg",
				Driver: "postgres",
				DSN:    "postgres://localhost:5432/dev",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := yaml.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got Connection
			if err := yaml.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(tc.in, got) {
				t.Fatalf("round-trip mismatch\nwant: %#v\ngot:  %#v\nyaml:\n%s", tc.in, got, out)
			}
		})
	}
}

func TestConnectionYAMLEmptySSHTunnelIsNil(t *testing.T) {
	const src = `name: x
driver: postgres
dsn: postgres://localhost/db
`
	var got Connection
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SSHTunnel != nil {
		t.Fatalf("SSHTunnel = %#v, want nil", got.SSHTunnel)
	}
}

func TestConnectionYAMLSnakeCaseKeys(t *testing.T) {
	c := Connection{
		Name: "x", Driver: "postgres", DSN: "postgres://localhost/db",
		ReadOnly: true, ConfirmWrites: true, ConfirmDDL: true,
		StatementTimeout: "30s",
		SSHTunnel:        &SSHTunnelConfig{Host: "h", User: "u", Port: 22, IdentityFile: "/k"},
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"read_only:", "confirm_writes:", "confirm_ddl:", "statement_timeout:", "ssh_tunnel:", "identity_file:"} {
		if !strings.Contains(string(out), key) {
			t.Errorf("missing snake_case key %q in marshalled output:\n%s", key, out)
		}
	}
}
