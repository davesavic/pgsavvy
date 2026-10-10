package pg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/99designs/keyring"
	"github.com/adrg/xdg"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/session"
)

func TestResolvedCredentialProvidersLive(t *testing.T) {
	dsn := os.Getenv("PGSAVVY_TEST_PG")
	if dsn == "" {
		t.Skip("requires isolated PGSAVVY_TEST_PG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	observer, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("isolated credential fixture connection failed")
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	const secret = "LIVE_RESOLVED_PROVIDER_SECRET"
	username := fmt.Sprintf("review_credential_login_%d", time.Now().UnixNano())
	role := pgx.Identifier{username}.Sanitize()
	_, err = observer.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+secret+"'")
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := observer.Exec(cleanup, "DROP OWNED BY "+role+" RESTRICT")
		require.NoError(t, err)
		_, err = observer.Exec(cleanup, "DROP ROLE "+role)
		require.NoError(t, err)
	})
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.User = url.User(username)
	for _, source := range []string{"helper", "pgpass", "keyring"} {
		t.Run(source, func(t *testing.T) {
			profile := models.Connection{Name: "provider-" + source, Driver: "postgres", DSN: parsed.String()}
			switch source {
			case "helper":
				profile.PasswordCommand = "printf '" + secret + "'"
			case "pgpass":
				profile.PgpassPath = filepath.Join(t.TempDir(), "pgpass")
				require.NoError(t, os.WriteFile(profile.PgpassPath, []byte("*:*:*:"+username+":"+secret+"\n"), 0o600))
			case "keyring":
				dir := t.TempDir()
				t.Cleanup(xdg.Reload)
				t.Setenv("XDG_DATA_HOME", dir)
				xdg.Reload()
				t.Setenv("PGSAVVY_KEYRING_PASSPHRASE", "isolated-test-passphrase")
				keyringDir := filepath.Join(dir, "pgsavvy", "keyring")
				require.NoError(t, os.MkdirAll(keyringDir, 0o700))
				store, err := keyring.Open(keyring.Config{AllowedBackends: []keyring.BackendType{keyring.FileBackend}, ServiceName: "pgsavvy", FileDir: keyringDir, FilePasswordFunc: keyring.FixedStringPrompt("isolated-test-passphrase")})
				require.NoError(t, err)
				profile.KeyringRef = "live-provider"
				require.NoError(t, store.Set(keyring.Item{Key: profile.KeyringRef, Data: []byte(secret)}))
			}
			buf := installCaptureLogger(t)
			driver, err := New(stubPrompter{})(ctx)
			require.NoError(t, err)
			conn, err := driver.Open(ctx, profile, nil)
			if err != nil {
				t.Fatal("credential provider did not authenticate with isolated login")
			}
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			inner, err := conn.AcquireSession(ctx)
			require.NoError(t, err)
			sqlSession := session.New(conn, inner, session.Options{Logger: globalLogger.Load()})
			t.Cleanup(func() { require.NoError(t, sqlSession.Close()) })
			require.Empty(t, profile.Password)
			require.NotContains(t, profile.DSN, secret)
			q := models.Query{SQL: "SELECT '" + secret + "'::integer"}
			_, err = sqlSession.Execute(ctx, q)
			var backend *pgconn.PgError
			require.ErrorAs(t, err, &backend)
			require.Contains(t, backend.Message, secret)
			_, err = sqlSession.Explain(ctx, models.Query{SQL: q.SQL}, false)
			require.Error(t, err)
			_, err = sqlSession.Execute(ctx, models.Query{SQL: "DO $$ BEGIN RAISE NOTICE '" + secret + "'; END $$"})
			require.NoError(t, err)
			if strings.Contains(buf.String(), secret) {
				t.Fatal("resolved provider credential reached captured logs; log omitted")
			}
			require.Contains(t, buf.String(), "***")
			require.Contains(t, q.SQL, secret, "executable input must remain unchanged")
		})
	}
}
