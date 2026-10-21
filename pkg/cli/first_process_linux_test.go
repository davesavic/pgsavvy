//go:build integration && linux

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func processSignalReady(pid int, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
		if err != nil {
			return false
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "SigCgt:") {
				value, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "SigCgt:")), 16, 64)
				if err == nil && value&(1<<(uint(syscall.SIGTERM)-1)) != 0 && value&(1<<(uint(syscall.SIGINT)-1)) != 0 {
					// Go installs handlers before main. Also prove acquisition,
					// without mistaking an aliased output duplicate for input.
					if processInputAcquired(pid) {
						return true
					}
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

func processInputAcquired(pid int) bool {
	base := filepath.Join("/proc", strconv.Itoa(pid))
	directory := filepath.Join(base, "fd")
	input, err := os.Readlink(filepath.Join(directory, "0"))
	if err != nil {
		return false
	}
	socket := strings.HasPrefix(input, "socket:")
	required := 1
	if socket {
		// All socket duplicates have O_RDWR. Acquisition is ordered out, err,
		// in: each standard alias needs its own owned copy before input is ready.
		for _, descriptor := range []string{"1", "2"} {
			if target, _ := os.Readlink(filepath.Join(directory, descriptor)); target == input {
				required++
			}
		}
	}
	files, err := os.ReadDir(directory)
	if err != nil {
		return false
	}
	owned := 0
	for _, file := range files {
		descriptor, err := strconv.Atoi(file.Name())
		if err != nil || descriptor < 3 {
			continue
		}
		target, err := os.Readlink(filepath.Join(directory, file.Name()))
		if err != nil || target != input {
			continue
		}
		if !socket {
			info, err := os.ReadFile(filepath.Join(base, "fdinfo", file.Name()))
			if err != nil {
				continue
			}
			readable := false
			for line := range strings.SplitSeq(string(info), "\n") {
				if value, ok := strings.CutPrefix(line, "flags:"); ok {
					flags, err := strconv.ParseUint(strings.TrimSpace(value), 8, 64)
					readable = err == nil && flags&unix.O_ACCMODE == unix.O_RDONLY
				}
			}
			if !readable {
				continue
			}
		}
		owned++
	}
	return owned >= required
}
