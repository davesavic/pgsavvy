package editor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	iofs "io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

func TestBufferPathForRejectsEmptyConnID(t *testing.T) {
	got := bufferPathFor("/state", "", "deadbeef-1234-4567-89ab-cdef01234567")
	if got != "" {
		t.Errorf("bufferPathFor(empty connID) = %q; want \"\"", got)
	}
}

func TestBufferPathForRejectsInvalidUUID(t *testing.T) {
	cases := []string{
		"",
		"not-a-uuid",
		"../../etc/passwd",
		"deadbeef-1234-4567-89ab-cdef0123456",  // 11 trailing hex
		"deadbeef-1234-4567-89ab-cdef012345678", // 13 trailing hex
		"DEADBEEF12344567-89ab-cdef01234567",
	}
	for _, uuid := range cases {
		got := bufferPathFor("/state", "conn", uuid)
		if got != "" {
			t.Errorf("bufferPathFor(%q) = %q; want \"\"", uuid, got)
		}
	}
}

func TestBufferPathForValidUUID(t *testing.T) {
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	connID := "postgres-prod"
	sum := sha256.Sum256([]byte(connID))
	expectedDir := hex.EncodeToString(sum[:8])
	want := filepath.Join("/state", "buffers", expectedDir, uuid+".sql")
	got := bufferPathFor("/state", connID, uuid)
	if got != want {
		t.Errorf("bufferPathFor = %q; want %q", got, want)
	}
}

func TestSaveBufferContentAtomicWrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	err := SaveBufferContent(fs, "/state", "conn", uuid, "SELECT 1;\nSELECT 2;\n")
	if err != nil {
		t.Fatalf("SaveBufferContent: %v", err)
	}
	path := bufferPathFor("/state", "conn", uuid)
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "SELECT 1;\nSELECT 2;\n" {
		t.Errorf("file content = %q; want %q", string(data), "SELECT 1;\nSELECT 2;\n")
	}
	// No leftover tmp file.
	if exists, _ := afero.Exists(fs, path+".tmp"); exists {
		t.Error("tmp file left behind after successful save")
	}
}

func TestSaveBufferContentNoOpOnInvalidPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Empty connID — path resolution returns "" and save is a no-op.
	if err := SaveBufferContent(fs, "/state", "", "deadbeef-1234-4567-89ab-cdef01234567", "x"); err != nil {
		t.Errorf("SaveBufferContent(empty connID) returned %v; want nil", err)
	}
	// Invalid UUID — same.
	if err := SaveBufferContent(fs, "/state", "conn", "bad-uuid", "x"); err != nil {
		t.Errorf("SaveBufferContent(bad uuid) returned %v; want nil", err)
	}
	entries, _ := afero.ReadDir(fs, "/state")
	if len(entries) != 0 {
		t.Errorf("no-op save created dir entries: %v", entries)
	}
}

func TestSaveBufferContentNilFs(t *testing.T) {
	err := SaveBufferContent(nil, "/state", "conn", "deadbeef-1234-4567-89ab-cdef01234567", "x")
	if err == nil {
		t.Fatal("SaveBufferContent(nil fs) = nil; want error")
	}
	if !strings.Contains(err.Error(), "nil fs") {
		t.Errorf("err = %v; want nil-fs message", err)
	}
}

func TestSaveBufferLinesJoinsLines(t *testing.T) {
	fs := afero.NewMemMapFs()
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	lines := []Line{
		{Runes: []rune("alpha")},
		{Runes: []rune("beta")},
		{Runes: []rune("gamma")},
	}
	if err := SaveBufferLines(fs, "/state", "conn", uuid, lines); err != nil {
		t.Fatalf("SaveBufferLines: %v", err)
	}
	data, _ := afero.ReadFile(fs, bufferPathFor("/state", "conn", uuid))
	if string(data) != "alpha\nbeta\ngamma" {
		t.Errorf("joined content = %q; want %q", string(data), "alpha\nbeta\ngamma")
	}
}

func TestLoadBufferMissingFileReturnsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	buf, err := LoadBuffer(fs, "/state", "conn", uuid)
	if err != nil {
		t.Fatalf("LoadBuffer on missing: %v", err)
	}
	if buf == nil {
		t.Fatal("LoadBuffer returned nil buffer")
	}
	if buf.Marks == nil {
		t.Error("LoadBuffer returned buffer with nil Marks")
	}
	if buf.Jumps == nil {
		t.Error("LoadBuffer returned buffer with nil Jumps")
	}
	if buf.ConnectionID != "conn" || buf.UUID != uuid {
		t.Errorf("buf metadata = (%q, %q); want (\"conn\", %q)", buf.ConnectionID, buf.UUID, uuid)
	}
	if buf.Dirty {
		t.Error("fresh buffer has Dirty=true")
	}
}

func TestLoadBufferHydratesContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	if err := SaveBufferContent(fs, "/state", "conn", uuid, "SELECT *\nFROM users;"); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	buf, err := LoadBuffer(fs, "/state", "conn", uuid)
	if err != nil {
		t.Fatalf("LoadBuffer: %v", err)
	}
	if len(buf.Lines) != 2 {
		t.Fatalf("Lines count = %d; want 2", len(buf.Lines))
	}
	if string(buf.Lines[0].Runes) != "SELECT *" {
		t.Errorf("Lines[0] = %q; want %q", string(buf.Lines[0].Runes), "SELECT *")
	}
	if string(buf.Lines[1].Runes) != "FROM users;" {
		t.Errorf("Lines[1] = %q; want %q", string(buf.Lines[1].Runes), "FROM users;")
	}
	if buf.Cursor.Line != 0 || buf.Cursor.Col != 0 {
		t.Errorf("Cursor = %+v; want (0,0)", buf.Cursor)
	}
	if buf.Dirty {
		t.Error("hydrated buffer has Dirty=true")
	}
}

func TestLoadBufferInvalidUUIDReturnsEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	buf, err := LoadBuffer(fs, "/state", "conn", "../escape")
	if err != nil {
		t.Fatalf("LoadBuffer with bad uuid: %v", err)
	}
	if buf == nil {
		t.Fatal("expected non-nil empty buffer")
	}
	if len(buf.Lines) != 0 {
		t.Errorf("buf.Lines = %v; want empty", buf.Lines)
	}
}

func TestLoadBufferReadErrorPropagates(t *testing.T) {
	fs := afero.NewReadOnlyFs(afero.NewMemMapFs())
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	// File missing on a read-only fs returns iofs.ErrNotExist (still no error).
	buf, err := LoadBuffer(fs, "/state", "conn", uuid)
	if err != nil {
		t.Fatalf("LoadBuffer missing on RO fs: %v", err)
	}
	if buf == nil {
		t.Fatal("LoadBuffer returned nil")
	}
	if errors.Is(err, iofs.ErrNotExist) {
		t.Error("LoadBuffer surfaced ErrNotExist instead of fresh empty buffer")
	}
}

func TestListBuffersFiltersByUUIDRegex(t *testing.T) {
	fs := afero.NewMemMapFs()
	valid := "deadbeef-1234-4567-89ab-cdef01234567"
	if err := SaveBufferContent(fs, "/state", "conn", valid, "x"); err != nil {
		t.Fatalf("save valid: %v", err)
	}
	dir := bufferDirFor("/state", "conn")
	// Plant a junk file in the same directory.
	if err := afero.WriteFile(fs, filepath.Join(dir, "junk.txt"), []byte("nope"), 0o600); err != nil {
		t.Fatalf("plant junk: %v", err)
	}
	if err := afero.WriteFile(fs, filepath.Join(dir, "not-a-uuid.sql"), []byte("nope"), 0o600); err != nil {
		t.Fatalf("plant bad-name sql: %v", err)
	}
	got, err := ListBuffers(fs, "/state", "conn")
	if err != nil {
		t.Fatalf("ListBuffers: %v", err)
	}
	if len(got) != 1 || got[0] != valid {
		t.Errorf("ListBuffers = %v; want [%q]", got, valid)
	}
}

func TestListBuffersEmptyConnID(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := ListBuffers(fs, "/state", "")
	if err != nil || got != nil {
		t.Errorf("ListBuffers(empty connID) = (%v, %v); want (nil, nil)", got, err)
	}
}

func TestListBuffersMissingDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	got, err := ListBuffers(fs, "/state", "never-saved")
	if err != nil || got != nil {
		t.Errorf("ListBuffers on missing dir = (%v, %v); want (nil, nil)", got, err)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	uuid := "deadbeef-1234-4567-89ab-cdef01234567"
	content := "WITH cte AS (SELECT 1)\nSELECT * FROM cte;"
	if err := SaveBufferContent(fs, "/state", "conn", uuid, content); err != nil {
		t.Fatalf("save: %v", err)
	}
	buf, err := LoadBuffer(fs, "/state", "conn", uuid)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := buf.String(); got != content {
		t.Errorf("round-trip content = %q; want %q", got, content)
	}
}
