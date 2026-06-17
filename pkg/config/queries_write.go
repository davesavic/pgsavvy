package config

import (
	"errors"
	"strings"

	"github.com/spf13/afero"

	"github.com/davesavic/pgsavvy/pkg/models"
	"github.com/davesavic/pgsavvy/pkg/utils"
)

// ErrDuplicateQueryName is returned by AppendQuery when the new query's
// (trimmed) Name collides with an existing entry. Callers use errors.Is to
// detect the condition and prompt for a different name.
var ErrDuplicateQueryName = errors.New("config: duplicate query name")

// SaveQueries atomically writes qs to the YAML file at path using the wrapper
// form {queries: [...]} that LoadQueries expects. The file is written via
// pkg/utils.AtomicWriteYAML — temp file at path+".tmp", rename onto path,
// mode 0600, parent dir 0700.
func SaveQueries(fs afero.Fs, path string, qs []models.SavedQuery) error {
	wrapper := queriesFile{Queries: qs}
	return utils.AtomicWriteYAML(fs, path, wrapper, 0o600)
}

// AppendQuery loads the existing queries.yml at path (treating a missing file
// as an empty list), rejects a duplicate (trimmed) Name with
// ErrDuplicateQueryName, appends q, and writes the result via SaveQueries.
//
// On LoadQueries failure the error is returned unwrapped.
func AppendQuery(fs afero.Fs, path string, q models.SavedQuery) error {
	existing, err := LoadQueries(fs, path)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(q.Name)
	for i := range existing {
		if strings.TrimSpace(existing[i].Name) == key {
			return ErrDuplicateQueryName
		}
	}
	existing = append(existing, q)
	return SaveQueries(fs, path, existing)
}

// UpsertQuery loads the existing queries.yml at path, replacing in place the
// single entry whose (trimmed) Name matches q.Name — preserving order and
// count — or appending q when no match exists. Writes the result via
// SaveQueries.
//
// On LoadQueries failure the error is returned unwrapped.
func UpsertQuery(fs afero.Fs, path string, q models.SavedQuery) error {
	existing, err := LoadQueries(fs, path)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(q.Name)
	for i := range existing {
		if strings.TrimSpace(existing[i].Name) != key {
			continue
		}
		existing[i] = q
		return SaveQueries(fs, path, existing)
	}
	existing = append(existing, q)
	return SaveQueries(fs, path, existing)
}

// DeleteQuery loads the existing queries.yml at path and removes the single
// entry whose (trimmed) Name matches name, writing the result via SaveQueries.
// An absent name is a no-op and returns nil (no error). Deleting the last
// entry yields a valid {queries: []} file.
//
// On LoadQueries failure the error is returned unwrapped.
func DeleteQuery(fs afero.Fs, path, name string) error {
	existing, err := LoadQueries(fs, path)
	if err != nil {
		return err
	}
	key := strings.TrimSpace(name)
	for i := range existing {
		if strings.TrimSpace(existing[i].Name) != key {
			continue
		}
		existing = append(existing[:i], existing[i+1:]...)
		return SaveQueries(fs, path, existing)
	}
	return nil
}
