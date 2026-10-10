package models

import (
	"errors"
	"slices"
)

var (
	ErrEditIdentityMismatch   = errors.New("pending edit: physical row identity changed; settle existing edits before using another key")
	ErrEditIdentityUnresolved = errors.New("pending edit: physical row identity is unresolved")
)

type IdentityColumn struct {
	Attnum uint16
	Name   string
}

// EditIdentity fixes the physical meaning and order of staged identity values.
type EditIdentity struct {
	RelationOID uint32
	Columns     []IdentityColumn
}

func (id EditIdentity) Clone() EditIdentity {
	id.Columns = slices.Clone(id.Columns)
	return id
}

func (id EditIdentity) Names() []string {
	names := make([]string, len(id.Columns))
	for i, col := range id.Columns {
		names[i] = col.Name
	}
	return names
}

func (id EditIdentity) Validate() error {
	if id.RelationOID == 0 || len(id.Columns) == 0 {
		return ErrEditIdentityUnresolved
	}
	for i, col := range id.Columns {
		if col.Attnum <= 0 || col.Name == "" {
			return ErrEditIdentityUnresolved
		}
		for _, prior := range id.Columns[:i] {
			if prior.Attnum == col.Attnum || prior.Name == col.Name {
				return ErrEditIdentityUnresolved
			}
		}
	}
	return nil
}

func (id EditIdentity) Compatible(other EditIdentity) bool {
	if id.Validate() != nil || other.Validate() != nil || id.RelationOID != other.RelationOID || len(id.Columns) != len(other.Columns) {
		return false
	}
	for _, col := range id.Columns {
		if !slices.Contains(other.Columns, col) {
			return false
		}
	}
	return true
}

func (id EditIdentity) Normalize(other EditIdentity, values []any) ([]any, error) {
	if !id.Compatible(other) || len(values) != len(other.Columns) {
		return nil, ErrEditIdentityMismatch
	}
	out := make([]any, len(id.Columns))
	for i, col := range id.Columns {
		out[i] = values[slices.Index(other.Columns, col)]
	}
	return out, nil
}
