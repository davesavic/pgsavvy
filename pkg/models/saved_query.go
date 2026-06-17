package models

// SavedQuery is a named, hand-editable SQL snippet persisted to queries.yml.
// It is plain data; storage lives in the config layer. Name is the uniqueness
// key (trimmed before comparison by the config writers).
type SavedQuery struct {
	Name string `yaml:"name"`
	SQL  string `yaml:"sql"`
}
