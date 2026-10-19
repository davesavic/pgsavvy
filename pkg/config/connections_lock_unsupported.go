//go:build !linux && !darwin && !windows

package config

import (
	"errors"
	"os"
)

func openConnectionsLock(string) (*os.File, error) {
	return nil, errors.New("unsupported connections filesystem locking")
}

func safeConnectionsFile(os.FileInfo) bool { return false }

func validateConnectionsLinks(string) error {
	return errors.New("unsupported connections filesystem locking")
}

func validateConnectionsBasename(string) error {
	return errors.New("unsupported connections filesystem locking")
}

func tryConnectionsLock(*os.File) (bool, error) {
	return false, errors.New("unsupported connections filesystem locking")
}
