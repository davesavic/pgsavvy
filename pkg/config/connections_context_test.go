package config

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestConnectionsContext_ParsingCancellation(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for _, text := range []string{
		"connections:\n - name: dev\n   cli_history: true\n#" + strings.Repeat("comment", 160000),
		"connections:\n - name: dev\n   tags:\n" + strings.Repeat("    - tag\n", 100000),
	} {
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, "/connections.yml", []byte(text), 0o600))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		start := time.Now()
		_, err := LoadConnectionsContext(ctx, fs, "/connections.yml", io.Discard)
		cancel()
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.Less(t, time.Since(start), 150*time.Millisecond, "parser must observe cancellation, not just check afterward")
	}
}

func TestConnectionsContext_StrictCompatibility(t *testing.T) {
	for _, setting := range []string{"", "   cli_history: true\n", "   cli_history: false\n", "   cli_history: null\n", "   cli_history: yes\n", "   cli_history: 'true'\n", "   unknown: true\n", "   cli_access: invalid\n"} {
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, "/connections.yml", []byte("connections:\n - name: dev\n"+setting), 0o600))
		old, oldErr := LoadConnectionsWithWarnings(fs, "/connections.yml", io.Discard)
		current, err := LoadConnectionsContext(context.Background(), fs, "/connections.yml", io.Discard)
		require.Equal(t, old, current)
		if oldErr != nil {
			require.EqualError(t, err, oldErr.Error())
		} else {
			require.NoError(t, err)
		}
	}
	fs := afero.NewMemMapFs()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := LoadConnectionsContext(ctx, fs, "/missing.yml", nil)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, afero.WriteFile(fs, "/empty.yml", nil, 0o600))
	rows, err := LoadConnectionsContext(context.Background(), fs, "/empty.yml", nil)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestConnectionsContext_WideMapping(t *testing.T) {
	var text strings.Builder
	text.WriteString("connections:\n - name: dev\n")
	for i := range 3000 {
		fmt.Fprintf(&text, "   unknown_%d: value\n", i)
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/connections.yml", []byte(text.String()), 0o600))
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := LoadConnectionsContext(ctx, fs, "/connections.yml", io.Discard)
	require.ErrorContains(t, err, "too many connection mapping fields")
	require.Less(t, time.Since(start), 250*time.Millisecond)
}

func TestConnectionsContext_ComplexKey(t *testing.T) {
	var text strings.Builder
	text.WriteString("connections:\n - name: dev\n   ? {")
	for i := range 12000 {
		fmt.Fprintf(&text, "unknown_%d: value,", i)
	}
	text.WriteString("}\n   : ignored\n")
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/connections.yml", []byte(text.String()), 0o600))
	t.Run("structural rejection", func(t *testing.T) {
		_, err := LoadConnectionsContext(context.Background(), fs, "/connections.yml", io.Discard)
		require.ErrorContains(t, err, "too many connection mapping fields")
	})
	t.Run("cancellation during parsing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Cancel on a parser read rather than racing the scheduler against a
		// structural assertion. Start directly at parsing, after file reads.
		polls := 0
		parsing := cancelPollingContext{Context: ctx, poll: func() {
			polls++
			if polls == 10 {
				cancel()
			}
		}}
		info, err := fs.Stat("/connections.yml")
		require.NoError(t, err)
		_, err = decodeConnectionsContext(parsing, []byte(text.String()), info, "/connections.yml", io.Discard)
		require.ErrorIs(t, err, context.Canceled)
		require.GreaterOrEqual(t, polls, 10)
	})
}

type cancelPollingContext struct {
	context.Context
	poll func()
}

func (c cancelPollingContext) Err() error {
	c.poll()
	return c.Context.Err()
}
