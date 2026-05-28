package orchestrator

import "testing"

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
