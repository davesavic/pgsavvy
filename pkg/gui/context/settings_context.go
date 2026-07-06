package context

import (
	"fmt"
	"strings"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/theme"
	"github.com/mattn/go-runewidth"
)

const settingsTabCount = 6

var settingsTabLabels = [settingsTabCount]string{"Gen", "Theme", "UI", "Editor", "Query", "Keys"}

// SettingsContextDeps provides settings-specific dependencies on top of the
// standard context tree dependency bag. Cfg returns a live pointer to the
// application config. Defaults returns the shipped-default bindings (the
// union of every controller's GetKeybindings) so the Keys tab can list ALL
// active bindings, not just the user's on-disk overrides.
type SettingsContextDeps struct {
	types.ContextTreeDeps
	Cfg      func() *config.UserConfig
	Defaults func() []*types.ChordBinding
}

// KeybindingRow is one displayable Keys-tab entry: an effective binding
// (a shipped default, optionally overlaid by a user override that wins).
type KeybindingRow struct {
	Mode        string
	Scope       string
	Key         string
	Action      string
	Description string
	IsOverride  bool
}

// keyRow is the internal Keys-tab row, carrying the override index (or -1
// for an un-overridden shipped default) so edit/delete can map back to the
// cfg.Keybindings slice.
type keyRow struct {
	mode        string
	scope       string
	key         string
	action      string
	description string
	isOverride  bool
	overrideIdx int
}

// SettingsContext is the MAIN_CONTEXT modal that embeds a TabbedRailContext to
// provide 6 tabs of editable settings form fields. Each tab is rendered inline
// — no leaf context objects. On HandleFocus, a deep copy of the live config is
// taken so edits are isolated until saved.
type SettingsContext struct {
	*TabbedRailContext

	deps         SettingsContextDeps
	editedConfig *config.UserConfig
	formStates   [settingsTabCount]*settingsFormState
	keyRows      []keyRow
	viewName     string
	msgWidth     int
}

var _ types.IBaseContext = (*SettingsContext)(nil)

// NewSettingsContext constructs a SettingsContext bound to the SETTINGS key.
// The 6 tabs are declared but no fields are built until HandleFocus, when the
// live config is deep-copied.
func NewSettingsContext(base BaseContext, deps SettingsContextDeps) *SettingsContext {
	core := NewTabbedRailContext(base, deps.ContextTreeDeps, TabbedRailOpts{
		FireFocusHooks: false,
	},
		TabSpec{Label: settingsTabLabels[0], LeafKey: types.SETTINGS},
		TabSpec{Label: settingsTabLabels[1], LeafKey: types.SETTINGS},
		TabSpec{Label: settingsTabLabels[2], LeafKey: types.SETTINGS},
		TabSpec{Label: settingsTabLabels[3], LeafKey: types.SETTINGS},
		TabSpec{Label: settingsTabLabels[4], LeafKey: types.SETTINGS},
		TabSpec{Label: settingsTabLabels[5], LeafKey: types.SETTINGS},
	)
	return &SettingsContext{
		TabbedRailContext: core,
		deps:              deps,
		viewName:          string(types.SETTINGS),
	}
}

// SetCfg injects the live config accessor. Called by the orchestrator after
// construction.
func (s *SettingsContext) SetCfg(fn func() *config.UserConfig) { s.deps.Cfg = fn }

// SetDefaults injects the shipped-default binding accessor. Called by the
// orchestrator after construction. When unset the Keys tab lists only the
// user's on-disk overrides.
func (s *SettingsContext) SetDefaults(fn func() []*types.ChordBinding) { s.deps.Defaults = fn }

// HandleFocus deep-copies the live config and rebuilds all form states.
// Only clones when called for an initial open (editedConfig is nil);
// on focus-return (e.g. popup popped off above us) the existing clone
// is preserved so in-flight prompt edits survive.
func (s *SettingsContext) HandleFocus(_ types.OnFocusOpts) error {
	if s.deps.Cfg == nil {
		return nil
	}
	if s.editedConfig == nil {
		s.editedConfig = s.deps.Cfg().Clone()
		s.buildFormStates()
	}
	return s.TabbedRailContext.HandleFocus(types.OnFocusOpts{})
}

// HandleFocusLost drops the edited clone so the next HandleFocus (modal
// re-open) picks up the live config fresh.
func (s *SettingsContext) HandleFocusLost(_ types.OnFocusLostOpts) error {
	s.editedConfig = nil
	s.formStates = [settingsTabCount]*settingsFormState{}
	return s.TabbedRailContext.HandleFocusLost(types.OnFocusLostOpts{})
}

func (s *SettingsContext) buildFormStates() {
	byTab := buildSettingsFields()
	for i := range settingsTabCount {
		s.formStates[i] = &settingsFormState{
			fields:     byTab[i],
			focusedIdx: 0,
			scrollOff:  0,
			errorText:  "",
		}
	}
	s.rebuildKeyRows()
}

// rebuildKeyRows recomputes the Keys-tab rows from the shipped defaults
// (deps.Defaults) overlaid by the edited config's user overrides. Shipped
// defaults are listed first (in declaration order); a user override that
// targets the same (mode, scope, action) replaces the displayed key and
// links back via overrideIdx. User overrides that match no shipped default
// (brand-new bindings) are appended afterwards.
func (s *SettingsContext) rebuildKeyRows() {
	s.keyRows = nil
	if s.editedConfig == nil {
		return
	}
	overrides := s.editedConfig.Keybindings
	used := make([]bool, len(overrides))

	scopeOf := func(sc string) string {
		if sc == "" {
			return "global"
		}
		return sc
	}
	matchOverride := func(mode, scope, action string) int {
		for i := range overrides {
			if used[i] {
				continue
			}
			o := overrides[i]
			if o.Mode == mode && scopeOf(o.Scope) == scope && o.Action == action {
				return i
			}
		}
		return -1
	}

	if s.deps.Defaults != nil {
		for _, cb := range s.deps.Defaults() {
			if cb == nil {
				continue
			}
			mode := modeMaskTokens(cb.Mode)
			scope := scopeOf(string(cb.Scope))
			row := keyRow{
				mode:        mode,
				scope:       scope,
				key:         types.SequenceString(cb.Sequence),
				action:      cb.ActionID,
				description: cb.Description,
				overrideIdx: -1,
			}
			if idx := matchOverride(mode, scope, cb.ActionID); idx >= 0 {
				used[idx] = true
				o := overrides[idx]
				row.key = o.Key
				row.isOverride = true
				row.overrideIdx = idx
				if o.Description != "" {
					row.description = o.Description
				}
			}
			s.keyRows = append(s.keyRows, row)
		}
	}

	for i := range overrides {
		if used[i] {
			continue
		}
		o := overrides[i]
		action := o.Action
		if action == "" && o.Command != "" {
			action = "command:" + o.Command
		}
		s.keyRows = append(s.keyRows, keyRow{
			mode:        o.Mode,
			scope:       scopeOf(o.Scope),
			key:         o.Key,
			action:      action,
			description: o.Description,
			isOverride:  true,
			overrideIdx: i,
		})
	}
}

// modeMaskTokens renders a Mode bitmask as the comma-separated config mode
// tokens (mirroring modeByToken in pkg/gui/keys). ModeNormal (the zero
// sentinel) renders as "n".
func modeMaskTokens(m types.Mode) string {
	if m == types.ModeNormal {
		return "n"
	}
	pairs := []struct {
		bit types.Mode
		tok string
	}{
		{types.ModeInsert, "i"},
		{types.ModeVisual, "v"},
		{types.ModeVisualLine, "V"},
		{types.ModeVisualBlock, "<c-v>"},
		{types.ModeOperatorPending, "o"},
		{types.ModeCommand, "c"},
	}
	var toks []string
	for _, p := range pairs {
		if m&p.bit != 0 {
			toks = append(toks, p.tok)
		}
	}
	if len(toks) == 0 {
		return "n"
	}
	return strings.Join(toks, ",")
}

// HandleRender renders the tab strip and the active tab's form body.
func (s *SettingsContext) HandleRender() error {
	body := s.body()
	writeView(s.deps.ContextTreeDeps, func() error {
		return s.deps.GuiDriver.SetContent(s.GetViewName(), body)
	})
	return nil
}

func (s *SettingsContext) body() string {
	viewName := s.GetViewName()
	active := s.ActiveTab()

	labels := make([]string, settingsTabCount)
	copy(labels, settingsTabLabels[:])
	labels[active] = "[" + labels[active] + "]"

	if s.deps.GuiDriver != nil {
		_ = s.deps.GuiDriver.SetViewTabs(viewName, labels, active)
	}

	st := s.formStates[active]
	if st == nil {
		return ""
	}
	if active == 5 {
		return s.renderKeysTab(st)
	}
	body := s.renderFormTab(st)
	if active == 1 {
		body = "  fg: color  |  fg+bg: fg on bg  |  bg: on color\n" + body
	}
	return body
}

func (s *SettingsContext) renderFormTab(st *settingsFormState) string {
	var b strings.Builder
	for i, f := range st.fields {
		marker := "  "
		if i == st.focusedIdx {
			marker = "> "
		}
		val := s.displayFieldValue(f)
		if s.msgWidth > 0 {
			labelWidth := 25
			valueMax := s.msgWidth - 2 - labelWidth
			if valueMax > 0 && runewidth.StringWidth(val) > valueMax {
				val = truncateToWidth(val, valueMax)
			}
		}
		if f.hint != "" {
			fmt.Fprintf(&b, "%s%-24s %-18s %s\n", marker, f.label+":", val, f.hint)
		} else {
			fmt.Fprintf(&b, "%s%-24s %s\n", marker, f.label+":", val)
		}
		if i == st.focusedIdx && st.errorText != "" {
			fmt.Fprintf(&b, "    %s\n", clipInline(st.errorText, s.msgWidth-inlineMsgReserve))
		}
	}
	return b.String()
}

func (s *SettingsContext) displayFieldValue(f settingsField) string {
	if s.editedConfig == nil {
		return ""
	}
	val := f.getter(s.editedConfig)
	switch f.kind {
	case settingsFieldToggle:
		if b, _ := parseBool(val); b {
			return "[x]"
		}
		return "[ ]"
	default:
		if val == "" {
			return "(empty)"
		}
		if f.hint == "read-only" {
			return val
		}
		if f.isColor {
			if sgr := theme.PreviewSGR(val); sgr != "" {
				return sgr + val + theme.AnsiReset
			}
		}
		return val
	}
}

func (s *SettingsContext) renderKeysTab(st *settingsFormState) string {
	if len(s.keyRows) == 0 {
		return "  (no custom keybindings)"
	}
	var b strings.Builder
	start := st.scrollOff
	if start < 0 {
		start = 0
	}
	if start > len(s.keyRows)-1 {
		start = max(0, len(s.keyRows)-1)
	}
	end := min(start+20, len(s.keyRows))

	for i := start; i < end; i++ {
		marker := "  "
		if i == st.focusedIdx {
			marker = "> "
		}
		r := s.keyRows[i]
		line := fmt.Sprintf("%-6s %-12s %-24s %s", r.mode, r.scope, r.key, r.action)
		if r.description != "" {
			line += "  # " + r.description
		}
		if r.isOverride {
			line += " (custom)"
		}
		rw := runewidth.StringWidth(line)
		if s.msgWidth > 0 && rw > s.msgWidth {
			line = truncateToWidth(line, s.msgWidth)
		}
		fmt.Fprintf(&b, "%s%s\n", marker, line)
	}
	return b.String()
}

// GetFocusField returns the focused field index within the active tab.
func (s *SettingsContext) GetFocusField() int {
	st := s.formStates[s.ActiveTab()]
	if st == nil {
		return 0
	}
	return st.focusedIdx
}

// SetFocusField sets the focused field index within the active tab.
func (s *SettingsContext) SetFocusField(idx int) {
	st := s.formStates[s.ActiveTab()]
	if st == nil {
		return
	}
	active := s.ActiveTab()
	if active == 5 {
		n := len(s.keyRows)
		if n == 0 {
			st.focusedIdx = 0
			return
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		st.focusedIdx = idx
		if idx < st.scrollOff {
			st.scrollOff = idx
		}
		if idx >= st.scrollOff+20 {
			st.scrollOff = idx - 20 + 1
		}
		return
	}
	n := len(st.fields)
	if n == 0 {
		st.focusedIdx = 0
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	st.focusedIdx = idx
}

// FieldCount returns the number of focusable items in the active tab.
func (s *SettingsContext) FieldCount() int {
	st := s.formStates[s.ActiveTab()]
	if st == nil {
		return 0
	}
	if s.ActiveTab() == 5 {
		return len(s.keyRows)
	}
	return len(st.fields)
}

// GetEditedConfig returns the cloned, edited config.
func (s *SettingsContext) GetEditedConfig() *config.UserConfig {
	return s.editedConfig
}

// SetEditedConfig replaces the edited config (e.g. on save/restore).
func (s *SettingsContext) SetEditedConfig(cfg *config.UserConfig) {
	s.editedConfig = cfg
}

// RebuildFormStates rebuilds all per-tab form states from the current
// editedConfig without re-cloning. Use after mutating editedConfig in-place
// (e.g. adding/removing keybindings) to sync the form UI.
func (s *SettingsContext) RebuildFormStates() {
	s.buildFormStates()
}

// FocusedKeyRow returns the focused Keys-tab row and ok=true, or ok=false
// when not on the Keys tab or no row is focused.
func (s *SettingsContext) FocusedKeyRow() (KeybindingRow, bool) {
	if s.ActiveTab() != 5 {
		return KeybindingRow{}, false
	}
	st := s.formStates[5]
	if st == nil || st.focusedIdx < 0 || st.focusedIdx >= len(s.keyRows) {
		return KeybindingRow{}, false
	}
	r := s.keyRows[st.focusedIdx]
	return KeybindingRow{
		Mode:        r.mode,
		Scope:       r.scope,
		Key:         r.key,
		Action:      r.action,
		Description: r.description,
		IsOverride:  r.isOverride,
	}, true
}

// EditFocusedKeybinding rebinds the focused Keys-tab row to newKey. If the
// row is already a user override its key is updated in place; otherwise a new
// override entry (Mode/Scope/Action/Description copied from the shipped
// default) is appended. Rows are rebuilt so the change is visible. Returns
// false when not on the Keys tab or no row is focused.
func (s *SettingsContext) EditFocusedKeybinding(newKey string) bool {
	if s.editedConfig == nil || s.ActiveTab() != 5 {
		return false
	}
	st := s.formStates[5]
	if st == nil || st.focusedIdx < 0 || st.focusedIdx >= len(s.keyRows) {
		return false
	}
	r := s.keyRows[st.focusedIdx]
	if r.overrideIdx >= 0 && r.overrideIdx < len(s.editedConfig.Keybindings) {
		s.editedConfig.Keybindings[r.overrideIdx].Key = newKey
	} else {
		s.editedConfig.Keybindings = append(s.editedConfig.Keybindings, config.KeybindingConfig{
			Mode:        r.mode,
			Scope:       r.scope,
			Key:         newKey,
			Action:      r.action,
			Description: r.description,
		})
	}
	s.rebuildKeyRows()
	return true
}

// DeleteFocusedKeybinding removes the user override backing the focused
// Keys-tab row, reverting it to its shipped default. Returns ok=false when
// not on the Keys tab / no row focused; isDefault=true when the row is a
// shipped default with no override (nothing to delete).
func (s *SettingsContext) DeleteFocusedKeybinding() (ok bool, isDefault bool) {
	if s.editedConfig == nil || s.ActiveTab() != 5 {
		return false, false
	}
	st := s.formStates[5]
	if st == nil || st.focusedIdx < 0 || st.focusedIdx >= len(s.keyRows) {
		return false, false
	}
	r := s.keyRows[st.focusedIdx]
	if r.overrideIdx < 0 || r.overrideIdx >= len(s.editedConfig.Keybindings) {
		return false, true
	}
	kb := s.editedConfig.Keybindings
	s.editedConfig.Keybindings = append(kb[:r.overrideIdx], kb[r.overrideIdx+1:]...)
	s.rebuildKeyRows()
	if st.focusedIdx >= len(s.keyRows) {
		st.focusedIdx = max(0, len(s.keyRows)-1)
	}
	return true, false
}

// SetError stamps an inline error on the active tab's focused field.
func (s *SettingsContext) SetError(err string) {
	st := s.formStates[s.ActiveTab()]
	if st == nil {
		return
	}
	st.errorText = err
}

// ClearError clears the inline error on the active tab.
func (s *SettingsContext) ClearError() {
	st := s.formStates[s.ActiveTab()]
	if st == nil {
		return
	}
	st.errorText = ""
}

// GetFormError returns the error text for the given tab, or "".
func (s *SettingsContext) GetFormError(tab int) string {
	if tab < 0 || tab >= settingsTabCount || s.formStates[tab] == nil {
		return ""
	}
	return s.formStates[tab].errorText
}

// GetViewName returns the view name for the layout engine.
func (s *SettingsContext) GetViewName() string {
	if s.viewName != "" {
		return s.viewName
	}
	return s.BaseContext.GetViewName()
}

// TabCount returns the number of tabs.
func (s *SettingsContext) TabCount() int { return s.TabbedRailContext.TabCount() }

// GetKind returns MAIN_CONTEXT.
func (s *SettingsContext) GetKind() types.ContextKind { return types.MAIN_CONTEXT }

// SetLabelWrapWidth records the inner column count for truncation.
func (s *SettingsContext) SetLabelWrapWidth(w int) { s.msgWidth = w }

// GetFocusedField returns the focused settingsField in the active tab, or nil.
func (s *SettingsContext) GetFocusedField() *settingsField {
	active := s.ActiveTab()
	st := s.formStates[active]
	if st == nil || active == 5 || st.focusedIdx >= len(st.fields) || st.focusedIdx < 0 {
		return nil
	}
	return &st.fields[st.focusedIdx]
}

// GetFocusedFieldValue returns the current string value of the focused field.
func (s *SettingsContext) GetFocusedFieldValue() string {
	if s.editedConfig == nil {
		return ""
	}
	f := s.GetFocusedField()
	if f == nil {
		return ""
	}
	return f.getter(s.editedConfig)
}

// SetFocusedFieldValue stores a value into the focused field via its setter.
func (s *SettingsContext) SetFocusedFieldValue(v string) {
	if s.editedConfig == nil {
		return
	}
	f := s.GetFocusedField()
	if f == nil || f.setter == nil {
		return
	}
	if err := f.setter(s.editedConfig, v); err != nil {
		st := s.formStates[s.ActiveTab()]
		if st != nil {
			st.errorText = err.Error()
		}
		return
	}
	s.ClearError()
}

// ToggleFocused flips the focused toggle field.
func (s *SettingsContext) ToggleFocused() {
	if s.editedConfig == nil {
		return
	}
	f := s.GetFocusedField()
	if f == nil || f.kind != settingsFieldToggle || f.setter == nil {
		return
	}
	current := f.getter(s.editedConfig)
	switch current {
	case "true":
		_ = f.setter(s.editedConfig, "false")
	default:
		_ = f.setter(s.editedConfig, "true")
	}
	s.ClearError()
}

// NeedsRerenderOnWidthChange reports true so resizes re-render.
func (s *SettingsContext) NeedsRerenderOnWidthChange() bool { return true }

func truncateToWidth(s string, w int) string {
	if w <= 0 || runewidth.StringWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if used+rw > w {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}
