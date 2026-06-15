package orchestrator

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

// TestRowEndpoint covers the connection-picker endpoint suffix: DSN-derived,
// the discrete-field fallback for DSN-less connections, and the empty cases.
func TestRowEndpoint(t *testing.T) {
	cases := []struct {
		name string
		conn *models.Connection
		want string
	}{
		{"nil", nil, ""},
		{"dsn only", &models.Connection{DSN: "postgres://u@dbhost:5432/mydb"}, "dbhost/mydb"},
		{
			"discrete only (no dsn)",
			&models.Connection{Host: "disc.host", Database: "discdb"},
			"disc.host/discdb",
		},
		{
			"dsn wins over discrete",
			&models.Connection{DSN: "postgres://u@dbhost/mydb", Host: "ignored", Database: "ignored"},
			"dbhost/mydb",
		},
		{"empty both", &models.Connection{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rowEndpoint(c.conn); got != c.want {
				t.Errorf("rowEndpoint = %q, want %q", got, c.want)
			}
		})
	}
}
