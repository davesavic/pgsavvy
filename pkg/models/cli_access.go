package models

import (
	"errors"

	"gopkg.in/yaml.v3"
)

// CLIAccess is an explicit opt-in for database operations outside the TUI.
type CLIAccess string

const (
	CLIAccessDisabled CLIAccess = "disabled"
	CLIAccessMetadata CLIAccess = "metadata"
	CLIAccessRowRead  CLIAccess = "row-read"
)

func (a CLIAccess) Validate() error {
	switch a {
	case "", CLIAccessDisabled, CLIAccessMetadata, CLIAccessRowRead:
		return nil
	default:
		return errors.New("invalid CLI access policy")
	}
}

func (a *CLIAccess) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("CLI access policy must be a string")
	}
	value := CLIAccess(node.Value)
	if err := value.Validate(); err != nil {
		return err
	}
	*a = value
	return nil
}

func (c Connection) EffectiveCLIAccess() CLIAccess {
	switch c.CLIAccess {
	case CLIAccessMetadata, CLIAccessRowRead:
		return c.CLIAccess
	default:
		return CLIAccessDisabled
	}
}
