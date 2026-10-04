//go:build !unix && !windows

package session

import (
	"context"
	"os/exec"
)

func runPasswordCommand(_ context.Context, c *exec.Cmd) error {
	return c.Run()
}
