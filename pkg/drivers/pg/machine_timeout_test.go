//go:build linux || darwin

package pg

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMachineOpen_StatementTimeoutBounds(t *testing.T) {
	machineTestEnvironment(t)
	for _, tc := range []struct {
		timeout time.Duration
		wantMS  int
	}{
		{0, 10000},
		{time.Nanosecond, 1},
		{1500 * time.Microsecond, 2},
		{2 * time.Minute, 120000},
		{(1<<31 - 1) * time.Millisecond, 1<<31 - 1},
	} {
		t.Run(tc.timeout.String(), func(t *testing.T) {
			fixture := newMachineWireFixture(t)
			client, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{StatementTimeout: tc.timeout})
			if err != nil {
				t.Fatal(err)
			}
			if err := client.Ping(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := client.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			want := strconv.Itoa(tc.wantMS)
			if fixture.startup["statement_timeout"] != want || client.statementMS != tc.wantMS {
				t.Fatalf("startup/client limit differs: %v / %d", fixture.startup, client.statementMS)
			}
			queries := strings.Join(fixture.queries, "\n")
			if !strings.Contains(queries, "SET statement_timeout="+want+"\n") || !strings.Contains(queries, "SET LOCAL statement_timeout="+want+"\n") {
				t.Fatalf("session/transaction limits differ: %s", queries)
			}
		})
	}
	for _, timeout := range []time.Duration{-1, (1<<31-1)*time.Millisecond + 1} {
		fixture := newMachineWireFixture(t)
		client, err := OpenMachine(context.Background(), fixture.profile(t), MachineOptions{StatementTimeout: timeout})
		if err == nil || client != nil || fixture.connections.Load() != 0 {
			t.Fatalf("invalid timeout %s reached network", timeout)
		}
	}
}
