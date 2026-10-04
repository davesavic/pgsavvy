package orchestrator

import (
	"context"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/drivers"
	"github.com/stretchr/testify/require"
)

func TestSchemaSearchPathSQL(t *testing.T) {
	tests := []struct {
		name        string
		schema      string
		wantSQL     string
		wantDisplay string
	}{
		{
			name:        "plain schema is quoted with public fallback",
			schema:      "myschema",
			wantSQL:     `SET search_path TO "myschema", public`,
			wantDisplay: "myschema, public",
		},
		{
			name:        "public itself omits the redundant fallback",
			schema:      "public",
			wantSQL:     `SET search_path TO "public"`,
			wantDisplay: "public",
		},
		{
			name:        "injection payload is double-quote escaped",
			schema:      `app"; DROP TABLE users--`,
			wantSQL:     `SET search_path TO "app""; DROP TABLE users--", public`,
			wantDisplay: `app"; DROP TABLE users--, public`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotDisplay := schemaSearchPathSQL(tt.schema)
			if gotSQL != tt.wantSQL {
				t.Errorf("sql = %q, want %q", gotSQL, tt.wantSQL)
			}
			if gotDisplay != tt.wantDisplay {
				t.Errorf("display = %q, want %q", gotDisplay, tt.wantDisplay)
			}
		})
	}
}

func TestReview_settings_schemaActivation(t *testing.T) {
	for _, schema := range []string{"public", "a,b", `say"hi`} {
		t.Run(schema, func(t *testing.T) {
			fake := &reviewSettingsSession{}
			name := reviewSettingsRegister(t, func(context.Context) (drivers.Driver, error) {
				return &reviewSettingsDriver{conn: &reviewSettingsConn{sess: fake}}, nil
			})
			g, store, _, _ := reviewSettingsGui(t, name, "")
			g.activateSchema(&connectInvoker{g: g, helper: g.connectHelper}, schema)
			g.spinnerState.workersWG.Wait()
			saved, err := store.CanonicalSessionSettingsSnapshot("conn")
			require.NoError(t, err)
			names := []string{schema}
			if schema != "public" {
				names = append(names, "public")
			}
			require.Equal(t, decodedSetting(names...), saved["search_path"])
			require.Empty(t, store.LastSessionSettingsSnapshot("conn"))
			want, _ := schemaSearchPathSQL(schema)
			require.Equal(t, []string{want}, fake.queries)
		})
	}
}

func TestReview_settings_schemaPanicReleasesWriter(t *testing.T) {
	fake := &reviewSettingsSession{panicSet: true}
	name := reviewSettingsRegister(t, func(context.Context) (drivers.Driver, error) {
		return &reviewSettingsDriver{conn: &reviewSettingsConn{sess: fake}}, nil
	})
	g, _, _, _ := reviewSettingsGui(t, name, "")
	g.activateSchema(&connectInvoker{g: g, helper: g.connectHelper}, "public")
	g.spinnerState.workersWG.Wait()
	require.Equal(t, []string{`SET search_path TO "public"`}, fake.queries)
	locked := g.settingsRunMu.TryLock()
	if locked {
		g.settingsRunMu.Unlock()
	}
	require.True(t, locked, "recovered worker panic must not strand other tracked writers")
}
