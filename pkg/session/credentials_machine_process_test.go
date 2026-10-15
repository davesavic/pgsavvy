//go:build linux

package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMachineCredentials_HelperLimits(t *testing.T) {
	machineCredentialFixture(t)
	for _, tc := range []struct{ command, want string }{
		{"printf value", "value"},
		{"printf 'value\\r\\n'", "value"},
		{"printf secret >&2; printf value", "value"},
		{"true", ""},
		{"if read -r value; then printf unexpected; else printf no-stdin; fi", "no-stdin"},
		{"printf '%32768s' x", strings.Repeat(" ", 32767) + "x"},
	} {
		got, err := ExecMachineCredential(context.Background(), tc.command, MachineCredentialLimits{})
		if err != nil || got != tc.want {
			t.Fatalf("helper %q: bytes=%d %v", tc.command, len(got), err)
		}
	}
	for _, tc := range []struct {
		command string
		limits  MachineCredentialLimits
		kind    MachineCredentialFailure
	}{
		{"printf '%32769s' x", MachineCredentialLimits{}, MachineCredentialOutputLimit},
		{"printf SECRET >&2;exit 1", MachineCredentialLimits{}, MachineCredentialUnavailable},
		{"sleep 15", MachineCredentialLimits{Timeout: 30 * time.Millisecond}, MachineCredentialTimeout},
		{"sleep 15", MachineCredentialLimits{}, MachineCredentialTimeout},
	} {
		start := time.Now()
		_, err := ExecMachineCredential(context.Background(), tc.command, tc.limits)
		var safe *MachineCredentialError
		if !errors.As(err, &safe) || safe.Kind != tc.kind || strings.Contains(err.Error(), "SECRET") || time.Since(start) > 7*time.Second {
			t.Fatalf("helper limits: %v elapsed=%s", err, time.Since(start))
		}
	}
	t.Setenv(keyringPassphraseEnv, "SECRET")
	got, err := ExecMachineCredential(context.Background(), `printf '%s' "${PGSAVVY_KEYRING_PASSPHRASE-missing}"`, MachineCredentialLimits{})
	if err != nil || got != "missing" {
		t.Fatal("helper inherited keyring passphrase")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cancel()
			if _, err := ExecMachineCredential(ctx, "sleep 15", MachineCredentialLimits{}); err == nil {
				t.Error("cancelled helper succeeded")
			}
		})
	}
	wg.Wait()
}

func TestMachineCredentials_HelperLimits_Descendant(t *testing.T) {
	_, dir := machineCredentialFixture(t)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	command := "PGSAVVY_PASSWORD_PROBE_DIR=" + quote(dir) + " " + quote(os.Args[0]) + " -test.run=^TestReview_password_descendant_process$ & wait"
	go func() { _, err := ExecMachineCredential(ctx, command, MachineCredentialLimits{}); result <- err }()
	t.Cleanup(func() { cancel(); passwordProbeStop(dir) })
	pid := passwordProbePID(t, dir, "pid")
	start := time.Now()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("descendant invocation succeeded after cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("descendant cleanup exceeded2s")
	}
	passwordProbeAwaitStopped(t, pid, start.Add(2*time.Second))
	if _, err := os.Stat(filepath.Join(dir, "pid")); err != nil {
		t.Fatal("descendant fixture never started")
	}
}

func TestMachineCredentials_HelperLimits_ConcurrentRunning(t *testing.T) {
	_, dir := machineCredentialFixture(t)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	first, cancelFirst := context.WithCancel(context.Background())
	second, cancelSecond := context.WithCancel(context.Background())
	t.Cleanup(func() { cancelFirst(); cancelSecond() })
	results := make(chan error, 1)
	survivor := make(chan error, 1)
	release := filepath.Join(dir, "release")
	go func() {
		_, err := ExecMachineCredential(first, "printf '%s' \"$$\" > "+quote(filepath.Join(dir, "first.pid"))+"; sleep 15", MachineCredentialLimits{})
		results <- err
	}()
	go func() {
		value, err := ExecMachineCredential(second, "printf '%s' \"$$\" > "+quote(filepath.Join(dir, "second.pid"))+"; while [ ! -f "+quote(release)+" ]; do sleep 0.01; done; printf survivor", MachineCredentialLimits{})
		if err == nil && value != "survivor" {
			err = errors.New("wrong surviving helper result")
		}
		survivor <- err
	}()
	firstPID := passwordProbePID(t, dir, "first.pid")
	secondPID := passwordProbePID(t, dir, "second.pid")
	start := time.Now()
	cancelFirst()
	cancelFirst()
	select {
	case err := <-results:
		if err == nil {
			t.Fatal("cancelled running helper succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("running helper cleanup exceeded2s")
	}
	passwordProbeAwaitStopped(t, firstPID, start.Add(2*time.Second))
	if !passwordProbeRunning(secondPID) {
		t.Fatal("cancelling first helper killed independent helper")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-survivor:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("independent helper did not complete")
	}
}
