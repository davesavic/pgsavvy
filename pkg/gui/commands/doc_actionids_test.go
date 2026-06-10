package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestDocActionIDs is a doc-lint guard: every action ID cited in the
// user-facing docs/keybindings.md must resolve to a real action declared
// in actions.go (via AllActionIDs). It fails the build if the doc drifts
// and cites a stale or misspelled action ID.
//
// Repo-root resolution: docs/ lives at the repo root, which has no Go
// package, so `//go:embed` cannot reach it from this package. Instead we
// locate the test file via runtime.Caller(0) and walk UP parent
// directories until we find go.mod, then read <repoRoot>/docs/keybindings.md.
//
// Extraction convention (kept precise to avoid false positives like
// `config.yml` or `cross_cutting.md`): we lint action IDs from exactly two
// machine-readable shapes in the doc, both unambiguous:
//
//  1. The dedicated ```action-ids fenced block — one action ID per line.
//  2. Every `action: <id>` line inside the YAML example snippets.
//
// The unbind sentinel `<nop>` is NOT an action ID; it is excluded.
func TestDocActionIDs(t *testing.T) {
	repoRoot := findRepoRoot(t)
	docPath := filepath.Join(repoRoot, "docs", "keybindings.md")

	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	doc := string(data)

	cited := citedActionIDs(doc)
	if len(cited) == 0 {
		t.Fatal("no action IDs extracted from docs/keybindings.md; extraction convention may be broken")
	}

	known := make(map[string]struct{}, len(AllActionIDs()))
	for _, id := range AllActionIDs() {
		known[id] = struct{}{}
	}

	for _, id := range cited {
		if _, ok := known[id]; !ok {
			t.Errorf("docs/keybindings.md cites action ID %q which does not resolve in actions.go (AllActionIDs)", id)
		}
	}
}

// findRepoRoot walks up from this test file's directory until it finds a
// go.mod, returning that directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked up from %s without finding go.mod", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// actionIDShape matches the dot-namespaced action-ID convention
// (family.name or family.subfamily.name, lowercase + digits + underscore).
var actionIDShape = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// actionLine matches `action: <id>` (the YAML examples), capturing the id.
var actionLine = regexp.MustCompile(`(?m)^\s*(?:-\s*)?action:\s*(\S+)`)

// citedActionIDs extracts action IDs from the two machine-readable shapes:
// the ```action-ids fenced block and the `action:` YAML lines. `<nop>` is
// excluded (it is the unbind sentinel, not an action ID). Returns a
// deduplicated slice.
func citedActionIDs(doc string) []string {
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || id == "<nop>" {
			return
		}
		if !actionIDShape.MatchString(id) {
			return
		}
		seen[id] = struct{}{}
	}

	// 1) The dedicated ```action-ids fenced block.
	for _, block := range fencedBlocks(doc, "action-ids") {
		for line := range strings.SplitSeq(block, "\n") {
			add(line)
		}
	}

	// 2) `action: <id>` lines in YAML examples.
	for _, m := range actionLine.FindAllStringSubmatch(doc, -1) {
		add(m[1])
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

// fencedBlocks returns the bodies of every ```<info> ... ``` block whose
// info string equals info.
func fencedBlocks(doc, info string) []string {
	var blocks []string
	lines := strings.Split(doc, "\n")
	inBlock := false
	var buf []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```"+info {
				inBlock = true
				buf = nil
			}
			continue
		}
		if trimmed == "```" {
			blocks = append(blocks, strings.Join(buf, "\n"))
			inBlock = false
			continue
		}
		buf = append(buf, line)
	}
	return blocks
}
