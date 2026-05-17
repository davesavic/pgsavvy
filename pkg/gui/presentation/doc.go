// Package presentation supplies render-time helpers that derive border
// styling and header text from a *models.Connection, plus the closures
// the context layer consumes through ContextTreeDeps. Every helper reads
// theme.Current() fresh so theme hot-reload flows through without caching.
package presentation
