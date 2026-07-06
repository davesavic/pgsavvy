package context

import (
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
)

func newTestSettings(drv types.GuiDriver, cfg *config.UserConfig) *SettingsContext {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SETTINGS,
		ViewName: string(types.SETTINGS),
		Kind:     types.MAIN_CONTEXT,
		Title:    "Settings",
	})
	deps := SettingsContextDeps{
		ContextTreeDeps: types.ContextTreeDeps{GuiDriver: drv},
		Cfg:             func() *config.UserConfig { return cfg },
	}
	return NewSettingsContext(base, deps)
}

func TestSettingsTabCount(t *testing.T) {
	s := newTestSettings(&captureDriver{}, config.GetDefaultConfig())
	if got := s.TabCount(); got != settingsTabCount {
		t.Errorf("TabCount() = %d, want %d", got, settingsTabCount)
	}
}

func TestSettingsGetKind(t *testing.T) {
	s := newTestSettings(&captureDriver{}, config.GetDefaultConfig())
	if got := s.GetKind(); got != types.MAIN_CONTEXT {
		t.Errorf("GetKind() = %v, want MAIN_CONTEXT", got)
	}
}

func TestSettingsGetKey(t *testing.T) {
	s := newTestSettings(&captureDriver{}, config.GetDefaultConfig())
	if got := s.GetKey(); got != types.SETTINGS {
		t.Errorf("GetKey() = %q, want %q", got, types.SETTINGS)
	}
}

func TestSettingsGetViewName(t *testing.T) {
	s := newTestSettings(&captureDriver{}, config.GetDefaultConfig())
	if got := s.GetViewName(); got != string(types.SETTINGS) {
		t.Errorf("GetViewName() = %q, want %q", got, string(types.SETTINGS))
	}
}

func TestSettingsHandleFocusDeepCopy(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)

	_ = s.HandleFocus(types.OnFocusOpts{})

	edited := s.GetEditedConfig()
	if edited == nil {
		t.Fatal("GetEditedConfig() returned nil after HandleFocus")
	}
	if edited == cfg {
		t.Error("GetEditedConfig() returned the original pointer, want a deep copy")
	}
	if edited.Leader != cfg.Leader {
		t.Errorf("deep copy Leader = %q, want %q", edited.Leader, cfg.Leader)
	}

	edited.Leader = "modified"
	if cfg.Leader == "modified" {
		t.Error("mutating the clone mutated the original")
	}
}

func TestSettingsNilCfgHandleFocusSafe(t *testing.T) {
	base := NewBaseContext(BaseContextOpts{
		Key:      types.SETTINGS,
		ViewName: string(types.SETTINGS),
		Kind:     types.MAIN_CONTEXT,
	})
	deps := SettingsContextDeps{
		ContextTreeDeps: types.ContextTreeDeps{GuiDriver: &captureDriver{}},
	}
	s := NewSettingsContext(base, deps)
	if err := s.HandleFocus(types.OnFocusOpts{}); err != nil {
		t.Fatalf("HandleFocus with nil Cfg: %v", err)
	}
	if s.GetEditedConfig() != nil {
		t.Error("GetEditedConfig() should be nil when Cfg is nil")
	}
}

func TestSettingsGetFocusField(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	if s.GetFocusField() != 0 {
		t.Errorf("initial GetFocusField() = %d, want 0", s.GetFocusField())
	}

	s.SetFocusField(2)
	if s.GetFocusField() != 2 {
		t.Errorf("GetFocusField() after SetFocusField(2) = %d, want 2", s.GetFocusField())
	}

	active := s.ActiveTab()
	st := s.formStates[active]
	maxIdx := len(st.fields) - 1
	if maxIdx < 2 {
		t.Skip("tab has fewer than 3 fields")
	}
	s.SetFocusField(maxIdx + 10)
	if s.GetFocusField() != maxIdx {
		t.Errorf("GetFocusField() after overflow = %d, want clamped to %d", s.GetFocusField(), maxIdx)
	}
}

func TestSettingsFieldCount(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	for tab := 0; tab < settingsTabCount; tab++ {
		s.SetActiveTab(tab)
		n := s.FieldCount()
		if tab == 5 {
			if n != len(cfg.Keybindings) {
				t.Errorf("tab %d FieldCount() = %d, want %d", tab, n, len(cfg.Keybindings))
			}
		} else if n == 0 {
			t.Errorf("tab %d FieldCount() = 0, want > 0", tab)
		}
	}
}

func TestSettingsSetErrorAndClear(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetError("test error")
	st := s.formStates[s.ActiveTab()]
	if st.errorText != "test error" {
		t.Errorf("errorText = %q, want %q", st.errorText, "test error")
	}

	s.ClearError()
	if st.errorText != "" {
		t.Errorf("errorText after clear = %q, want empty", st.errorText)
	}
}

func TestSettingsToggleFocused(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(2)
	original := cfg.UI.Mouse.Enabled

	for i, f := range s.formStates[2].fields {
		if f.kind == settingsFieldToggle {
			s.SetFocusField(i)
			s.ToggleFocused()
			break
		}
	}

	if s.editedConfig.UI.Mouse.Enabled == original {
		t.Error("ToggleFocused() did not flip the toggle value")
	}
}

func TestSettingsRender(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	body := drv.lastContent
	if body == "" {
		t.Fatal("HandleRender wrote empty body")
	}
	if !strings.Contains(body, "leader:") {
		t.Error("tab 0 body missing 'leader:' field")
	}
}

func TestSettingsRenderThemeTab(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})
	s.SetActiveTab(1)

	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 1: %v", err)
	}

	body := drv.lastContent
	for _, want := range []string{"active_border:", "inactive_border:", "keyword:"} {
		if !strings.Contains(body, want) {
			t.Errorf("tab 1 body missing %q", want)
		}
	}
}

func TestSettingsRenderTabs(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	for tab := 0; tab < settingsTabCount; tab++ {
		s.SetActiveTab(tab)
		drv.lastContent = ""
		if err := s.HandleRender(); err != nil {
			t.Fatalf("HandleRender tab %d: %v", tab, err)
		}
		if drv.lastContent == "" && tab != 5 {
			t.Errorf("tab %d body is empty", tab)
		}
	}
}

func TestSettingsNilGuiDriverNoPanic(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(nil, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender nil driver: %v", err)
	}
}

func TestSettingsGetFocusedFieldValue(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Leader = " "
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(0)
	s.SetFocusField(0)

	val := s.GetFocusedFieldValue()
	if val != " " {
		t.Errorf("GetFocusedFieldValue() = %q, want %q", val, " ")
	}
}

func TestSettingsSetFocusedFieldValue(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(0)
	s.SetFocusField(0)

	s.SetFocusedFieldValue("\\")
	if s.editedConfig.Leader != "\\" {
		t.Errorf("after SetFocusedFieldValue, Leader = %q, want %q", s.editedConfig.Leader, "\\")
	}

	s.SetFocusField(1)
	if val := s.GetFocusedFieldValue(); val != "," {
		t.Errorf("local_leader value = %q, want %q", val, ",")
	}
}

func TestSettingsSetLabelWrapWidth(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	s.SetLabelWrapWidth(80)
	if s.msgWidth != 80 {
		t.Errorf("msgWidth = %d, want 80", s.msgWidth)
	}
}

func TestSettingsNeedsRerenderOnWidthChange(t *testing.T) {
	s := newTestSettings(&captureDriver{}, config.GetDefaultConfig())
	if !s.NeedsRerenderOnWidthChange() {
		t.Error("NeedsRerenderOnWidthChange() = false, want true")
	}
}

func TestSettingsSetCfg(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, nil)
	s.SetCfg(func() *config.UserConfig { return cfg })
	_ = s.HandleFocus(types.OnFocusOpts{})
	if s.GetEditedConfig() == nil {
		t.Fatal("GetEditedConfig() nil after SetCfg + HandleFocus")
	}
}

func TestSettingsFocusClampedWithinTab(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(0)
	s.SetFocusField(-5)
	if s.GetFocusField() != 0 {
		t.Errorf("negative focus not clamped to 0: got %d", s.GetFocusField())
	}
	n := s.FieldCount()
	s.SetFocusField(n + 100)
	if s.GetFocusField() != n-1 {
		t.Errorf("overflow focus not clamped to %d: got %d", n-1, s.GetFocusField())
	}
}

func TestSettingsReadOnlyField(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(0)
	found := false
	for i, f := range s.formStates[0].fields {
		if f.hint == "read-only" {
			s.SetFocusField(i)
			found = true
			break
		}
	}
	if !found {
		t.Skip("no read-only field found")
	}
	if s.GetFocusedField().setter != nil {
		t.Error("read-only field should have nil setter")
	}
}

func TestSettingsDisplayToggle(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(2)
	for i, f := range s.formStates[2].fields {
		if f.kind == settingsFieldToggle {
			s.SetFocusField(i)
			s.ToggleFocused()
			break
		}
	}

	_ = s.HandleRender()

	if !strings.Contains(drv.lastContent, "[x]") && !strings.Contains(drv.lastContent, "[ ]") {
		t.Error("toggle display missing [x] or [ ] marker")
	}
}

func TestSettingsKeysTabEmptyRender(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = nil
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 5: %v", err)
	}

	if !strings.Contains(drv.lastContent, "no custom keybindings") {
		t.Error("empty keybindings tab should show placeholder")
	}
}

func TestSettingsKeysTabRenderWithBindings(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "global", Key: "<c-c>", Action: "app.quit", Description: "Quit"},
		{Mode: "n", Scope: "global", Key: "?", Action: "help.cheatsheet", Description: "Help"},
	}
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 5: %v", err)
	}

	for _, want := range []string{"<c-c>", "app.quit", "Quit", "?", "help.cheatsheet"} {
		if !strings.Contains(drv.lastContent, want) {
			t.Errorf("keys tab body missing %q", want)
		}
	}
}

func TestSettingsKeysTabScrollOffset(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = make([]config.KeybindingConfig, 25)
	for i := range cfg.Keybindings {
		cfg.Keybindings[i] = config.KeybindingConfig{
			Mode: "n", Scope: "global", Key: "k", Action: "list.down",
		}
	}
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	s.SetFocusField(24)
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 5: %v", err)
	}

	if !strings.Contains(drv.lastContent, "> ") {
		t.Error("scrolled tab should still show focused marker")
	}
}

func TestSettingsFocusTab5BeforeHandleFocus(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	s.SetActiveTab(5)
	s.SetFocusField(0)
	if s.GetFocusField() != 0 {
		t.Errorf("SetFocusField tab 5 with nil editedConfig: got %d", s.GetFocusField())
	}
}

func TestSettingsDefaultStatementTimeoutDisplay(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Query.DefaultStatementTimeout = func() *time.Duration { d := 30 * time.Second; return &d }()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(4)
	if s.FieldCount() != 1 {
		t.Fatalf("query tab FieldCount = %d, want 1", s.FieldCount())
	}

	s.SetFocusField(0)
	val := s.GetFocusedFieldValue()
	if val != "30s" {
		t.Errorf("DefaultStatementTimeout display = %q, want %q", val, "30s")
	}
}

func TestSettingsSetDefaultStatementTimeout(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(4)
	s.SetFocusField(0)
	s.SetFocusedFieldValue("5m")

	if s.editedConfig.Query.DefaultStatementTimeout == nil {
		t.Fatal("DefaultStatementTimeout is nil after set")
	}
	if *s.editedConfig.Query.DefaultStatementTimeout != 5*time.Minute {
		t.Errorf("DefaultStatementTimeout = %v, want 5m", *s.editedConfig.Query.DefaultStatementTimeout)
	}
}

func TestSettingsSetInvalidDuration(t *testing.T) {
	cfg := config.GetDefaultConfig()
	orig := 30 * time.Second
	cfg.Query.DefaultStatementTimeout = &orig
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(4)
	s.SetFocusField(0)
	s.SetFocusedFieldValue("not-a-duration")

	// Value should be unchanged after invalid input.
	if s.editedConfig.Query.DefaultStatementTimeout == nil || *s.editedConfig.Query.DefaultStatementTimeout != orig {
		t.Errorf("DefaultStatementTimeout changed after invalid input: %v", s.editedConfig.Query.DefaultStatementTimeout)
	}

	st := s.formStates[4]
	if st.errorText == "" {
		t.Error("should have error after invalid duration input")
	}
}

func TestSettingsTabSwitchPreservesError(t *testing.T) {
	cfg := config.GetDefaultConfig()
	s := newTestSettings(&captureDriver{}, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(0)
	s.SetError("err on gen")
	s.SetActiveTab(1)
	s.SetActiveTab(0)
	st := s.formStates[0]
	if st.errorText != "err on gen" {
		t.Errorf("errorText after tab switch = %q, want %q", st.errorText, "err on gen")
	}
}

func TestSettingsFieldHintDisplay(t *testing.T) {
	cfg := config.GetDefaultConfig()
	drv := &captureDriver{}
	s := newTestSettings(drv, cfg)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(4)
	s.SetFocusField(0)
	s.ClearError()

	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender: %v", err)
	}

	if !strings.Contains(drv.lastContent, "e.g. 30s") {
		t.Error("rendered body missing hint text for duration field")
	}
}

func newTestSettingsWithDefaults(drv types.GuiDriver, cfg *config.UserConfig, defaults []*types.ChordBinding) *SettingsContext {
	s := newTestSettings(drv, cfg)
	s.SetDefaults(func() []*types.ChordBinding { return defaults })
	return s
}

func TestSettingsKeysTabListsShippedDefaults(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = nil
	defaults := []*types.ChordBinding{
		{Sequence: []types.ChordKey{{Code: 'j'}}, Mode: types.ModeNormal, Scope: "TABLES", ActionID: "list.down", Description: "Move down"},
	}
	drv := &captureDriver{}
	s := newTestSettingsWithDefaults(drv, cfg, defaults)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	if s.FieldCount() != 1 {
		t.Fatalf("Keys tab FieldCount = %d, want 1 (the shipped default)", s.FieldCount())
	}
	if err := s.HandleRender(); err != nil {
		t.Fatalf("HandleRender tab 5: %v", err)
	}
	for _, want := range []string{"j", "TABLES", "list.down", "Move down"} {
		if !strings.Contains(drv.lastContent, want) {
			t.Errorf("Keys tab body missing %q; got:\n%s", want, drv.lastContent)
		}
	}
}

func TestSettingsKeysTabEditDefaultCreatesOverride(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = nil
	defaults := []*types.ChordBinding{
		{Sequence: []types.ChordKey{{Code: 'j'}}, Mode: types.ModeNormal, Scope: "TABLES", ActionID: "list.down", Description: "Move down"},
	}
	s := newTestSettingsWithDefaults(&captureDriver{}, cfg, defaults)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	s.SetFocusField(0)

	if row, ok := s.FocusedKeyRow(); !ok || row.IsOverride || row.Key != "j" {
		t.Fatalf("FocusedKeyRow = %+v ok=%v, want default row key=j", row, ok)
	}

	if !s.EditFocusedKeybinding("<c-j>") {
		t.Fatal("EditFocusedKeybinding returned false")
	}

	got := s.GetEditedConfig().Keybindings
	if len(got) != 1 {
		t.Fatalf("Keybindings len = %d, want 1 synthesized override", len(got))
	}
	want := config.KeybindingConfig{Mode: "n", Scope: "TABLES", Key: "<c-j>", Action: "list.down", Description: "Move down"}
	if got[0] != want {
		t.Errorf("override = %+v, want %+v", got[0], want)
	}

	// Row now reflects the override and stays a single merged row.
	if s.FieldCount() != 1 {
		t.Fatalf("FieldCount after edit = %d, want 1", s.FieldCount())
	}
	row, ok := s.FocusedKeyRow()
	if !ok || !row.IsOverride || row.Key != "<c-j>" {
		t.Errorf("row after edit = %+v ok=%v, want override key=<c-j>", row, ok)
	}

	// Editing again updates the existing override in place (no duplicate).
	if !s.EditFocusedKeybinding("J") {
		t.Fatal("second EditFocusedKeybinding returned false")
	}
	if got := s.GetEditedConfig().Keybindings; len(got) != 1 || got[0].Key != "J" {
		t.Errorf("after re-edit Keybindings = %+v, want single entry key=J", got)
	}
}

func TestSettingsKeysTabDeleteOverrideRevertsToDefault(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = []config.KeybindingConfig{
		{Mode: "n", Scope: "TABLES", Key: "<c-j>", Action: "list.down", Description: "Move down"},
	}
	defaults := []*types.ChordBinding{
		{Sequence: []types.ChordKey{{Code: 'j'}}, Mode: types.ModeNormal, Scope: "TABLES", ActionID: "list.down", Description: "Move down"},
	}
	s := newTestSettingsWithDefaults(&captureDriver{}, cfg, defaults)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	s.SetFocusField(0)

	// The override should be merged onto the single default row.
	if s.FieldCount() != 1 {
		t.Fatalf("FieldCount = %d, want 1 merged row", s.FieldCount())
	}
	if row, _ := s.FocusedKeyRow(); !row.IsOverride || row.Key != "<c-j>" {
		t.Fatalf("row = %+v, want override key=<c-j>", row)
	}

	ok, isDefault := s.DeleteFocusedKeybinding()
	if !ok || isDefault {
		t.Fatalf("DeleteFocusedKeybinding ok=%v isDefault=%v, want ok=true isDefault=false", ok, isDefault)
	}

	if len(s.GetEditedConfig().Keybindings) != 0 {
		t.Fatalf("Keybindings after delete = %d, want 0", len(s.GetEditedConfig().Keybindings))
	}
	// Row reverts to the shipped default (key j, no longer an override).
	row, ok := s.FocusedKeyRow()
	if !ok || row.IsOverride || row.Key != "j" {
		t.Errorf("row after delete = %+v ok=%v, want default key=j", row, ok)
	}
}

func TestSettingsKeysTabDeleteDefaultReportsIsDefault(t *testing.T) {
	cfg := config.GetDefaultConfig()
	cfg.Keybindings = nil
	defaults := []*types.ChordBinding{
		{Sequence: []types.ChordKey{{Code: 'j'}}, Mode: types.ModeNormal, Scope: "TABLES", ActionID: "list.down"},
	}
	s := newTestSettingsWithDefaults(&captureDriver{}, cfg, defaults)
	_ = s.HandleFocus(types.OnFocusOpts{})

	s.SetActiveTab(5)
	s.SetFocusField(0)

	ok, isDefault := s.DeleteFocusedKeybinding()
	if ok || !isDefault {
		t.Errorf("DeleteFocusedKeybinding ok=%v isDefault=%v, want ok=false isDefault=true", ok, isDefault)
	}
}
