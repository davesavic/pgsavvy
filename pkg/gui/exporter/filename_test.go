package exporter

import (
	"strings"
	"testing"
	"time"
)

func TestSanitizeComponent_AllowedCharsPassthrough(t *testing.T) {
	in := "abc123._-"
	got := SanitizeComponent(in)
	if got != "abc123._-" {
		t.Fatalf("expected passthrough %q, got %q", in, got)
	}
}

func TestSanitizeComponent_ReplacesIllegal(t *testing.T) {
	// "../../../etc/passwd"
	// after char-pass: ".._.._.._etc_passwd"
	// after TrimLeft('.'): "_.._.._etc_passwd"
	got := SanitizeComponent("../../../etc/passwd")
	want := "_.._.._etc_passwd"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeComponent_StripsLeadingDots(t *testing.T) {
	got := SanitizeComponent(".hidden")
	if got != "hidden" {
		t.Fatalf("expected %q, got %q", "hidden", got)
	}
	got = SanitizeComponent("...still")
	if got != "still" {
		t.Fatalf("expected %q, got %q", "still", got)
	}
}

func TestSanitizeComponent_TruncatesAt100(t *testing.T) {
	in := strings.Repeat("a", 200)
	got := SanitizeComponent(in)
	if len(got) != 100 {
		t.Fatalf("expected length 100, got %d", len(got))
	}
}

func TestSanitizeComponent_EmptyReturnsUnderscore(t *testing.T) {
	got := SanitizeComponent("")
	if got != "_" {
		t.Fatalf("expected %q, got %q", "_", got)
	}
}

func TestDefaultFilename_Shape(t *testing.T) {
	ts := time.Date(2026, 5, 20, 14, 30, 45, 0, time.UTC)
	got := DefaultFilename("local", "public.users", "csv", ts)
	want := "local_public.users_20260520T143045Z.csv"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestContainedUnder_AcceptsChild(t *testing.T) {
	if !ContainedUnder("/tmp/d", "/tmp/d/file.csv") {
		t.Fatal("expected child path to be contained")
	}
}

func TestContainedUnder_RejectsParent(t *testing.T) {
	if ContainedUnder("/tmp/d", "/tmp/other.csv") {
		t.Fatal("expected sibling path to be rejected")
	}
}

func TestContainedUnder_RejectsTraversal(t *testing.T) {
	// /tmp/d/../etc.csv → /tmp/etc.csv, outside /tmp/d
	if ContainedUnder("/tmp/d", "/tmp/d/../etc.csv") {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestContainedUnder_RejectsSelf(t *testing.T) {
	if ContainedUnder("/tmp/d", "/tmp/d") {
		t.Fatal("expected dir==candidate to be rejected")
	}
}
