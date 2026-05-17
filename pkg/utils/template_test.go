package utils

import (
	"strings"
	"testing"
)

func TestResolveTemplate_ValidRendersWithData(t *testing.T) {
	got, err := ResolveTemplate("hello {{.Name}}", map[string]string{"Name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("got %q, want %q", got, "hello world")
	}
}

func TestResolveTemplate_EmptyTemplateReturnsEmptyNoError(t *testing.T) {
	got, err := ResolveTemplate("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestResolveTemplate_MalformedNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ResolveTemplate panicked: %v", r)
		}
	}()
	got, err := ResolveTemplate("{{.Foo", nil)
	if err == nil {
		t.Fatal("expected non-nil error on malformed template")
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if !strings.Contains(err.Error(), "template") {
		t.Fatalf("error %q should contain 'template'", err.Error())
	}
}

func TestResolveTemplate_ExecuteErrorReturnsError(t *testing.T) {
	got, err := ResolveTemplate("{{.Missing.Field}}", struct{}{})
	if err == nil {
		t.Fatal("expected non-nil error on bad field access")
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
