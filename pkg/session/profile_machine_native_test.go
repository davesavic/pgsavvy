//go:build linux || darwin || windows

package session

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/jackc/pgx/v5"

	appenv "github.com/davesavic/pgsavvy/pkg/env"
	"github.com/davesavic/pgsavvy/pkg/models"
)

func machineProfileFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(name), "PG") {
			t.Setenv(name, "")
		}
	}
	t.Setenv("HOME", dir)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, filepath.Join(dir, name))
	}
	t.Setenv("PGSERVICEFILE", filepath.Join(dir, "missing-service"))
	t.Setenv("PGPASSFILE", filepath.Join(dir, "missing-pass"))
	// pgx discovers native certificate defaults independently of HOME. Supply
	// owned TLS fixtures so parsing never reads the user's certificate contents.
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic.invalid"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, public, private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "owned-cert.pem"), filepath.Join(dir, "owned-key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSSLROOTCERT", certPath)
	t.Setenv("PGSSLCERT", certPath)
	t.Setenv("PGSSLKEY", keyPath)
	return dir
}

func TestMachineConfig_NativeDefaults(t *testing.T) {
	// Reload only while this test owns the environment; restore it after Setenv
	// cleanups. These path checks never open the returned native directories.
	t.Cleanup(xdg.Reload)
	dir := machineProfileFixture(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	xdg.Reload()
	want := filepath.Join(xdg.ConfigHome, "pgsavvy")
	switch runtime.GOOS {
	case "linux":
		want = filepath.Join(dir, ".config", "pgsavvy")
	case "darwin":
		want = filepath.Join(dir, "Library", "Application Support", "pgsavvy")
	case "windows":
		// xdg obtains LocalAppData from Windows known folders before considering
		// environment fallbacks. Do not assume USERPROFILE can relocate it.
		if !filepath.IsAbs(xdg.ConfigHome) || strings.EqualFold(want, filepath.Join(dir, ".config", "pgsavvy")) {
			t.Fatal("native Windows library default became a Linux-style path")
		}
	}
	if got := appenv.GetConfigDir(); got != want {
		t.Fatal("native configuration location changed")
	}
	override := filepath.Join(dir, "explicit λ config")
	t.Setenv("XDG_CONFIG_HOME", override)
	xdg.Reload()
	if appenv.GetConfigDir() != filepath.Join(override, "pgsavvy") {
		t.Fatal("absolute XDG configuration override was ignored")
	}
}

func TestMachineConfig_IsolatedEnvironment(t *testing.T) {
	dir := machineProfileFixture(t)
	t.Setenv("PGHOST", "environment-host")
	t.Setenv("PGPORT", "6432")
	t.Setenv("PGUSER", "environment-user")
	t.Setenv("PGDATABASE", "environment-db")
	service := filepath.Join(dir, "owned-services")
	if err := os.WriteFile(service, []byte("[fixture]\nhost=service-host\nport=6543\nuser=service-user\ndbname=service-db\nsslmode=disable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGSERVICEFILE", service)
	for _, tc := range []struct {
		dsn, host, user, database string
		port                      uint16
	}{
		{"sslmode=disable", "environment-host", "environment-user", "environment-db", 6432},
		{"service=fixture", "service-host", "service-user", "service-db", 6543},
		{"service=fixture host=explicit-host port=7654 user=explicit-user dbname=explicit-db", "explicit-host", "explicit-user", "explicit-db", 7654},
	} {
		cfg, err := BuildMachineConfig(context.Background(), models.Connection{DSN: tc.dsn, Host: "ignored", StatementTimeout: "5s"})
		if err != nil || cfg.Host != tc.host || cfg.Port != tc.port || cfg.User != tc.user || cfg.Database != tc.database {
			t.Fatal("trusted environment/service/explicit target precedence changed")
		}
		if cfg.Tracer != nil || cfg.OnNotice != nil || cfg.OnNotification != nil || cfg.DefaultQueryExecMode != pgx.QueryExecModeExec || cfg.RuntimeParams["statement_timeout"] != "5000" || cfg.RuntimeParams["lock_timeout"] != "1000" {
			t.Fatal("machine callbacks, query mode or timeout ceilings changed")
		}
	}
	profile := models.Connection{DSN: "host=first,second port=5432,6543 user=u dbname=d sslmode=prefer", StatementTimeout: "30s"}
	cfg, err := BuildMachineConfig(context.Background(), profile)
	if err != nil || cfg.Host != "first" || cfg.TLSConfig == nil || len(cfg.Fallbacks) != 3 || cfg.Fallbacks[0].TLSConfig != nil || cfg.Fallbacks[1].Host != "second" || cfg.Fallbacks[1].Port != 6543 || cfg.Fallbacks[1].TLSConfig == nil || cfg.Fallbacks[2].TLSConfig != nil {
		t.Fatal("TLS and multi-host fallback order changed")
	}
	copy := cfg.Copy()
	copy.RuntimeParams["statement_timeout"] = "1"
	copy.Fallbacks[1].Host = "changed"
	if cfg.RuntimeParams["statement_timeout"] != "10000" || cfg.Fallbacks[1].Host != "second" {
		t.Fatal("connection snapshots share mutable settings")
	}
	for _, invalid := range []models.Connection{
		{}, {DSN: "host='SECRET"}, {Host: "host", StatementTimeout: "1s;SECRET"}, {DSN: "service=missing-SECRET"}, {DSN: "host=host port=SECRET"},
	} {
		_, err := BuildMachineConfig(context.Background(), invalid)
		var safe *MachineCredentialError
		if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("invalid configuration classification or privacy changed")
		}
	}
	if err := os.WriteFile(service, []byte("[fixture]\nhost=service-host\nport=SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildMachineConfig(context.Background(), models.Connection{DSN: "service=fixture"})
	var safe *MachineCredentialError
	if !errors.As(err, &safe) || safe.Kind != MachineCredentialUnavailable || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("malformed service data classification or privacy changed")
	}
}
