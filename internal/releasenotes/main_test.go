package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestReleaseNotesSection(t *testing.T) {
	tests := []struct {
		name  string
		notes string
		want  string
	}{
		{
			name:  "exact section between other releases",
			notes: "pgsavvy v0.11.30\n\n  Newer.\n\npgsavvy v0.11.3\n\n  Selected.\n  Second line.\n\npgsavvy v0.11.2\n\n  Older.\n",
			want:  "pgsavvy v0.11.3\n\n  Selected.\n  Second line.\n",
		},
		{
			name:  "CRLF normalized",
			notes: "pgsavvy v0.11.3\r\n\r\n  Selected.\r\n\r\npgsavvy v0.11.2\r\n\r\n  Older.\r\n",
			want:  "pgsavvy v0.11.3\n\n  Selected.\n",
		},
		{
			name:  "EOF without newline",
			notes: "pgsavvy v0.11.3\n\n  Selected.",
			want:  "pgsavvy v0.11.3\n\n  Selected.\n",
		},
		{
			name:  "body mentions another release",
			notes: "pgsavvy v0.11.3\n\n  Replaces pgsavvy v0.11.2.\n",
			want:  "pgsavvy v0.11.3\n\n  Replaces pgsavvy v0.11.2.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := releaseNotesSection(tt.notes, "v0.11.3")
			if err != nil || got != tt.want {
				t.Fatalf("releaseNotesSection() = %q, %v; want %q, nil", got, err, tt.want)
			}
		})
	}
}

func TestRun(t *testing.T) {
	const valid = "pgsavvy v0.11.4\n\n  Newer.\n\npgsavvy v0.11.3\n\n  Selected.\n\npgsavvy v0.11.2\n\n  Older.\n"
	tests := []struct {
		name   string
		args   []string
		notes  string
		absent bool
		want   string
		failed bool
	}{
		{name: "legacy validation is silent", args: []string{"v0.11.3"}, notes: valid},
		{name: "print selected section", args: []string{"--print", "v0.11.3"}, notes: valid, want: "pgsavvy v0.11.3\n\n  Selected.\n"},
		{name: "print CRLF", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.3\r\n\r\n  Selected.\r\n", want: "pgsavvy v0.11.3\n\n  Selected.\n"},
		{name: "unknown tag", args: []string{"--print", "v0.11.5"}, notes: valid, failed: true},
		{name: "legacy unknown tag", args: []string{"v0.11.5"}, notes: valid, failed: true},
		{name: "exact header required", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.30\n\n  Wrong.\n", failed: true},
		{name: "header whitespace is not exact", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.3 \n\n  Wrong.\n", failed: true},
		{name: "empty entry does not borrow older notes", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.3\n\npgsavvy v0.11.2\n\n  Older.\n", failed: true},
		{name: "empty first duplicate does not borrow later notes", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.3\n\npgsavvy v0.11.3\n\n  Later.\n", failed: true},
		{name: "whitespace only entry", args: []string{"--print", "v0.11.3"}, notes: "pgsavvy v0.11.3\r\n \t\r\n", failed: true},
		{name: "empty file", args: []string{"--print", "v0.11.3"}, failed: true},
		{name: "missing file", args: []string{"--print", "v0.11.3"}, absent: true, failed: true},
		{name: "missing args", notes: valid, failed: true},
		{name: "empty tag", args: []string{""}, notes: valid, failed: true},
		{name: "print without tag", args: []string{"--print"}, notes: valid, failed: true},
		{name: "print empty tag", args: []string{"--print", ""}, notes: valid, failed: true},
		{name: "extra args", args: []string{"--print", "v0.11.3", "extra"}, notes: valid, failed: true},
		{name: "unknown flag", args: []string{"--unknown", "v0.11.3"}, notes: valid, failed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if !tt.absent {
				if err := os.WriteFile(filepath.Join(dir, "RELEASE_NOTES.txt"), []byte(tt.notes), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(dir)
			var stdout, stderr bytes.Buffer
			exit := run(tt.args, &stdout, &stderr)
			wantExit := 0
			if tt.failed {
				wantExit = 1
			}
			if exit != wantExit || (stderr.Len() > 0) != tt.failed || stdout.String() != tt.want {
				t.Fatalf("run() = exit %d, stdout %q, stderr %q; want stdout %q, failed %t", exit, stdout.String(), stderr.String(), tt.want, tt.failed)
			}
		})
	}
}

type failedWriter struct {
	short bool
}

func (w failedWriter) Write([]byte) (int, error) {
	if w.short {
		return 0, nil
	}
	return 0, errors.New("output unavailable")
}

func TestRunOutputFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "RELEASE_NOTES.txt"), []byte("pgsavvy v0.11.3\n\n  Selected.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, short := range []bool{false, true} {
		var stderr bytes.Buffer
		if exit := run([]string{"--print", "v0.11.3"}, failedWriter{short: short}, &stderr); exit != 1 || !strings.Contains(stderr.String(), "release notes output failed:") {
			t.Fatalf("run() = exit %d, stderr %q; want output failure", exit, stderr.String())
		}
	}
}
