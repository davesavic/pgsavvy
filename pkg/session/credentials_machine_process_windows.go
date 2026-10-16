package session

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errMachineProcess = errors.New("credential process unavailable")

// Hooks are invocation-local so startup fixtures never change another launch.
type machineWindowsProcessHooks struct {
	suspended    func(windows.Handle)
	assign       func(windows.Handle, windows.Handle) error
	beforeResume func()
}

func runMachinePasswordCommand(ctx context.Context, command *exec.Cmd) error {
	return runMachineWindowsPasswordCommand(ctx, command, machineWindowsProcessHooks{})
}

func runMachineWindowsPasswordCommand(ctx context.Context, c *exec.Cmd, hooks machineWindowsProcessHooks) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// This private adapter implements the saved-helper caller's command contract,
	// not arbitrary os/exec plumbing. Never silently ignore unsupported settings.
	if c == nil || c.Err != nil || c.Path == "" || c.Dir != "" || c.SysProcAttr != nil || c.Stdin != nil ||
		c.Stderr != nil && c.Stderr != io.Discard || len(c.ExtraFiles) != 0 || c.WaitDelay != 0 || c.Process != nil || c.ProcessState != nil {
		return errMachineProcess
	}
	app, line, env, err := machineWindowsCommand(c)
	if err != nil {
		return errMachineProcess
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return errMachineProcess
	}
	defer func() { _ = windows.CloseHandle(job) }()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return errMachineProcess
	}
	reader, writer, err := machineWindowsStdout()
	if err != nil {
		return errMachineProcess
	}
	defer func() { _ = reader.Close() }()
	childHandles := []windows.Handle{0, writer, 0}
	closeChildren := func() {
		for i, handle := range childHandles {
			if handle != 0 {
				_ = windows.CloseHandle(handle)
				childHandles[i] = 0
			}
		}
	}
	defer closeChildren()
	null, _ := windows.UTF16PtrFromString("NUL")
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	childHandles[0], err = windows.CreateFile(null, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		childHandles[0] = 0
		return errMachineProcess
	}
	childHandles[2], err = windows.CreateFile(null, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		childHandles[2] = 0
		return errMachineProcess
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return errMachineProcess
	}
	defer attributes.Delete()
	if err := attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&childHandles[0]), uintptr(len(childHandles))*unsafe.Sizeof(childHandles[0])); err != nil {
		return errMachineProcess
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = childHandles[0], childHandles[1], childHandles[2]
	if err := ctx.Err(); err != nil {
		return err
	}
	var process windows.ProcessInformation
	err = windows.CreateProcess(app, line, nil, nil, true,
		windows.CREATE_SUSPENDED|windows.CREATE_UNICODE_ENVIRONMENT|windows.EXTENDED_STARTUPINFO_PRESENT, &env[0], nil, &si.StartupInfo, &process)
	runtime.KeepAlive(childHandles)
	runtime.KeepAlive(env)
	if err != nil {
		return errMachineProcess
	}
	defer func() { _ = windows.CloseHandle(process.Process); _ = windows.CloseHandle(process.Thread) }()
	closeChildren()
	if hooks.suspended != nil {
		hooks.suspended(process.Process)
	}
	assign := hooks.assign
	if assign == nil {
		assign = windows.AssignProcessToJobObject
	}
	if err := assign(job, process.Process); err != nil {
		_ = machineStopSuspended(process.Process, time.Now().Add(2*time.Second))
		return errMachineProcess
	}
	if hooks.beforeResume != nil {
		hooks.beforeResume()
	}
	if err := ctx.Err(); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		if !machineStopSuspended(process.Process, time.Now().Add(2*time.Second)) {
			return errMachineProcess
		}
		return err
	}
	if previous, err := windows.ResumeThread(process.Thread); err != nil || previous != 1 {
		_ = windows.TerminateJobObject(job, 1)
		_ = machineStopSuspended(process.Process, time.Now().Add(2*time.Second))
		return errMachineProcess
	}
	stdout := c.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	drained := make(chan error, 1)
	go func() { _, err := io.Copy(stdout, reader); drained <- err }()
	var result error
	var exitCode uint32
	drainComplete := false
	waiting := true
	for waiting {
		select {
		case <-ctx.Done():
			result, waiting = ctx.Err(), false
		case err := <-drained:
			drainComplete = true
			if err != nil {
				result, waiting = errMachineProcess, false
			}
		default:
		}
		if !waiting {
			break
		}
		state, err := windows.WaitForSingleObject(process.Process, 10)
		if err != nil || state != uint32(windows.WAIT_TIMEOUT) && state != windows.WAIT_OBJECT_0 {
			result, waiting = errMachineProcess, false
		} else if state == windows.WAIT_OBJECT_0 {
			if windows.GetExitCodeProcess(process.Process, &exitCode) != nil {
				result = errMachineProcess
			}
			waiting = false
		}
	}
	// Main-process exit is independent of pipe EOF. Stop descendants first,
	// including descendants that inherited stdout after the shell already exited.
	cleanupDeadline := time.Now().Add(2 * time.Second)
	if windows.TerminateJobObject(job, 1) != nil {
		result = errMachineProcess
	}
	if !machineWindowsJobIdle(job, cleanupDeadline) {
		result = errMachineProcess
	}
	if state, err := windows.WaitForSingleObject(process.Process, 0); err != nil || state != windows.WAIT_OBJECT_0 {
		_ = machineStopSuspended(process.Process, cleanupDeadline)
		result = errMachineProcess
	}
	if !drainComplete {
		// The writer is invocation-owned and cooperating. Closing this IOCP reader
		// cancels a pending read; no synchronous anonymous-pipe reader is abandoned.
		timer := time.NewTimer(time.Until(cleanupDeadline))
		select {
		case err := <-drained:
			if err != nil {
				result = errMachineProcess
			}
		case <-timer.C:
			_ = reader.Close()
			<-drained
			result = errMachineProcess
		}
		timer.Stop()
	}
	if result != nil {
		return result
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if exitCode != 0 {
		return machineWindowsExitError(exitCode)
	}
	return nil
}

type machineWindowsExitError uint32

func (machineWindowsExitError) Error() string   { return "credential process failed" }
func (e machineWindowsExitError) ExitCode() int { return int(e) }

func machineStopSuspended(process windows.Handle, deadline time.Time) bool {
	_ = windows.TerminateProcess(process, 1)
	// Never kill by PID or wait without a cleanup bound. An unassigned process
	// reaches this path only while suspended, so failure cannot run its code.
	remaining := max(time.Until(deadline), 0)
	state, waitErr := windows.WaitForSingleObject(process, uint32(remaining/time.Millisecond))
	// TerminateProcess may race a natural exit. Only a signaled process object
	// establishes completion; do not infer it from a PID or an exit-code sentinel.
	return waitErr == nil && state == windows.WAIT_OBJECT_0
}

type machineJobAccounting struct {
	TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, Terminated uint32
}

func machineWindowsJobIdle(job windows.Handle, deadline time.Time) bool {
	for {
		var info machineJobAccounting
		if windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) != nil {
			return false
		}
		if info.ActiveProcesses == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func machineWindowsCommand(c *exec.Cmd) (app, line *uint16, env []uint16, err error) {
	path, err := filepath.Abs(c.Path)
	if err != nil {
		return nil, nil, nil, err
	}
	app, err = windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, nil, err
	}
	args := c.Args
	if len(args) == 0 {
		args = []string{c.Path}
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	line, err = windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return nil, nil, nil, err
	}
	for _, value := range c.Env {
		if strings.ContainsRune(value, 0) {
			return nil, nil, nil, errMachineProcess
		}
	}
	// Cmd.Environ retains Go's case-insensitive last-value deduplication and
	// SYSTEMROOT completion. The launcher receives only this explicit block.
	values := c.Environ()
	key := func(value string) string {
		k, _, _ := strings.Cut(value, "=")
		b := []byte(k)
		for i, c := range b {
			if c >= 'a' && c <= 'z' {
				b[i] -= 'a' - 'A'
			}
		}
		return string(b)
	}
	slices.SortFunc(values, func(a, b string) int { return strings.Compare(key(a), key(b)) })
	for _, value := range values {
		encoded, encodeErr := windows.UTF16FromString(value)
		if encodeErr != nil {
			return nil, nil, nil, encodeErr
		}
		env = append(env, encoded...)
	}
	if len(env) == 0 {
		env = append(env, 0)
	}
	env = append(env, 0)
	return app, line, env, nil
}

func machineWindowsStdout() (*os.File, windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, 0, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, 0, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(`\\.\pipe\pgsavvy-credential-` + rand.Text())
	if err != nil {
		return nil, 0, err
	}
	server, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, &sa)
	runtime.KeepAlive(sd)
	if err != nil {
		return nil, 0, err
	}
	inheritable := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	writer, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, &inheritable, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		_ = windows.CloseHandle(server)
		return nil, 0, err
	}
	reader := os.NewFile(uintptr(server), "pgsavvy-credential-stdout")
	if reader == nil {
		_ = windows.CloseHandle(server)
		_ = windows.CloseHandle(writer)
		return nil, 0, errMachineProcess
	}
	if err := reader.SetReadDeadline(time.Time{}); err != nil {
		_ = windows.CloseHandle(writer)
		_ = reader.Close()
		return nil, 0, err
	}
	return reader, writer, nil
}
