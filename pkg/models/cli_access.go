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
	CLIAccessFull     CLIAccess = "full"
)

func (a CLIAccess) Validate() error {
	switch a {
	case "", CLIAccessDisabled, CLIAccessMetadata, CLIAccessRowRead, CLIAccessFull:
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
	case CLIAccessMetadata, CLIAccessRowRead, CLIAccessFull:
		return c.CLIAccess
	default:
		return CLIAccessDisabled
	}
}

// AllowsCLIAccess treats an empty requirement as any enabled CLI capability.
// Disabled and unknown policies or requirements never grant access.
func (a CLIAccess) AllowsCLIAccess(required CLIAccess) bool {
	switch required {
	case "", CLIAccessMetadata:
		return a == CLIAccessMetadata || a == CLIAccessRowRead || a == CLIAccessFull
	case CLIAccessRowRead:
		return a == CLIAccessRowRead || a == CLIAccessFull
	case CLIAccessFull:
		return a == CLIAccessFull
	default:
		return false
	}
}
