//go:build linux || darwin

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/davesavic/pgsavvy/internal/machineio"
	"github.com/davesavic/pgsavvy/pkg/cli"
)

func runMachine(args []string) (bool, int) {
	if handled, exit := machineio.RunHelper(); handled {
		return true, exit
	}
	if cli.IsLegacyRoute(args) {
		return false, 0
	}
	parent, cancel := context.WithTimeout(context.Background(), cli.InvocationTimeout)
	defer cancel()
	interrupted, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deadline, _ := parent.Deadline()
	acquisition, cancelAcquisition := context.WithDeadline(parent, deadline.Add(-2*time.Second))
	defer cancelAcquisition()
	joined := make(chan struct{})
	stopAcquisition := context.AfterFunc(interrupted, func() { cancelAcquisition(); close(joined) })
	finishAcquisition := sync.OnceFunc(func() {
		if !stopAcquisition() {
			<-joined
		}
		cancelAcquisition()
	})
	defer finishAcquisition()
	service := cli.InvocationService{Approve: cli.NewTerminalApprovalProvider(machineio.OpenApprovalTerminal)}
	registry := map[string]cli.Command{"connections": cli.ConnectionsCommand(service), "schema": cli.SchemaCommand(service), "query": cli.QueryCommand(service), "capabilities": cli.CapabilitiesCommand()}
	var out io.WriteCloser
	setupFailure := func() (bool, int) {
		exit, code := cli.ExitResource, cli.ResourceFailure
		if acquisition.Err() != nil {
			exit, code = cli.ExitTimeout, cli.Timeout
		}
		if out != nil {
			ctx, cancel := context.WithTimeout(parent, 2*time.Second)
			defer cancel()
			_ = cli.WriteJSON(ctx, out, cli.Envelope{Command: "unknown", Error: &cli.PublicError{Code: code}})
		}
		return true, exit
	}
	var err error
	out, err = machineio.OpenOutput(acquisition, os.Stdout)
	if err != nil {
		return setupFailure()
	}
	defer func() { _ = out.Close() }()
	diagnostic, _ := machineio.OpenOutput(acquisition, os.Stderr)
	if diagnostic != nil {
		defer func() { _ = diagnostic.Close() }()
	}
	in, err := machineio.OpenInput(acquisition, os.Stdin)
	if err != nil {
		return setupFailure()
	}
	defer func() { _ = in.Close() }()
	finishAcquisition()
	streams := cli.IO{In: in, Out: out, Cancel: interrupted.Done()}
	if diagnostic != nil {
		streams.Err = diagnostic
	}
	_, exit := cli.Dispatch(parent, args, streams, registry)
	return true, exit
}
