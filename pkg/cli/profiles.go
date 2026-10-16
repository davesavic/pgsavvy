//go:build linux || darwin || windows

package cli

import (
	"os"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/spf13/afero"
)

// A trusted profile file is still data, not a pipe that may wait for a writer.
type machineProfileFS struct{ afero.Fs }

func (fs machineProfileFS) Open(name string) (afero.File, error) {
	if err := validateMachineFilePath(name); err != nil {
		return nil, err
	}
	info, err := fs.Stat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := fs.OpenFile(name, machineFileOpenFlags(), 0)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, os.ErrInvalid
	}
	return file, nil
}

// PublicProfile is an explicit allowlist, never a serialized connection config.
type PublicProfile struct {
	Name             string           `json:"name"`
	Driver           string           `json:"driver"`
	Tags             []string         `json:"tags"`
	Label            string           `json:"label"`
	CLIAccess        models.CLIAccess `json:"cli_access"`
	ReadOnly         bool             `json:"read_only"`
	CredentialStatus string           `json:"credential_status"`
}

func PublicConnection(profile models.Connection) PublicProfile {
	return PublicProfile{Name: profile.Name, Driver: profile.Driver, Tags: append([]string{}, profile.Tags...), Label: profile.Label, CLIAccess: profile.EffectiveCLIAccess(), ReadOnly: profile.ReadOnly, CredentialStatus: "not_checked"}
}
