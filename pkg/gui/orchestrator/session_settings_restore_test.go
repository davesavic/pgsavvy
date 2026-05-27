package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/davesavic/dbsavvy/pkg/session"
)

func TestReplaySessionSettings(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)

	t.Run("empty saved map returns empty hint", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		got := replaySessionSettings(ctx, map[string]string{}, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("nil saved map returns empty hint", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		got := replaySessionSettings(ctx, nil, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("role is filtered out by allowlist", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"role": "admin"}
		got := replaySessionSettings(ctx, saved, noopExec, snap, log, "conn1")
		assert.Equal(t, "", got)
		assert.Empty(t, snap.All())
	})

	t.Run("search_path schemas are identifier-quoted", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"search_path": "app, public"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, `restored: search_path=app, public`, got)
		assert.Equal(t, []string{`SET search_path TO "app", "public"`}, executed)
		all := snap.All()
		assert.Equal(t, "app, public", all["search_path"])
	})

	t.Run("search_path injection payload is safely quoted", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"search_path": `app"; DROP TABLE users--`}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.NotEmpty(t, got)
		assert.Equal(t, []string{`SET search_path TO "app""; DROP TABLE users--"`}, executed)
	})

	t.Run("statement_timeout is canonicalized", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"statement_timeout": "30s"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: statement_timeout=30s", got)
		assert.Equal(t, []string{"SET statement_timeout = '30s'"}, executed)
		assert.Equal(t, "30s", snap.All()["statement_timeout"])
	})

	t.Run("invalid statement_timeout is skipped", func(t *testing.T) {
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"statement_timeout": "DROP TABLE users"}
		got := replaySessionSettings(ctx, saved, noopExec, snap, log, "conn1")

		assert.Equal(t, "", got)
		assert.Empty(t, snap.All())
	})

	t.Run("timezone value is single-quote escaped", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"timezone": "America/New_York"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: timezone=America/New_York", got)
		assert.Equal(t, []string{"SET timezone TO 'America/New_York'"}, executed)
	})

	t.Run("application_name with single quote is escaped", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"application_name": "my'app"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, "restored: application_name=my'app", got)
		assert.Equal(t, []string{"SET application_name TO 'my''app'"}, executed)
	})

	t.Run("failed SET is skipped, others continue", func(t *testing.T) {
		exec := func(_ context.Context, sql string) error {
			if sql == `SET search_path TO "gone_schema"` {
				return errors.New("schema does not exist")
			}
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{
			"application_name": "dbsavvy",
			"search_path":      "gone_schema",
		}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Contains(t, got, "application_name=dbsavvy")
		assert.NotContains(t, got, "search_path")
		all := snap.All()
		assert.Equal(t, "", all["search_path"])
		assert.Equal(t, "dbsavvy", all["application_name"])
	})

	t.Run("all settings fail returns empty hint", func(t *testing.T) {
		exec := func(_ context.Context, _ string) error {
			return errors.New("fail")
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{"timezone": "UTC"}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")
		assert.Equal(t, "", got)
	})

	t.Run("multiple settings restored in sorted order", func(t *testing.T) {
		var executed []string
		exec := func(_ context.Context, sql string) error {
			executed = append(executed, sql)
			return nil
		}
		snap := session.NewSettingsSnapshot()
		saved := map[string]string{
			"timezone":          "UTC",
			"search_path":       "myschema",
			"statement_timeout": "10s",
		}
		got := replaySessionSettings(ctx, saved, exec, snap, log, "conn1")

		assert.Equal(t, `restored: search_path=myschema, statement_timeout=10s, timezone=UTC`, got)
		assert.Len(t, executed, 3)
	})
}

func noopExec(_ context.Context, _ string) error { return nil }
