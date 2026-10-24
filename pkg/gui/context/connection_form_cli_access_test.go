package context

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/i18n"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
)

func TestForm_CLIAccessSelector(t *testing.T) {
	for _, initial := range []models.CLIAccess{"", models.CLIAccessDisabled, models.CLIAccessMetadata, models.CLIAccessRowRead, models.CLIAccessFull} {
		t.Run(string(initial), func(t *testing.T) {
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			source := models.Connection{Name: "dev", CLIAccess: initial}
			c.OpenEditForm(source, []string{"dev"}, testDrivers)
			var spec connFieldSpec
			found := false
			for i, s := range c.form.focusableSpecs() {
				if s.label == "cli_access" {
					spec, c.form.focus, found = s, i, true
					break
				}
			}
			if !found || spec.kind != fieldSelector || c.FormFocusedIsText() {
				t.Fatal("CLI access must be an available selector, not free text")
			}
			current := source.EffectiveCLIAccess()
			if got := c.form.displayValue(spec); got != string(current) {
				t.Fatalf("display = %q, want %q", got, current)
			}
			if !strings.Contains(c.form.render(), "cli_access:") {
				t.Fatal("selector missing from rendered form")
			}
			if original, changed := c.FormCLIAccessChange(); original != initial || changed {
				t.Fatal("opening the form marked the policy as changed")
			}
			// A text setter must never allow arbitrary policy values.
			c.FormSetFocusedValue("unrestricted")
			if c.FormConnection().CLIAccess != initial {
				t.Fatal("selector accepted free text")
			}
			next := map[models.CLIAccess]models.CLIAccess{
				models.CLIAccessDisabled: models.CLIAccessMetadata,
				models.CLIAccessMetadata: models.CLIAccessRowRead,
				models.CLIAccessRowRead:  models.CLIAccessFull,
				models.CLIAccessFull:     models.CLIAccessDisabled,
			}
			for range 8 {
				current = next[current]
				c.FormToggleFocused()
				conn, editing, name, ok := c.FormValidateAll(i18n.EnglishTranslationSet())
				if !ok || !editing || name != "dev" || conn.CLIAccess != current || c.form.displayValue(spec) != string(current) {
					t.Fatalf("selected policy = %q, want %q (valid=%v)", conn.CLIAccess, current, ok)
				}
				if original, changed := c.FormCLIAccessChange(); original != initial || changed != (current != source.EffectiveCLIAccess()) {
					t.Fatalf("policy edit intent = %q/%v, current = %q", original, changed, current)
				}
			}
			if source.CLIAccess != initial {
				t.Fatal("selector mutated source profile")
			}
		})
	}
}

func TestForm_CLIAccessSelectorSaveReload(t *testing.T) {
	for _, writes := range []bool{false, true} {
		for _, ddl := range []bool{false, true} {
			t.Run(fmt.Sprintf("writes_%t_ddl_%t", writes, ddl), func(t *testing.T) {
				fs := afero.NewOsFs()
				path := filepath.Join(t.TempDir(), "connections.yml")
				source := models.Connection{Name: "dev", CLIAccess: models.CLIAccessRowRead, ConfirmWrites: writes, ConfirmDDL: ddl}
				if err := config.SaveConnections(fs, path, []models.Connection{source}); err != nil {
					t.Fatal(err)
				}
				for _, selected := range []models.CLIAccess{models.CLIAccessFull, models.CLIAccessDisabled} {
					c := newTestConnectionManager(&captureDriver{}, nil, nil)
					c.OpenEditForm(source, []string{"dev"}, testDrivers)
					for i, spec := range c.form.focusableSpecs() {
						if spec.id == fieldCLIAccess {
							c.form.focus = i
							break
						}
					}
					c.FormToggleFocused()
					conn, editing, name, ok := c.FormValidateAll(i18n.EnglishTranslationSet())
					original, changed := c.FormCLIAccessChange()
					if !ok || !editing || !changed || original != source.CLIAccess || conn.CLIAccess != selected {
						t.Fatalf("selection = %q, want %q (valid=%v, changed=%v)", conn.CLIAccess, selected, ok, changed)
					}
					if err := config.UpdateConnectionWithCLIAccess(fs, path, name, conn, original); err != nil {
						t.Fatal(err)
					}
					got, err := config.LoadConnections(fs, path)
					if err != nil || len(got) != 1 || got[0].CLIAccess != selected || got[0].ConfirmWrites != writes || got[0].ConfirmDDL != ddl {
						t.Fatalf("reloaded selection/confirmation flags = %v, %v", got, err)
					}
					source = got[0]
				}
			})
		}
	}
}
