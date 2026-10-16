package session

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func machineCredentialHelperCommand(ctx context.Context, mode string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, os.Args[0], machineCredentialHelperArgs(mode, args...)...)
	command.Env = append(scrubbedEnv(os.Environ()), machineHelperFixtureEnv+"=1")
	command.Stderr = io.Discard
	return command
}

func TestMachineCredentialHelperRestrictedParentProcess(t *testing.T) {
	if os.Getenv("PGSAVVY_MACHINE_RESTRICTED_PARENT") != "1" {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal("restricted parent fixture creation failed")
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal("restricted parent fixture configuration failed")
	}
	if windows.AssignProcessToJobObject(job, windows.CurrentProcess()) != nil {
		t.Fatal("restricted parent fixture assignment failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	path := os.Getenv("PGSAVVY_MACHINE_RESTRICTED_SENTINEL")
	if path == "" {
		t.Fatal("restricted parent sentinel missing")
	}
	command := machineCredentialHelperCommand(ctx, "touch", path)
	command.Stdout = io.Discard
	start := time.Now()
	if runMachinePasswordCommand(ctx, command) == nil || time.Since(start) > 2*time.Second {
		t.Fatal("restricted parent allowed another contained process")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("restricted parent allowed helper execution")
	}
	var accounting machineJobAccounting
	if windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil) != nil || accounting.ActiveProcesses != 1 {
		t.Fatal("restricted parent retained an extra process")
	}
}
