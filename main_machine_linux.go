//go:build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/davesavic/pgsavvy/pkg/cli"
)

// Duplicates start above standard descriptor slots, including when one of the
// inherited standard handles is closed. Pipe nonblocking mode makes Go's poller
// interruptible by Close; regular-file offsets/append flags remain inherited.
func machineFile(file *os.File, mode int) (*os.File, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	source := int(file.Fd())
	opened := -1
	if info.Mode()&os.ModeCharDevice != 0 {
		// Reopen a terminal as a separate open-file description rather than
		// changing the parent shell's shared terminal nonblocking flags.
		opened, err = unix.Open("/proc/self/fd/"+strconv.Itoa(source), mode|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		source = opened
		defer func() { _ = unix.Close(opened) }()
	}
	duplicate, err := unix.FcntlInt(uintptr(source), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	if info.Mode()&(os.ModeNamedPipe|os.ModeSocket) != 0 {
		if err := unix.SetNonblock(duplicate, true); err != nil {
			_ = unix.Close(duplicate)
			return nil, err
		}
	}
	return os.NewFile(uintptr(duplicate), "pgsavvy-machine-stdio"), nil
}

func runMachine(args []string) (bool, int) {
	if cli.IsLegacyRoute(args) {
		return false, 0
	}
	parent, cancel := context.WithTimeout(context.Background(), cli.InvocationTimeout)
	defer cancel()
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	service := cli.InvocationService{}
	registry := map[string]cli.Command{"connections": cli.ConnectionsCommand(service), "schema": cli.SchemaCommand(service), "query": cli.QueryCommand(service), "capabilities": cli.CapabilitiesCommand()}
	out, err := machineFile(os.Stdout, unix.O_WRONLY)
	if err != nil {
		return true, cli.ExitResource
	}
	defer func() { _ = out.Close() }()
	diagnostic, _ := machineFile(os.Stderr, unix.O_WRONLY)
	if diagnostic != nil {
		defer func() { _ = diagnostic.Close() }()
	}
	in, err := machineFile(os.Stdin, unix.O_RDONLY)
	if err != nil {
		ctx, cancel := context.WithTimeout(parent, 2*time.Second)
		defer cancel()
		_ = cli.WriteJSON(ctx, out, cli.Envelope{Command: "unknown", Error: &cli.PublicError{Code: cli.ResourceFailure}})
		return true, cli.ExitResource
	}
	defer func() { _ = in.Close() }()
	streams := cli.IO{In: in, Out: out, Cancel: interrupted.Done()}
	if diagnostic != nil {
		streams.Err = diagnostic
	}
	_, exit := cli.Dispatch(parent, args, streams, registry)
	return true, exit
}
