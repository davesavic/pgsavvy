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

const helperFailure = 125

func run(args []string) int {
	if len(args) < 2 {
		return helperFailure
	}
	log, err := os.OpenFile(args[1], os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return helperFailure
	}
	stderr, err := os.OpenFile(args[1]+".err", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		_ = log.Close()
		return helperFailure
	}
	// pg_ctl intentionally leaves a server and, on Windows, its shell alive.
	// Neither may inherit capture pipes whose EOF would delay this invocation.
	command := exec.Command(args[0], args[2:]...)
	command.Stdout = log
	command.Stderr = stderr
	err = command.Run()
	closeErr := log.Close()
	stderrCloseErr := stderr.Close()
	return commandStatus(err, closeErr, stderrCloseErr)
}

func commandStatus(err, stdoutCloseErr, stderrCloseErr error) int {
	// A caller may expect a child failure, but must never accept failed logging.
	if stdoutCloseErr != nil || stderrCloseErr != nil {
		return helperFailure
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return helperFailure
	}
	return 0
}
