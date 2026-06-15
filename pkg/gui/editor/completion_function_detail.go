package editor

import "github.com/davesavic/pgsavvy/pkg/models"

// FunctionDetailProvider is the synchronous-read + async-warm surface the
// completion path queries for a SELECTED function's signature detail. It is the
// dependency-inversion seam that keeps the editor package
// free of a pkg/gui/controllers/helpers/data import: the editor declares this
// interface and the orchestrator injects the concrete ConnectHelper (whose
// FunctionDetail / WarmFunctionDetail methods satisfy it structurally), exactly
// as SchemaMetadata / TableWarmer are injected for schema completion.
//
// Shape rationale: an interface (not a func-struct) mirrors SchemaMetadata and
// TableWarmer in completion_metadata.go, and the connectHelper already exposes
// methods with these exact signatures, so the concrete type satisfies the
// interface with no adapter.
//
// FunctionDetail is a pure, race-safe in-memory read: NO driver round-trip, NO
// blocking. (details,false) on a miss distinguishes an unloaded entry from a
// loaded-but-empty one so the caller can fire a reactive warm.
//
// WarmFunctionDetail is the non-blocking lazy-load trigger: it schedules a
// background load (idempotent per key) and invokes onReady on the UI loop once
// the cache is populated. A nil onReady is tolerated (the load still warms the
// cache). Under in-flight dedup a second concurrent warm for the same key may
// NOT register its onReady — only the first caller's callback is guaranteed to
// fire (see ConnectHelper.WarmFunctionDetail), so consumers re-read via
// FunctionDetail after any warm landing rather than relying on their specific
// onReady.
//
// This interface only supplies the seam; signature population for the selected
// suggestion via this provider is implemented elsewhere.
type FunctionDetailProvider interface {
	FunctionDetail(schema, name string) ([]models.FunctionDetail, bool)
	WarmFunctionDetail(schema, name string, onReady func())
}
