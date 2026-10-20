package context

import (
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
)

func TestCLIHistoryForm_DefaultAndEdit(t *testing.T) {
	for _, edit := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			source := models.Connection{Name: "dev", CLIHistory: enabled, ReadOnly: true, CLIAccess: models.CLIAccessMetadata}
			sourceSnapshot := source
			if edit {
				c.OpenEditForm(source, []string{"dev"}, testDrivers)
			} else {
				c.OpenAddForm(nil, testDrivers)
				enabled = false
			}
			before := c.FormConnection()
			var found bool
			for i, spec := range c.form.focusableSpecs() {
				if spec.id == fieldCLIHistory {
					found = true
					c.form.focus = i
					require.Equal(t, fieldToggle, spec.kind)
					require.Equal(t, fieldCLIAccess, c.form.focusableSpecs()[i-1].id)
					require.Equal(t, boolDisplay(enabled), c.form.displayValue(spec))
					break
				}
			}
			require.True(t, found)
			original, changed := c.FormCLIHistoryChange()
			require.Equal(t, edit && enabled, original)
			require.False(t, changed)
			c.form.msgWidth = 24
			body := c.form.render()
			require.Len(t, strings.Split(strings.TrimSuffix(body, "\n"), "\n"), len(c.form.focusableSpecs()), "only field rows belong in the form")
			require.Contains(t, body, "> cli_history:")
			c.FormToggleFocused()
			after := c.FormConnection()
			require.Equal(t, !enabled, after.CLIHistory)
			require.Equal(t, before.CLIAccess, after.CLIAccess)
			require.Equal(t, before.ReadOnly, after.ReadOnly)
			original, changed = c.FormCLIHistoryChange()
			require.Equal(t, edit && enabled, original)
			require.Equal(t, edit, changed)
			c.FormToggleFocused()
			_, changed = c.FormCLIHistoryChange()
			require.False(t, changed, "toggle back must preserve latest disk consent")
			require.Equal(t, before.CLIHistory, c.FormConnection().CLIHistory)
			require.Equal(t, sourceSnapshot, source, "form mutated the caller's profile")
		}
	}
}

func TestCLIHistoryForm_NavigationOnlyVisitsFields(t *testing.T) {
	for _, edit := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			c := newTestConnectionManager(&captureDriver{}, nil, nil)
			if edit {
				c.OpenEditForm(models.Connection{Name: "dev", CLIHistory: enabled}, []string{"dev"}, testDrivers)
			} else {
				c.OpenAddForm(nil, testDrivers)
				c.form.conn.CLIHistory = enabled
			}
			before := c.FormConnection()
			specs := c.form.focusableSpecs()
			for _, width := range []int{8, 24, 80} {
				c.SetLabelWrapWidth(width)
				for _, delta := range []int{1, -1} {
					for step := range specs {
						i := step
						if delta < 0 {
							i = len(specs) - 1 - step
						}
						require.Equal(t, specs[i].label, c.FormFocusedLabel())
						body := c.form.render()
						require.Len(t, strings.Split(strings.TrimSuffix(body, "\n"), "\n"), len(specs))
						require.Contains(t, body, "> "+specs[i].label+":")
						if step < len(specs)-1 {
							c.FormNavigate(delta)
						}
					}
				}
			}
			require.Equal(t, before, c.FormConnection(), "navigation must not change connection settings")
		}
	}
}
