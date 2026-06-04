package pg

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestEnrichedExplainSQL(t *testing.T) {
	const sql = "SELECT 1"
	tests := []struct {
		name     string
		analyze  bool
		wantJSON string
		wantText string
	}{
		{
			name:     "analyze permitted enriches with ANALYZE/BUFFERS/VERBOSE/SETTINGS",
			analyze:  true,
			wantJSON: "EXPLAIN (ANALYZE, BUFFERS, VERBOSE, SETTINGS, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN ANALYZE SELECT 1",
		},
		{
			name:     "analyze denied is estimate-only with VERBOSE/SETTINGS",
			analyze:  false,
			wantJSON: "EXPLAIN (VERBOSE, SETTINGS, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON, gotText := enrichedExplainSQL(sql, tt.analyze)
			if gotJSON != tt.wantJSON {
				t.Errorf("jsonSQL = %q, want %q", gotJSON, tt.wantJSON)
			}
			if gotText != tt.wantText {
				t.Errorf("textSQL = %q, want %q", gotText, tt.wantText)
			}
		})
	}
}

func TestBareExplainSQL(t *testing.T) {
	const sql = "SELECT 1"
	tests := []struct {
		name     string
		analyze  bool
		wantJSON string
		wantText string
	}{
		{
			name:     "analyze fallback",
			analyze:  true,
			wantJSON: "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN ANALYZE SELECT 1",
		},
		{
			name:     "estimate fallback",
			analyze:  false,
			wantJSON: "EXPLAIN (FORMAT JSON) SELECT 1",
			wantText: "EXPLAIN SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON, gotText := bareExplainSQL(sql, tt.analyze)
			if gotJSON != tt.wantJSON {
				t.Errorf("jsonSQL = %q, want %q", gotJSON, tt.wantJSON)
			}
			if gotText != tt.wantText {
				t.Errorf("textSQL = %q, want %q", gotText, tt.wantText)
			}
		})
	}
}

func TestIsUnsupportedExplainOption(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "syntax error 42601 triggers fallback", err: &pgconn.PgError{Code: "42601"}, want: true},
		{name: "wrapped syntax error", err: errors.Join(errors.New("ctx"), &pgconn.PgError{Code: "42601"}), want: true},
		{name: "other pg error does not", err: &pgconn.PgError{Code: "42P01"}, want: false},
		{name: "plain error does not", err: errors.New("boom"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnsupportedExplainOption(tt.err); got != tt.want {
				t.Errorf("isUnsupportedExplainOption(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
