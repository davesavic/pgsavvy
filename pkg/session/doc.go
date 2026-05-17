// Package session resolves runtime credentials for database connection
// profiles and (in subsequent tasks) builds engine-specific connection
// configs.
//
// This package currently provides the credentials waterfall used to obtain
// a Postgres password at dial time. The waterfall is documented on
// ResolvePassword and follows epic dbsavvy-921 task 921.4 and DESIGN.md
// §11.2.
package session
