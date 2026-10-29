//go:build linux || darwin

package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/stretchr/testify/require"
)

func TestMachineCLI_ExplicitTimeout(t *testing.T) {
	streams, out, _ := testStreams()
	called := false
	_, exit := Dispatch(context.Background(), []string{"test", "--timeout=2m"}, streams, map[string]Command{"test": func(ctx context.Context, _ []string, _ IO) (Envelope, error) {
		called = true
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 110*time.Second)
		return Envelope{ResultComplete: true}, nil
	}})
	require.Equal(t, ExitSuccess, exit, out.String())
	require.True(t, called)
}

func TestMachineQuery_TimeoutPropagation(t *testing.T) {
	for _, mode := range []string{"row-read", "full-read-only", "full"} {
		for _, tc := range []struct {
			name, timeout, profile, want string
		}{
			{"default", "", "", "10000"},
			{"explicit", "2m", "", "120000"},
			{"explicit-default", "30s", "", "30000"},
			{"profile-shorter", "2m", "3s", "3000"},
			{"profile-above-old-ceiling", "2m", "30s", "30000"},
			{"profile-off", "2m", "0", "120000"},
			{"profile-longer", "2m", "99d", "120000"},
			{"default-profile", "", "30s", "10000"},
			{"default-shorter-profile", "", "5s", "5000"},
			{"explicit-shorter", "5s", "30s", "5000"},
			{"maximum", "2147483647ms", "", "2147483647"},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				script := &historyWireScript{startup: make(chan map[string]string, 1), statements: make(chan string, 32)}
				service := historyWireService(t, script, nil)
				lookup := service.historyLookup
				service.historyLookup = func(ctx context.Context, name string) (models.Connection, error) {
					profile, err := lookup(ctx, name)
					profile.StatementTimeout = tc.profile
					if mode != "row-read" {
						profile.CLIAccess = models.CLIAccessFull
						profile.ReadOnly = mode == "full-read-only"
					}
					return profile, err
				}
				var extra []string
				if tc.timeout != "" {
					extra = []string{"--timeout=" + tc.timeout}
				}
				exit, output := runHistoryQuery(t, service, "SELECT 'history_payload'", extra...)
				require.Equal(t, ExitSuccess, exit, output)
				require.Equal(t, tc.want, (<-script.startup)["statement_timeout"])
				var statements []string
				for len(script.statements) > 0 {
					statements = append(statements, <-script.statements)
				}
				require.Contains(t, statements, "SET statement_timeout="+tc.want)
				if mode != "full" {
					require.Contains(t, statements, "SET LOCAL statement_timeout="+tc.want)
				}
				for _, sql := range statements {
					if strings.Contains(sql, "statement_timeout=") {
						require.True(t, strings.HasSuffix(sql, "="+tc.want), "inconsistent timeout: %s", sql)
					}
				}
			})
		}
	}
}
