package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

const machineHelperFixtureEnv = "PGSAVVY_MACHINE_HELPER_FIXTURE"

func machineCredentialHelperArgs(mode string, args ...string) []string {
	return append([]string{"-test.run=^TestMachineCredentialHelperProcess$", "--", mode}, args...)
}

// This fixture is the test binary itself, never a separately installed helper.
func TestMachineCredentialHelperProcess(t *testing.T) {
	if os.Getenv(machineHelperFixtureEnv) != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(90)
	}
	switch args[0] {
	case "output":
		_, _ = fmt.Fprint(os.Stdout, args[1])
	case "exit":
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		if err != nil || len(data) != 0 {
			os.Exit(91)
		}
		_, _ = fmt.Fprint(os.Stdout, "no-stdin")
	case "environment":
		if os.Getenv(keyringPassphraseEnv) != "" {
			os.Exit(92)
		}
		_, _ = fmt.Fprint(os.Stdout, os.Getenv("PGSAVVY_MACHINE_HELPER_VALUE"))
	case "touch":
		if os.WriteFile(args[1], []byte("started"), 0o600) != nil {
			os.Exit(93)
		}
	case "hold":
		if os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			os.Exit(94)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "descendant", "early-descendant":
		child := exec.Command(os.Args[0], machineCredentialHelperArgs("hold", args[1])...)
		child.Env = os.Environ()
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil {
			os.Exit(95)
		}
		// Do not exit until the child has opened its test-owned readiness file.
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(args[1]); err == nil {
				break
			}
			if time.Now().After(deadline) {
				os.Exit(96)
			}
			time.Sleep(time.Millisecond)
		}
		_, _ = fmt.Fprint(os.Stdout, "descendant-ready")
		if args[0] == "early-descendant" {
			if len(args) > 2 {
				for {
					if _, err := os.Stat(args[2]); err == nil {
						break
					}
					if time.Now().After(deadline) {
						os.Exit(98)
					}
					time.Sleep(time.Millisecond)
				}
			}
			os.Exit(0)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(97)
	}
	os.Exit(0)
}
