//go:build linux || darwin || windows

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

func readMachineInput(ctx context.Context, path string, stdin io.ReadCloser) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var reader io.ReadCloser
	if path == "-" {
		reader = stdin
	} else {
		if err := validateMachineFilePath(path); err != nil {
			return nil, &PublicError{Code: InvalidInput}
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, &PublicError{Code: ResourceFailure}
		}
		if !info.Mode().IsRegular() {
			return nil, &PublicError{Code: InvalidInput}
		}
		file, err := os.OpenFile(path, machineFileOpenFlags(), 0)
		if err != nil {
			return nil, &PublicError{Code: ResourceFailure}
		}
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, &PublicError{Code: InvalidInput}
		}
		reader = file
		defer func() { _ = file.Close() }()
	}
	if reader == nil {
		return nil, &PublicError{Code: InvalidInput}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stop, joined := make(chan struct{}), make(chan struct{})
	closeReader := sync.OnceFunc(func() { _ = reader.Close() })
	go func() {
		defer close(joined)
		select {
		case <-ctx.Done():
			closeReader()
		case <-stop:
		}
	}()
	data, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	close(stop)
	<-joined
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, &PublicError{Code: ResourceFailure}
	}
	if len(data) > MaxInputBytes || !utf8.Valid(data) {
		return nil, &PublicError{Code: InvalidInput}
	}
	return data, nil
}

func ReadSQL(ctx context.Context, path string, stdin io.ReadCloser) (string, error) {
	data, err := readMachineInput(ctx, path, stdin)
	if err != nil {
		return "", err
	}
	sql := string(data)
	if strings.TrimSpace(sql) == "" {
		return "", &PublicError{Code: InvalidInput}
	}
	return sql, nil
}

func ReadParameters(ctx context.Context, path string) ([][]byte, error) {
	if path == "-" {
		return nil, &PublicError{Code: InvalidInput}
	}
	data, err := readMachineInput(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var values []any
	if err := decoder.Decode(&values); err != nil || values == nil || len(values) > MaxParameters {
		return nil, &PublicError{Code: InvalidInput}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, &PublicError{Code: InvalidInput}
	}
	parameters := make([][]byte, 0, len(values))
	for _, value := range values {
		switch value := value.(type) {
		case nil:
			parameters = append(parameters, nil)
		case string:
			parameters = append(parameters, []byte(value))
		case bool:
			parameters = append(parameters, []byte(strconv.FormatBool(value)))
		case json.Number:
			parameters = append(parameters, []byte(value.String()))
		default:
			return nil, &PublicError{Code: InvalidInput}
		}
	}
	return parameters, nil
}
