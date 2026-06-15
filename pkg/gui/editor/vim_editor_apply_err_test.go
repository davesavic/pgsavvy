package editor_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/jesseduffield/lazygit/pkg/gocui"

	"github.com/davesavic/pgsavvy/pkg/gui/editor"
	"github.com/davesavic/pgsavvy/pkg/gui/keys"
	"github.com/davesavic/pgsavvy/pkg/gui/types"
	"github.com/davesavic/pgsavvy/pkg/logs"
)

// hasEvent reports whether rec captured a record carrying the canonical
// {cat, evt} pair emitted by logs.Event.
func hasEvent(rec *logs.RecordingHandler, cat, evt string) bool {
	for _, r := range rec.Records() {
		var gotCat, gotEvt string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "cat":
				gotCat = a.Value.String()
			case "evt":
				gotEvt = a.Value.String()
			}
			return true
		})
		if gotCat == cat && gotEvt == evt {
			return true
		}
	}
	return false
}

func newInsertRigWithLog(t *testing.T, logger *slog.Logger) (*editor.Buffer, *editor.VimEditor) {
	t.Helper()
	store := keys.NewModeStore()
	store.Set(types.QUERY_EDITOR, types.ModeInsert)
	m, err := keys.NewMatcher(keys.NewTrieSet(), keys.MatcherConfig{
		Modes:       store,
		TimeoutLen:  50 * time.Millisecond,
		TtimeoutLen: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMatcher: %v", err)
	}
	buf := &editor.Buffer{}
	ve := editor.NewVimEditor(&bufProvider{buf: buf}, m, types.QUERY_EDITOR, editor.WithSessionLog(logger))
	return buf, ve
}

// TestVimEditor_InsertApplyError_IsLogged proves that an insert-mode
// Buffer.Apply failure is no longer swallowed silently: it emits a
// structured log record. SetCursor does not clamp, so seeding an
// out-of-range cursor makes the next insert Apply fail with
// ErrEditOutOfRange — the exact silent failure mode that hid the
// "stuck in insert mode, can't type" bug.
func TestVimEditor_InsertApplyError_IsLogged(t *testing.T) {
	rec := logs.NewRecordingHandler()
	buf, ve := newInsertRigWithLog(t, slog.New(rec))

	buf.SetCursor(editor.Position{Line: 99, Col: 0})

	v := newViewForVimTest()
	ve.Edit(v, gocui.NewKeyRune('a'))

	if !hasEvent(rec, "input", "insert_apply_err") {
		t.Fatalf("expected an insert_apply_err log record after a failed Apply; got %d records", len(rec.Records()))
	}
}
