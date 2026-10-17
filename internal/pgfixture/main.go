// Command pgfixture gives fixture controllers file-backed standard handles.
package main

import (
	"errors"
	"os"
	"os/exec"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) < 2 {
		return 2
	}
	log, err := os.OpenFile(args[1], os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 1
	}
	// pg_ctl intentionally leaves a server and, on Windows, its shell alive.
	// Neither may inherit capture pipes whose EOF would delay this invocation.
	command := exec.Command(args[0], args[2:]...)
	command.Stdout = log
	command.Stderr = log
	err = command.Run()
	closeErr := log.Close()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return 1
	}
	if closeErr != nil {
		return 1
	}
	return 0
}
