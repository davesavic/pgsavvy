//go:build linux

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/davesavic/pgsavvy/internal/machineio"
	"github.com/davesavic/pgsavvy/pkg/cli"
)

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
	out, err := machineio.OpenOutput(parent, os.Stdout)
	if err != nil {
		return true, cli.ExitResource
	}
	defer func() { _ = out.Close() }()
	diagnostic, _ := machineio.OpenOutput(parent, os.Stderr)
	if diagnostic != nil {
		defer func() { _ = diagnostic.Close() }()
	}
	in, err := machineio.OpenInput(parent, os.Stdin)
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
