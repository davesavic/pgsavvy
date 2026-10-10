package controllers

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/davesavic/pgsavvy/pkg/gui/commands"
	"github.com/davesavic/pgsavvy/pkg/gui/controllers/helpers/ui"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func TestReverseHandlerBuildsPerConstraintValues(t *testing.T) {
	tabs := ui.NewResultTabsHelper(ui.ResultTabsHelperDeps{})
	if err := tabs.OpenResultTab("parent", nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tabs.CloseAll)
	gv := tabs.Active().Grid()
	gv.SetColumns([]models.ColumnMeta{
		{Name: "code", PhysicalName: "id", TableOID: 42, TableAttributeNumber: 1},
		{Name: "other", PhysicalName: "code", TableOID: 42, TableAttributeNumber: 2},
		{Name: "tenant", PhysicalName: "tenant", TableOID: 42, TableAttributeNumber: 3},
	})
	gv.SetEditability(true, []int{0}, "", models.Ref{Schema: "app", Table: "parents"})
	gv.AppendRows([]models.Row{{Values: []any{1, 99, 7}}})
	fks := []models.ForeignKey{
		{Columns: []string{"parent_id"}, RefColumns: []string{"id"}},
		{Columns: []string{"parent_code"}, RefColumns: []string{"code"}},
		{Columns: []string{"tenant", "parent_code"}, RefColumns: []string{"tenant", "code"}},
		{Columns: []string{"absent"}, RefColumns: []string{"unloaded"}},
	}
	var entries []ReverseEntry
	controller := NewResultTabsController(nil, CoreDeps{}, UIDeps{}, EditDeps{
		ReverseFKLookup:     func(context.Context, string, string) ([]models.ForeignKey, error) { return fks, nil },
		OpenFKReversePicker: func(resolved []ReverseEntry, _ FKReverseOriginTab, _, _ int) bool { entries = resolved; return true },
	}, tabs)
	if err := controller.fkReverseHandler(commands.ExecCtx{}); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 || !reflect.DeepEqual(entries[0].PKValues, []any{1}) || !reflect.DeepEqual(entries[1].PKValues, []any{99}) || !reflect.DeepEqual(entries[2].PKValues, []any{7, 99}) || entries[3].UnavailableReason == "" || len(entries[3].PKValues) != 0 {
		t.Fatal("reverse handler reused row identity instead of per-constraint referenced values")
	}
}

func TestReverseReferencedPhysicalValues(t *testing.T) {
	cols := []models.ColumnMeta{
		{Name: "code", PhysicalName: "id", TableOID: 42},
		{Name: "label", PhysicalName: "code", TableOID: 42},
		{Name: "tenant", PhysicalName: "tenant", TableOID: 42},
	}
	row := []any{1, 99, 7}
	for _, tc := range []struct {
		refs []string
		want []any
	}{
		{[]string{"id"}, []any{1}},
		{[]string{"code"}, []any{99}},
		{[]string{"code", "tenant"}, []any{99, 7}},
		{[]string{"tenant", "code"}, []any{7, 99}},
	} {
		fk := models.ForeignKey{RefColumns: tc.refs, Columns: make([]string, len(tc.refs))}
		got, reason := reverseReferencedValues(fk, cols, row, 42)
		if reason != "" || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("references %v bound %v, want %v, reason %q", tc.refs, got, tc.want, reason)
		}
	}
	fk := models.ForeignKey{Columns: []string{"parent_code"}, RefColumns: []string{"code"}}
	for _, projection := range [][]models.ColumnMeta{cols[:1], {{Name: "code", TableOID: 42}}, {{Name: "code", PhysicalName: "code", TableOID: 99}}, {cols[1], cols[1]}} {
		if values, reason := reverseReferencedValues(fk, projection, row, 42); values != nil || reason == "" {
			t.Fatal("missing, unresolved, foreign or ambiguous referenced projection was selectable")
		}
	}
}

func TestReverseUnavailableEntryHasNoNavigationSideEffects(t *testing.T) {
	ctx, _ := newReversePickerContext()
	tree, runner, tabs, jumps, toast := &fakeReverseTree{}, &fakeReverseRunner{}, &fakeReverseTabs{}, &fakeReverseJumps{}, &fakeReverseToast{}
	controller := NewFKReversePickerController(nil, CoreDeps{}, FKReversePickerDeps{Context: ctx, Tree: tree, Runner: runner, Tabs: tabs, Jumps: jumps, Toast: toast})
	fk := models.ForeignKey{Schema: "app", Table: "children", Columns: []string{"parent_code"}, RefColumns: []string{"code"}}
	entry := ReverseEntry{FK: fk, PKValues: []any{99}, UnavailableReason: "referenced column is not loaded: code"}
	if !strings.Contains(reverseBody(entry), entry.UnavailableReason) {
		t.Fatal("picker did not explain why navigation is unavailable")
	}
	controller.Open([]ReverseEntry{entry, {FK: fk, PKValues: []any{99}}}, nil, 0, 0)
	if err := controller.Select(commands.ExecCtx{}); err != nil {
		t.Fatal(err)
	}
	if runner.Captured().SQL != "" || tabs.calls != 0 || len(jumps.pushed) != 0 || tree.pops != 0 || len(toast.msgs) == 0 {
		t.Fatal("unavailable FK caused a query, jump, tab allocation or popup dismissal")
	}
	ctx.NextTab()
	if err := controller.Select(commands.ExecCtx{}); err != nil {
		t.Fatal(err)
	}
	if runner.Captured().Args[0] != 99 || tabs.calls != 1 || len(jumps.pushed) != 1 || tree.pops != 1 {
		t.Fatal("an unavailable FK disabled another usable entry")
	}
}
