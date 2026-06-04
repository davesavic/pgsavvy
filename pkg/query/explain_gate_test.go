package query

import "testing"

func TestEffectiveAnalyze(t *testing.T) {
	tests := []struct {
		name      string
		sql       string
		readOnly  bool
		requested bool
		want      bool
	}{
		{
			name:      "requested false is always false",
			sql:       "SELECT 1",
			readOnly:  false,
			requested: false,
			want:      false,
		},
		{
			name:      "SELECT on writable conn allowed",
			sql:       "SELECT * FROM users",
			readOnly:  false,
			requested: true,
			want:      true,
		},
		{
			name:      "UPDATE on writable conn denied",
			sql:       "UPDATE users SET name = 'x'",
			readOnly:  false,
			requested: true,
			want:      false,
		},
		{
			name:      "writable-CTE DELETE on writable conn denied (fail closed)",
			sql:       "WITH d AS (DELETE FROM users RETURNING *) SELECT * FROM d",
			readOnly:  false,
			requested: true,
			want:      false,
		},
		{
			name:      "writable-CTE INSERT on writable conn denied (fail closed)",
			sql:       "WITH n AS (INSERT INTO logs VALUES (1) RETURNING id) SELECT * FROM n",
			readOnly:  false,
			requested: true,
			want:      false,
		},
		{
			name:      "UPDATE on read-only conn allowed (server rejects writes)",
			sql:       "UPDATE users SET name = 'x'",
			readOnly:  true,
			requested: true,
			want:      true,
		},
		{
			name:      "column named updated_at does NOT trigger DML scan",
			sql:       "SELECT id, updated_at, deleted_count FROM events",
			readOnly:  false,
			requested: true,
			want:      true,
		},
		{
			name:      "lowercase embedded delete in CTE denied",
			sql:       "with d as (delete from t returning *) select * from d",
			readOnly:  false,
			requested: true,
			want:      false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EffectiveAnalyze(tt.sql, tt.readOnly, tt.requested)
			if got != tt.want {
				t.Errorf("EffectiveAnalyze(%q, readOnly=%v, requested=%v) = %v, want %v",
					tt.sql, tt.readOnly, tt.requested, got, tt.want)
			}
		})
	}
}
