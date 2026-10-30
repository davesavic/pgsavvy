//go:build linux || darwin

package session

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const temporaryPassfileMaxBytes = 1 << 20

func temporaryCredentialUnavailable() error {
	return &MachineCredentialError{Kind: MachineCredentialUnavailable}
}

// ResolveTemporaryMachinePassword has exactly one credential source. It never
// invokes the saved-profile resolver or pgx's ambient password fallback.
func ResolveTemporaryMachinePassword(ctx context.Context, target TemporaryTarget) (string, error) {
	return resolveTemporaryMachinePassword(ctx, target, openTemporaryPassfile)
}

func openTemporaryPassfile(path string) (*os.File, error) {
	// Nonblocking avoids FIFO hangs; pathological filesystem syscalls still cannot
	// universally be interrupted. No abandoned background reader is created.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "temporary passfile"), nil
}

func temporaryPassfileProtected(info os.FileInfo, euid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Uid == uint32(euid) && info.Mode().Perm()&0o077 == 0 && info.Size() <= temporaryPassfileMaxBytes
}

func resolveTemporaryMachinePassword(ctx context.Context, target TemporaryTarget, open func(string) (*os.File, error)) (string, error) {
	if err := validateTemporaryAcquisition(ctx, target); err != nil {
		return "", err
	}
	file, err := open(target.passfile)
	if err != nil {
		return "", machineCredentialError(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !temporaryPassfileProtected(info, os.Geteuid()) {
		return "", temporaryCredentialUnavailable()
	}
	data := make([]byte, 0, min(info.Size(), temporaryPassfileMaxBytes))
	var chunk [32 << 10]byte
	for {
		if ctx.Err() != nil {
			return "", &MachineCredentialError{Kind: MachineCredentialTimeout}
		}
		n, err := file.Read(chunk[:min(len(chunk), temporaryPassfileMaxBytes+1-len(data))])
		data = append(data, chunk[:n]...)
		if len(data) > temporaryPassfileMaxBytes {
			return "", temporaryCredentialUnavailable()
		}
		if err == io.EOF {
			break
		}
		if err != nil || n == 0 {
			return "", temporaryCredentialUnavailable()
		}
	}
	return matchTemporaryPassfile(ctx, target, string(data))
}

func matchTemporaryPassfile(ctx context.Context, target TemporaryTarget, data string) (string, error) {
	host := target.host
	if target.socketDir != "" {
		host = "localhost"
	}
	want := [4]string{host, strconv.Itoa(int(target.port)), target.database, target.username}
	for line := range strings.SplitSeq(data, "\n") {
		if ctx.Err() != nil {
			return "", &MachineCredentialError{Kind: MachineCredentialTimeout}
		}
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var fields [5]string
		var field strings.Builder
		index := 0
		checkpoint := 0
		for i := 0; i < len(line); i++ {
			if i >= checkpoint {
				if ctx.Err() != nil {
					return "", &MachineCredentialError{Kind: MachineCredentialTimeout}
				}
				checkpoint = i + 4096
			}
			if line[i] == '\\' && i+1 < len(line) {
				i++
				field.WriteByte(line[i])
			} else if line[i] == ':' {
				if index == 4 {
					index++
					break
				}
				fields[index] = field.String()
				field.Reset()
				index++
			} else {
				field.WriteByte(line[i])
			}
		}
		if ctx.Err() != nil {
			return "", &MachineCredentialError{Kind: MachineCredentialTimeout}
		}
		if index != 4 {
			continue
		}
		fields[4] = field.String()
		matched := true
		for i, value := range want {
			matched = matched && pgpassFieldMatches(fields[i], value)
		}
		if matched {
			if fields[4] == "" || strings.IndexByte(fields[4], 0) >= 0 {
				return "", temporaryCredentialUnavailable()
			}
			return fields[4], nil
		}
	}
	return "", temporaryCredentialUnavailable()
}
