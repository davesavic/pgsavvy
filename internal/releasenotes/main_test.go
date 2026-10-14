package main

import "testing"

func TestValidateReleaseNotes(t *testing.T) {
	tests := []struct {
		name    string
		notes   string
		wantErr bool
	}{
		{
			name:  "matching tag with notes",
			notes: "pgsavvy v0.11.3\n\n  Fixed diagnostics.\n\npgsavvy v0.11.2\n\n  Older notes.\n",
		},
		{
			name:  "matching section after another release",
			notes: "pgsavvy v0.11.4\n\n  Newer notes.\n\npgsavvy v0.11.3\n\n  Fixed diagnostics.",
		},
		{
			name:  "CRLF",
			notes: "pgsavvy v0.11.3\r\n\r\n  Fixed diagnostics.\r\n",
		},
		{
			name:    "missing tag",
			notes:   "pgsavvy v0.11.2\n\n  Older notes.\n",
			wantErr: true,
		},
		{
			name:    "tag prefix is not an exact match",
			notes:   "pgsavvy v0.11.30\n\n  Different release.\n",
			wantErr: true,
		},
		{
			name:    "mention is not a heading",
			notes:   "  See pgsavvy v0.11.3 for details.\n",
			wantErr: true,
		},
		{
			name:    "empty entry at EOF",
			notes:   "pgsavvy v0.11.3\n",
			wantErr: true,
		},
		{
			name:    "older notes do not fill an empty entry",
			notes:   "pgsavvy v0.11.3\n\npgsavvy v0.11.2\n\n  Older notes.\n",
			wantErr: true,
		},
		{
			name:    "whitespace is not notes",
			notes:   "pgsavvy v0.11.3\n\n \t \n",
			wantErr: true,
		},
		{
			name:    "empty file",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReleaseNotes(tt.notes, "v0.11.3")
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateReleaseNotes() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
