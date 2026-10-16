//go:build linux || darwin

// Package machineio owns cancellable copies of inherited machine CLI streams.
package machineio

import (
	"context"
	"errors"
	"io"
	"os"
)

var errUnavailable = errors.New("machine stream unavailable")

// OpenInput acquires an owned stream. The caller owns cancellation and Close
// after acquisition; canceling ctx does not close a successfully returned stream.
func OpenInput(ctx context.Context, file *os.File) (io.ReadCloser, error) {
	return acquire(ctx, file, os.O_RDONLY)
}

// OpenOutput acquires an owned stream whose lifetime is independent of ctx so
// cancellation of work cannot consume the caller's remaining publication budget.
func OpenOutput(ctx context.Context, file *os.File) (io.WriteCloser, error) {
	return acquire(ctx, file, os.O_WRONLY)
}

func acquire(ctx context.Context, file *os.File, mode int) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errUnavailable
	}
	stream, err := openStream(ctx, file, mode)
	if canceled := ctx.Err(); canceled != nil {
		if stream != nil {
			_ = stream.Close()
		}
		return nil, canceled
	}
	if err != nil {
		return nil, errUnavailable
	}
	return stream, nil
}
