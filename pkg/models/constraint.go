package models

// Constraint describes a check, unique, primary-key or foreign-key constraint.
type Constraint struct {
	Name       string
	Schema     string
	Table      string
	Kind       string
	Columns    []string
	Definition string
}
