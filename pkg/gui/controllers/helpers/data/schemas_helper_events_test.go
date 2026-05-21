package data_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/dbsavvy/pkg/common"
	"github.com/davesavic/dbsavvy/pkg/config"
	"github.com/davesavic/dbsavvy/pkg/gui/controllers/helpers/data"
	"github.com/davesavic/dbsavvy/pkg/i18n"
)

func bufLogger() (*logrus.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := logrus.New()
	l.SetOutput(buf)
	l.SetLevel(logrus.DebugLevel)
	l.SetFormatter(&logrus.JSONFormatter{})
	return l, buf
}

func lines(buf *bytes.Buffer, subs ...string) []string {
	var out []string
	for _, ln := range strings.Split(buf.String(), "\n") {
		ok := true
		for _, s := range subs {
			if !strings.Contains(ln, s) {
				ok = false
				break
			}
		}
		if ok && ln != "" {
			out = append(out, ln)
		}
	}
	return out
}

func buildHelper(t *testing.T) (*data.SchemasHelper, *common.AppStateStore, *bytes.Buffer) {
	t.Helper()
	log, buf := bufLogger()
	fs := afero.NewMemMapFs()
	tr := i18n.EnglishTranslationSet()
	cfg := config.GetDefaultConfig()
	c := common.NewCommon(log, tr, cfg, &common.AppState{}, fs)
	store := common.NewAppStateStore(fs, "/s.yml", common.DefaultClock())
	t.Cleanup(func() { _ = store.Close() })
	// Drain any cat=state lines the store itself emits during construction
	// (none today, but Close fires; we reset before each test action).
	return data.NewSchemasHelper(c, store), store, buf
}

func TestHide_EmitsEvent(t *testing.T) {
	h, _, buf := buildHelper(t)

	require.NoError(t, h.HideSchema("conn-1", "audit"))

	got := lines(buf, `"cat":"state"`, `"evt":"schema_hide"`)
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"conn_id":"conn-1"`)
	require.Contains(t, got[0], `"schema":"audit"`)
}

func TestUnhide_OnlyEmitsWhenRemoved(t *testing.T) {
	h, _, buf := buildHelper(t)

	// 1. ErrNeedsConfirmation path — schema matches a builtin pattern;
	// the helper must NOT emit schema_unhide.
	err := h.UnhideSchema("conn-1", "pg_catalog", []string{"pg_catalog"}, nil)
	require.ErrorIs(t, err, data.ErrNeedsConfirmation)
	require.Empty(t, lines(buf, `"evt":"schema_unhide"`),
		"ErrNeedsConfirmation path must not emit schema_unhide")

	// 2. Not-present no-op — schema isn't in the runtime layer; helper
	// must NOT emit either.
	require.NoError(t, h.UnhideSchema("conn-1", "ghost", nil, nil))
	require.Empty(t, lines(buf, `"evt":"schema_unhide"`),
		"not-present unhide must not emit schema_unhide")

	// 3. Actual removal — hide first, then unhide; the unhide must emit
	// exactly one schema_unhide.
	require.NoError(t, h.HideSchema("conn-1", "scratch"))
	// drain the schema_hide and (probably) appstate_mutate_scheduled lines.
	buf.Reset()

	// Wait until the MutateAndSave's goroutine boundary settles. The
	// store mutation is synchronous so no sleep is required, but a tiny
	// yield lets any pending logrus formatter flush.
	time.Sleep(5 * time.Millisecond)

	require.NoError(t, h.UnhideSchema("conn-1", "scratch", nil, nil))

	got := lines(buf, `"cat":"state"`, `"evt":"schema_unhide"`)
	require.Len(t, got, 1)
	require.Contains(t, got[0], `"conn_id":"conn-1"`)
	require.Contains(t, got[0], `"schema":"scratch"`)
}
