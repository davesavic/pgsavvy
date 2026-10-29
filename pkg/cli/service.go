//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/config"
	"github.com/davesavic/pgsavvy/pkg/drivers/pg"
	"github.com/davesavic/pgsavvy/pkg/env"
	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/query"
)

type InvocationService struct {
	Load    func() ([]models.Connection, error)
	Options pg.MachineOptions
	Approve ApprovalProvider

	// Private seams carry verified consent provenance, never arbitrary Load
	// output. Production reads trusted saved metadata; tests inject fixtures.
	historyLookup func(context.Context, string) (models.Connection, error)
	historyAppend func(context.Context, string, query.HistoryEvent) error
	historyPath   string
}

func LoadProfiles(fs afero.Fs, path string) ([]models.Connection, error) {
	profiles, err := config.LoadConnectionsWithWarnings(machineProfileFS{fs}, path, io.Discard)
	if err != nil {
		return nil, &PublicError{Code: ConnectionUnavailable}
	}
	return profiles, nil
}

func (s InvocationService) Profiles() ([]models.Connection, error) {
	if s.Load != nil {
		profiles, err := s.Load()
		if err != nil {
			return nil, &PublicError{Code: ConnectionUnavailable}
		}
		return profiles, nil
	}
	return LoadProfiles(afero.NewOsFs(), filepath.Join(env.GetConfigDir(), "connections.yml"))
}

func ResolveExactProfile(profiles []models.Connection, name string) (models.Connection, error) {
	if name == "" || len(name) > MaxProfileBytes || !utf8.ValidString(name) {
		return models.Connection{}, &PublicError{Code: InvalidInput}
	}
	index := -1
	for i, profile := range profiles {
		if profile.Name == name {
			if index != -1 {
				return models.Connection{}, &PublicError{Code: ConnectionUnavailable}
			}
			index = i
		}
	}
	if index == -1 {
		return models.Connection{}, &PublicError{Code: ConnectionUnavailable}
	}
	profile := profiles[index]
	profile.Tags = append([]string(nil), profile.Tags...)
	profile.HiddenSchemas = append([]string(nil), profile.HiddenSchemas...)
	if profile.SSHTunnel != nil {
		copy := *profile.SSHTunnel
		profile.SSHTunnel = &copy
	}
	return profile, nil
}

func (s InvocationService) Open(ctx context.Context, name string, required models.CLIAccess) (*pg.MachineClient, models.Connection, error) {
	if ctx.Err() != nil {
		return nil, models.Connection{}, &PublicError{Code: Timeout}
	}
	if name == "" || len(name) > MaxProfileBytes || !utf8.ValidString(name) {
		return nil, models.Connection{}, &PublicError{Code: InvalidInput}
	}
	profiles, err := s.Profiles()
	if err != nil {
		return nil, models.Connection{}, err
	}
	profile, err := ResolveExactProfile(profiles, name)
	if err != nil {
		return nil, models.Connection{}, err
	}
	return s.openProfile(ctx, profile, required)
}

func (s InvocationService) openProfile(ctx context.Context, profile models.Connection, required models.CLIAccess) (*pg.MachineClient, models.Connection, error) {
	if ctx.Err() != nil {
		return nil, models.Connection{}, &PublicError{Code: Timeout}
	}
	if !profile.EffectiveCLIAccess().AllowsCLIAccess(required) {
		return nil, models.Connection{}, &PublicError{Code: PolicyDenied}
	}
	options := s.Options
	options.RequiredAccess = required
	options.CleanupTimeout = min(CleanupTimeout, CleanupBudget(ctx))
	if timeout, ok := ctx.Value(statementTimeoutKey{}).(time.Duration); ok {
		options.StatementTimeout = timeout
	}
	client, err := pg.OpenMachine(ctx, profile, options)
	if err != nil {
		return nil, models.Connection{}, PublicMachineError(err)
	}
	return client, profile, nil
}

func PublicMachineError(err error) *PublicError {
	var machine *pg.MachineError
	if errors.As(err, &machine) {
		code := ErrorCode(machine.Code)
		if _, ok := publicErrors[code]; ok {
			return &PublicError{Code: code, SQLState: machine.SQLState}
		}
	}
	return safeError(err)
}

func CloseMachine(ctx context.Context, client *pg.MachineClient) error {
	cleanup, cancel := context.WithTimeout(context.Background(), min(CleanupTimeout, CleanupBudget(ctx)))
	defer cancel()
	if err := client.Close(cleanup); err != nil {
		return PublicMachineError(err)
	}
	return nil
}
