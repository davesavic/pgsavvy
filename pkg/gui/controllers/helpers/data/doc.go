// Package data hosts data-access helpers that adapt drivers.Session into the
// shape the gui controllers consume. Today this is ConnectHelper (per-Session
// serialized call queue + Connection/Session lifecycle); future helpers (e.g.
// schema-cache, paged-result-tracker) live alongside it.
package data
