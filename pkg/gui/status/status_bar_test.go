package status

import (
	"strings"
	"testing"

	"github.com/davesavic/dbsavvy/pkg/i18n"
	"github.com/davesavic/dbsavvy/pkg/models"
)

func TestBuildStatusLine_NilTranslationSet(t *testing.T) {
	if got := BuildStatusLine("", nil, nil, nil, 0, 0, "", nil, nil); got != "" {
		t.Fatalf("BuildStatusLine(nil tr) = %q, want empty", got)
	}
}

func TestBuildStatusLine_NoConnOmitsHeaderSlot(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, nil)

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

	got := BuildStatusLine("", conn, nil, tr, 0, 0, "", nil, nil)

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

	got := BuildStatusLine("", conn, nil, tr, 0, 0, "", nil, nil)

	if strings.Contains(got, tr.ReadOnlyTag) {
		t.Fatalf("got %q must not contain RO tag when ReadOnly=false", got)
	}
}

func TestBuildStatusLine_IconAndLabel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "⚠", Label: "PROD"}

	got := BuildStatusLine("", conn, nil, tr, 0, 0, "", nil, nil)

	if !strings.Contains(got, "⚠ PROD") {
		t.Fatalf("got %q, want substring %q", got, "⚠ PROD")
	}
}

func TestBuildStatusLine_OptionsRendered(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	opts := []string{"q:quit", "?:help"}

	got := BuildStatusLine("", nil, opts, tr, 0, 0, "", nil, nil)

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
		got := BuildStatusLine("", c.conn, c.opts, tr, 0, 0, "", nil, nil)
		if !strings.HasSuffix(got, tr.OptionsBarMore) {
			t.Fatalf("%s: got %q, want suffix %q", c.name, got, tr.OptionsBarMore)
		}
	}
}

// TestBuildStatusLine_TwoLines verifies the populated status content is
// split into exactly two lines joined by a single "\n": line 1 carries
// the mode banner + connection header, line 2 carries the options and
// the trailing more-hint (fc2.2 — status bar grown to 2 rows).
func TestBuildStatusLine_TwoLines(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "⚠", Label: "PROD"}
	opts := []string{"q:quit"}

	got := BuildStatusLine("-- INSERT --", conn, opts, tr, 0, 0, "", nil, nil)

	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("got %q with %d newlines, want exactly 1 (two lines)", got, n)
	}
	lines := strings.SplitN(got, "\n", 2)
	line1, line2 := lines[0], lines[1]

	if !strings.Contains(line1, "-- INSERT --") {
		t.Errorf("line1 %q missing mode banner", line1)
	}
	if !strings.Contains(line1, "⚠ PROD") {
		t.Errorf("line1 %q missing connection header", line1)
	}
	if !strings.Contains(line2, "q:quit") {
		t.Errorf("line2 %q missing options", line2)
	}
	if !strings.HasSuffix(line2, tr.OptionsBarMore) {
		t.Errorf("line2 %q must end with the more-hint", line2)
	}
	// The more-hint lives on line 2, not line 1.
	if strings.Contains(line1, tr.OptionsBarMore) {
		t.Errorf("line1 %q must not carry the more-hint", line1)
	}
}

// TestBuildStatusLine_NoConnStillTwoLines verifies that with no active
// connection the output still renders two lines: line 1 is the empty
// no-conn slot, line 2 carries the more-hint (fc2.2 AC).
func TestBuildStatusLine_NoConnStillTwoLines(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, nil)

	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("got %q with %d newlines, want exactly 1 (two lines)", got, n)
	}
	lines := strings.SplitN(got, "\n", 2)
	if lines[0] != "" {
		t.Errorf("line1 = %q, want empty no-conn slot", lines[0])
	}
	if !strings.HasSuffix(lines[1], tr.OptionsBarMore) {
		t.Errorf("line2 %q must end with the more-hint", lines[1])
	}
}

// TestBuildStatusLine_NilTrNoSpuriousNewline verifies the empty edge:
// a nil translation set yields "" with no spurious "\n" (fc2.2 AC).
func TestBuildStatusLine_NilTrNoSpuriousNewline(t *testing.T) {
	got := BuildStatusLine("", nil, nil, nil, 0, 0, "", nil, nil)
	if got != "" {
		t.Fatalf("got %q, want empty string with no newline", got)
	}
}

// TestBuildStatusLine_ModeLabelPrepended verifies modeLabel becomes the
// FIRST section of the status line, ahead of the connection header.
func TestBuildStatusLine_ModeLabelPrepended(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "⚠", Label: "PROD"}

	got := BuildStatusLine("-- COMMAND --", conn, nil, tr, 0, 0, "", nil, nil)

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
// slot is omitted (no leading separator) when modeLabel is "".
func TestBuildStatusLine_EmptyModeLabelOmitsSlot(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, nil)

	if strings.HasPrefix(got, sectionSep) {
		t.Fatalf("got %q must not start with section separator when modeLabel empty", got)
	}
}

// A connection with a named colour must produce an
// ANSI SGR foreground wrapper around its icon+label header so the
// status bar surface visibly tints the active-connection slot. The
// reset escape MUST follow the header so subsequent sections (options /
// "?: more") render in the default foreground.
func TestBuildStatusLine_ConnColorTintsHeader(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "*", Label: "local-pg", Color: "red"}

	got := BuildStatusLine("", conn, nil, tr, 0, 0, "", nil, nil)

	if !strings.Contains(got, "\x1b[31m* local-pg\x1b[0m") {
		t.Fatalf("got %q; want substring %q", got, "\x1b[31m* local-pg\x1b[0m")
	}
}

// Edge: an unrecognised colour token (hex) must NOT emit an ANSI
// escape — the header falls through to plain text so we never write a
// malformed sequence to the cell buffer.
func TestBuildStatusLine_ConnHexColorIsNotTinted(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	conn := &models.Connection{Icon: "*", Label: "stg", Color: "#abcdef"}

	got := BuildStatusLine("", conn, nil, tr, 0, 0, "", nil, nil)

	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("got %q must not contain an ANSI escape for an unrecognised colour token", got)
	}
	if !strings.Contains(got, "* stg") {
		t.Fatalf("got %q; want plain '* stg' header", got)
	}
}

// TestBuildStatusLine_NilConnWithModeLabel covers the (nil conn,
// non-empty modeLabel) edge.
func TestBuildStatusLine_NilConnWithModeLabel(t *testing.T) {
	tr := i18n.EnglishTranslationSet()

	got := BuildStatusLine("-- INSERT --", nil, nil, tr, 0, 0, "", nil, nil)

	if !strings.HasPrefix(got, "-- INSERT --") {
		t.Fatalf("got %q, want prefix %q", got, "-- INSERT --")
	}
	if !strings.HasSuffix(got, tr.OptionsBarMore) {
		t.Fatalf("got %q, want suffix %q", got, tr.OptionsBarMore)
	}
}

// --- Transaction indicator tests ---

// TestBuildStatusLine_TxActiveNoSavepoints verifies [TX] rendered with
// WarningFg (yellow ANSI) when tx is active with no savepoints.
func TestBuildStatusLine_TxActiveNoSavepoints(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxActive, nil, nil)

	wantTag := "\x1b[33m[TX]\x1b[0m"
	if !strings.Contains(got, wantTag) {
		t.Fatalf("got %q, want substring %q", got, wantTag)
	}
}

// TestBuildStatusLine_TxActiveEmptySavepointsRendersTX verifies that an
// empty savepoint slice with active tx renders [TX] not [TX:].
func TestBuildStatusLine_TxActiveEmptySavepointsRendersTX(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxActive, []string{}, nil)

	wantTag := "\x1b[33m[TX]\x1b[0m"
	if !strings.Contains(got, wantTag) {
		t.Fatalf("got %q, want substring %q", got, wantTag)
	}
	if strings.Contains(got, "[TX:]") {
		t.Fatalf("got %q, must not contain [TX:] for empty savepoint list", got)
	}
}

// TestBuildStatusLine_TxActiveWithSavepoints verifies [TX:sp1,sp2] is
// rendered with WarningFg when savepoints are present.
func TestBuildStatusLine_TxActiveWithSavepoints(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxActive, []string{"sp1", "sp2"}, nil)

	wantTag := "\x1b[33m[TX:sp1,sp2]\x1b[0m"
	if !strings.Contains(got, wantTag) {
		t.Fatalf("got %q, want substring %q", got, wantTag)
	}
}

// TestBuildStatusLine_TxAbortedInTx verifies [TX*] is rendered with
// ErrorFg (red ANSI) when tx is in aborted_in_tx state.
func TestBuildStatusLine_TxAbortedInTx(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxAbortedInTx, nil, nil)

	wantTag := "\x1b[31m[TX*]\x1b[0m"
	if !strings.Contains(got, wantTag) {
		t.Fatalf("got %q, want substring %q", got, wantTag)
	}
}

// TestBuildStatusLine_NoTxIndicatorWhenInactive verifies no TX indicator
// appears when txStatus is the zero value (no transaction).
func TestBuildStatusLine_NoTxIndicatorWhenInactive(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, nil)

	if strings.Contains(got, "[TX") {
		t.Fatalf("got %q, must not contain TX indicator when inactive", got)
	}
}

// TestBuildStatusLine_NoTxIndicatorWhenCommitted verifies no TX indicator
// for committed transactions.
func TestBuildStatusLine_NoTxIndicatorWhenCommitted(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxCommitted, nil, nil)

	if strings.Contains(got, "[TX") {
		t.Fatalf("got %q, must not contain TX indicator when committed", got)
	}
}

// TestBuildStatusLine_NoTxIndicatorWhenRolledBack verifies no TX indicator
// for rolled-back transactions.
func TestBuildStatusLine_NoTxIndicatorWhenRolledBack(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, models.TxRolledBack, nil, nil)

	if strings.Contains(got, "[TX") {
		t.Fatalf("got %q, must not contain TX indicator when rolled back", got)
	}
}

// --- Session settings tests ---

// TestBuildStatusLine_SessionSettings_SearchPath verifies [search_path=…]
// appears in the status line when the settings map contains search_path.
func TestBuildStatusLine_SessionSettings_SearchPath(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	settings := map[string]string{"search_path": "app,public"}
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, settings)

	want := "[search_path=app,public]"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q", got, want)
	}
}

// TestBuildStatusLine_SessionSettings_Role verifies [role=…] appears in
// the status line when the settings map contains role.
func TestBuildStatusLine_SessionSettings_Role(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	settings := map[string]string{"role": "app_readonly"}
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, settings)

	want := "[role=app_readonly]"
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q", got, want)
	}
}

// TestBuildStatusLine_SessionSettings_EmptyMap verifies no settings
// section appears when the settings map is empty.
func TestBuildStatusLine_SessionSettings_EmptyMap(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	settings := map[string]string{}
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, settings)

	if strings.Contains(got, "[search_path=") || strings.Contains(got, "[role=") {
		t.Fatalf("got %q, must not contain settings tags for empty map", got)
	}
}

// TestBuildStatusLine_SessionSettings_Nil verifies no crash and no
// settings section when the settings map is nil.
func TestBuildStatusLine_SessionSettings_Nil(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, nil)

	if strings.Contains(got, "[search_path=") || strings.Contains(got, "[role=") {
		t.Fatalf("got %q, must not contain settings tags for nil map", got)
	}
}

// TestBuildStatusLine_SessionSettings_EmptyValue verifies empty-string
// values are not displayed in the settings section.
func TestBuildStatusLine_SessionSettings_EmptyValue(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	settings := map[string]string{"search_path": "", "role": "", "time_zone": ""}
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, settings)

	if strings.Contains(got, "[search_path=") || strings.Contains(got, "[role=") || strings.Contains(got, "[time_zone=") {
		t.Fatalf("got %q, must not contain settings tags for empty values", got)
	}
}

// TestBuildStatusLine_SessionSettings_LongSearchPath verifies truncation
// of search_path values exceeding settingsSearchPathMaxLen.
func TestBuildStatusLine_SessionSettings_LongSearchPath(t *testing.T) {
	tr := i18n.EnglishTranslationSet()
	longPath := strings.Repeat("schema_name,", 5) // 60 chars, exceeds 40
	settings := map[string]string{"search_path": longPath}
	got := BuildStatusLine("", nil, nil, tr, 0, 0, "", nil, settings)

	if !strings.Contains(got, "...") {
		t.Fatalf("got %q, want truncated search_path with '...'", got)
	}
	// Full value must NOT appear.
	if strings.Contains(got, longPath) {
		t.Fatalf("got %q, must not contain full untruncated search_path", got)
	}
}

func TestSearchIndicator_Inactive(t *testing.T) {
	if got := SearchIndicator("alic", 3, 40, false); got != "" {
		t.Fatalf("SearchIndicator(active=false) = %q, want empty", got)
	}
	// query/counts are ignored when inactive — even a populated query
	// must yield the empty segment so the caller appends nothing.
	if got := SearchIndicator("", 0, 0, false); got != "" {
		t.Fatalf("SearchIndicator(empty, inactive) = %q, want empty", got)
	}
}

func TestSearchIndicator_ActiveWithMatches(t *testing.T) {
	got := SearchIndicator("alic", 3, 40, true)
	want := "search: alic 3/40"
	if got != want {
		t.Fatalf("SearchIndicator(active, total>0) = %q, want %q", got, want)
	}
}

func TestSearchIndicator_ActiveNoMatches(t *testing.T) {
	got := SearchIndicator("alic", 0, 0, true)
	want := "search: alic 0/0" + optionSep + "no matches"
	if got != want {
		t.Fatalf("SearchIndicator(active, total==0) = %q, want %q", got, want)
	}
}
