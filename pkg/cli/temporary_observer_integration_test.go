//go:build integration && (linux || darwin)

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestMain also authorizes native bootstrap connections through these helpers;
// keep them available to general integration without selecting native-only SQL.
type temporaryLiveOwnership struct {
	Token    string `json:"token"`
	DSN      string `json:"observer_dsn"`
	SystemID string `json:"system_identifier"`
	DataDir  string `json:"data_directory"`
}

func temporaryLiveObserverConfig(root, token, dsn string) (*pgx.ConnConfig, temporaryLiveOwnership, error) {
	var owner temporaryLiveOwnership
	denied := errors.New("owned native observer authorization failed")
	if !filepath.IsAbs(root) || !strings.HasPrefix(filepath.Base(root), "pgsavvy-native.") || token == "" {
		return nil, owner, denied
	}
	path := filepath.Join(root, "temporary-owner")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 8192 {
		return nil, owner, denied
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Token != token || owner.DSN != dsn {
		return nil, owner, denied
	}
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	identifier, idErr := strconv.ParseUint(owner.SystemID, 10, 64)
	if err != nil || idErr != nil || identifier == 0 || owner.DataDir != directory {
		return nil, owner, denied
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Port == 0 || cfg.Database != "pgsavvy_test" || cfg.User != "pgsavvy" || cfg.Password == "" {
		return nil, owner, denied
	}
	cfg.TLSConfig, cfg.Fallbacks = nil, nil
	cfg.RuntimeParams = map[string]string{}
	cfg.ConnectTimeout = 5 * time.Second
	endpoint := net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != endpoint {
			return nil, denied
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return cfg, owner, nil
}

func temporaryLiveVerifyObserver(ctx context.Context, observer *pgx.Conn, owner temporaryLiveOwnership) error {
	var identifier, directory, database, username string
	err := observer.QueryRow(ctx, "SELECT system_identifier::text,current_setting('data_directory'),current_database(),current_user FROM pg_control_system()").Scan(&identifier, &directory, &database, &username)
	if err != nil || identifier != owner.SystemID || directory != owner.DataDir || database != "pgsavvy_test" || username != "pgsavvy" {
		return errors.New("connected observer identity is not owned")
	}
	return nil
}

func TestTemporaryCLIObserverOwnership(t *testing.T) {
	root, err := os.MkdirTemp(t.TempDir(), "pgsavvy-native.")
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "data"), 0o700))
	directory, err := filepath.EvalSymlinks(filepath.Join(root, "data"))
	require.NoError(t, err)
	const dsn = "postgres://pgsavvy:private-fixture@127.0.0.1:65432/pgsavvy_test?sslmode=disable"
	owner := temporaryLiveOwnership{Token: "private-token", DSN: dsn, SystemID: "1234567890123456789", DataDir: directory}
	data, err := json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	for _, absent := range []struct{ root, token string }{{"", ""}, {root, ""}, {"", owner.Token}} {
		cfg, _, err := temporaryLiveObserverConfig(absent.root, absent.token, dsn)
		require.Error(t, err)
		require.Nil(t, cfg, "missing ownership produced a connectable artifact")
	}
	for _, wrong := range []string{
		strings.Replace(dsn, "127.0.0.1", "192.0.2.1", 1), strings.Replace(dsn, "65432", "65433", 1),
		strings.Replace(dsn, "pgsavvy_test", "unrelated", 1), strings.Replace(dsn, "private-fixture", "different", 1), dsn + "&options=-cstatement_timeout=0",
	} {
		cfg, _, err := temporaryLiveObserverConfig(root, owner.Token, wrong)
		require.Error(t, err)
		require.Nil(t, cfg, "mismatched DSN produced a connectable artifact")
	}
	cfg, _, err := temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.NoError(t, err)
	// The only executable network hook also rejects alternate addresses before dialing.
	conn, err := cfg.DialFunc(context.Background(), "tcp", "127.0.0.1:65433")
	require.Error(t, err)
	require.Nil(t, conn)
	_, _, err = temporaryLiveObserverConfig(root, "wrong-token", dsn)
	require.Error(t, err)
	owner.SystemID = ""
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "missing cluster identity was accepted")
	owner.SystemID, owner.DataDir = "1234567890123456789", filepath.Join(directory, "unrelated")
	data, err = json.Marshal(owner)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "temporary-owner"), data, 0o600))
	_, _, err = temporaryLiveObserverConfig(root, owner.Token, dsn)
	require.Error(t, err, "unowned data directory was accepted")
}
