package models

// Database describes a single database catalog on a server.
type Database struct {
	Name     string
	Owner    string
	Encoding string
}
