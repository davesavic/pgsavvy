package ui

import (
	"testing"

	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestNavigationTargetRejectsChangedOrigin(t *testing.T) {
	for _, mode := range []string{"switch-back", "close", "close-all", "supersede", "reattach", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			helper := NewResultTabsHelper(ResultTabsHelperDeps{})
			if err := helper.OpenResultTab("origin", nil); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(helper.CloseAll)
			origin := helper.Active()
			origin.Grid().SetColumns([]models.ColumnMeta{{Name: "id"}})
			target := helper.CaptureNavigation()
			if !target.Current() {
				t.Fatal("fresh origin was not admitted")
			}
			switch mode {
			case "switch-back":
				if err := helper.OpenResultTab("other", nil); err != nil {
					t.Fatal(err)
				}
				helper.SwitchToTabByID("1")
			case "close":
				if err := helper.CloseActive(); err != nil {
					t.Fatal(err)
				}
			case "close-all":
				helper.CloseAll()
			case "supersede":
				helper.CaptureNavigation()
			case "reattach":
				origin.SetIdentity("different", origin.resultIdentity)
			case "shutdown":
				helper.CancelNavigation()
			}
			if target.Current() || target.AdoptSort(nil, "stale", "original") {
				t.Fatal("changed origin accepted a stale sort publication")
			}
		})
	}
}
