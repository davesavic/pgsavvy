package models

// Ref is a schema-qualified table reference. Used wherever a target table
// must be identified across packages (PendingEditSet, FK navigation, etc.).
type Ref struct {
	Schema string
	Table  string
}
