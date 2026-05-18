package status

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestBuildStatusLine_NilTranslationSet(t *testing.T) {
	if got := BuildStatusLine("", nil, nil, nil); got != "" {
		t.Fatalf("BuildStatusLine(nil tr) = %q, want empty", got)
	}
}

func TestBuildStatusLine_NoConnOmitsHeaderSlot(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr)

	if !strings.HasSuffix(got, tr.OptionsBarMore) {
		t.Fatalf("got %q, want suffix %q", got, tr.OptionsBarMore)
	}
	if strings.Contains(got, tr.ReadOnlyTag) {
		t.Fatalf("got %q must not contain RO tag when no conn", got)
	}
}

func TestBuildStatusLine_ReadOnlyTagPresent(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Label: "prod", ReadOnly: true}

	got := BuildStatusLine("", conn, nil, tr)

	if !strings.Contains(got, tr.ReadOnlyTag) {
		t.Fatalf("got %q, want substring %q", got, tr.ReadOnlyTag)
	}
	if !strings.Contains(got, "prod") {
		t.Fatalf("got %q, want substring %q", got, "prod")
	}
}

func TestBuildStatusLine_ReadOnlyTagAbsentWhenNotReadOnly(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Label: "stg", ReadOnly: false}

	got := BuildStatusLine("", conn, nil, tr)

	if strings.Contains(got, tr.ReadOnlyTag) {
		t.Fatalf("got %q must not contain RO tag when ReadOnly=false", got)
	}
}

func TestBuildStatusLine_IconAndLabel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "⚠", Label: "PROD"}

	got := BuildStatusLine("", conn, nil, tr)

	if !strings.Contains(got, "⚠ PROD") {
		t.Fatalf("got %q, want substring %q", got, "⚠ PROD")
	}
}

func TestBuildStatusLine_OptionsRendered(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	opts := []string{"q:quit", "?:help"}

	got := BuildStatusLine("", nil, opts, tr)

	for _, o := range opts {
		if !strings.Contains(got, o) {
			t.Fatalf("got %q, missing option %q", got, o)
		}
	}
}

func TestBuildStatusLine_AlwaysEndsWithOptionsBarMore(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	cases := []struct {
		name string
		conn *models.Connection
		opts []string
	}{
		{"empty", nil, nil},
		{"conn only", &models.Connection{Label: "x"}, nil},
		{"options only", nil, []string{"a"}},
		{"conn+ro+opts", &models.Connection{Label: "p", ReadOnly: true}, []string{"a", "b"}},
	}
	for _, c := range cases {
		got := BuildStatusLine("", c.conn, c.opts, tr)
		if !strings.HasSuffix(got, tr.OptionsBarMore) {
			t.Fatalf("%s: got %q, want suffix %q", c.name, got, tr.OptionsBarMore)
		}
	}
}

// TestBuildStatusLine_ModeLabelPrepended verifies modeLabel becomes the
// FIRST section of the status line, ahead of the connection header. New
// in dlp.9.
func TestBuildStatusLine_ModeLabelPrepended(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "⚠", Label: "PROD"}

	got := BuildStatusLine("-- COMMAND --", conn, nil, tr)

	if !strings.HasPrefix(got, "-- COMMAND --") {
		t.Fatalf("got %q, want prefix %q", got, "-- COMMAND --")
	}
	connIdx := strings.Index(got, "⚠ PROD")
	modeIdx := strings.Index(got, "-- COMMAND --")
	if connIdx == -1 {
		t.Fatalf("got %q, missing connection header", got)
	}
	if modeIdx >= connIdx {
		t.Fatalf("got %q, mode banner must precede connection header", got)
	}
}

// TestBuildStatusLine_EmptyModeLabelOmitsSlot verifies the mode banner
// slot is omitted (no leading separator) when modeLabel is "". New in
// dlp.9.
func TestBuildStatusLine_EmptyModeLabelOmitsSlot(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	got := BuildStatusLine("", nil, nil, tr)

	if strings.HasPrefix(got, sectionSep) {
		t.Fatalf("got %q must not start with section separator when modeLabel empty", got)
	}
}

// TestBuildStatusLine_NilConnWithModeLabel covers the (nil conn,
// non-empty modeLabel) edge listed in dlp.9 AC. New in dlp.9.
func TestBuildStatusLine_NilConnWithModeLabel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	got := BuildStatusLine("-- INSERT --", nil, nil, tr)

	if !strings.HasPrefix(got, "-- INSERT --") {
		t.Fatalf("got %q, want prefix %q", got, "-- INSERT --")
	}
	if !strings.HasSuffix(got, tr.OptionsBarMore) {
		t.Fatalf("got %q, want suffix %q", got, tr.OptionsBarMore)
	}
}
