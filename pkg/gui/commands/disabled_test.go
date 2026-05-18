package commands

import "testing"

// TestCommand_Disabled_GetDisabledReturnsDisabled confirms that a
// non-nil GetDisabled predicate's return value is used verbatim.
func TestCommand_Disabled_GetDisabledReturnsDisabled(t *testing.T) {
	cmd := Command{
		ID: "test.dynamic",
		GetDisabled: func(ExecCtx) (string, bool) {
			return "driver lacks live cancel", true
		},
	}

	reason, disabled := cmd.Disabled(ExecCtx{})
	if !disabled {
		t.Fatalf("Disabled() disabled = false, want true")
	}
	if reason != "driver lacks live cancel" {
		t.Errorf("Disabled() reason = %q, want %q", reason, "driver lacks live cancel")
	}
}

// TestCommand_Disabled_GetDisabledReturnsEnabled confirms an enabled
// dynamic predicate produces (empty, false). Empty reason is fine when
// disabled=false.
func TestCommand_Disabled_GetDisabledReturnsEnabled(t *testing.T) {
	cmd := Command{
		GetDisabled: func(ExecCtx) (string, bool) {
			return "", false
		},
		// Static reason MUST NOT win when GetDisabled is non-nil.
		DisabledReasonStatic: "static reason should be ignored",
	}

	reason, disabled := cmd.Disabled(ExecCtx{})
	if disabled {
		t.Fatalf("Disabled() disabled = true, want false (predicate said enabled)")
	}
	if reason != "" {
		t.Errorf("Disabled() reason = %q, want empty", reason)
	}
}

// TestCommand_Disabled_StaticReasonHonored confirms that when
// GetDisabled is nil, a non-empty DisabledReasonStatic disables the
// command.
func TestCommand_Disabled_StaticReasonHonored(t *testing.T) {
	cmd := Command{
		DisabledReasonStatic: "driver lacks feature",
	}

	reason, disabled := cmd.Disabled(ExecCtx{})
	if !disabled {
		t.Fatalf("Disabled() disabled = false, want true")
	}
	if reason != "driver lacks feature" {
		t.Errorf("Disabled() reason = %q, want %q", reason, "driver lacks feature")
	}
}

// TestCommand_Disabled_BothNilEnabled confirms the all-empty defaults
// case: no predicate, no static reason → enabled.
func TestCommand_Disabled_BothNilEnabled(t *testing.T) {
	cmd := Command{ID: "test.plain"}

	reason, disabled := cmd.Disabled(ExecCtx{})
	if disabled {
		t.Errorf("Disabled() disabled = true, want false (zero-value command must be enabled)")
	}
	if reason != "" {
		t.Errorf("Disabled() reason = %q, want empty", reason)
	}
}

// TestCommand_Disabled_PanicRecovers confirms that a panicking
// GetDisabled is treated as disabled with the canonical "<internal
// error>" reason — the Matcher must not crash when a predicate misuses
// state.
func TestCommand_Disabled_PanicRecovers(t *testing.T) {
	cmd := Command{
		GetDisabled: func(ExecCtx) (string, bool) {
			panic("intentional test panic")
		},
	}

	reason, disabled := cmd.Disabled(ExecCtx{})
	if !disabled {
		t.Fatalf("Disabled() disabled = false after panic, want true")
	}
	if reason != "<internal error>" {
		t.Errorf("Disabled() reason = %q, want %q", reason, "<internal error>")
	}
}
