package editor

import (
	"context"
	"sync"

	"github.com/davesavic/dbsavvy/pkg/drivers"
)

// FunctionSourceName is the stable Name() for the function-aware
// completion source. Z1 wiring references this string when
// constructing the engine.
const FunctionSourceName = "functions"

// FunctionSourcePriority is the default Priority() the function source
// declares. Lower than schema (which is 80) so a schema-aware match
// wins ties in Engine dedupe; higher than keywords/history so a
// function name wins over an identically-spelled keyword. The exact
// value is not load-bearing for C3 — Z1 may rewire from a central
// registry.
const FunctionSourcePriority = 60

// FunctionSource implements Source by returning the names of FUNCTION
// routines visible to the active drivers.Session, decorated with a
// `<name>(...)` Display so the popup hints at callability.
//
// The list is fetched lazily on the first Suggest call (per active
// session) and cached in-memory. Subsequent Suggest calls reuse the
// cache without re-querying. When the underlying session changes
// (e.g. reconnect or session reset) the cache is dropped and the next
// Suggest re-fetches against the new session.
//
// Suggest never returns nil — callers can range freely. Errors from
// the loader are swallowed and surface as an empty slice; the
// completion popup MUST NOT block on driver failures.
type FunctionSource struct {
	priority int
	session  SessionProvider

	mu         sync.Mutex
	cached     []Suggestion
	cachedSess drivers.Session // identity of the session the cache was built against
	hasCache   bool
}

// NewFunctionSource constructs a FunctionSource. The session provider
// may be nil — a nil provider is treated as "no active session" which
// causes Suggest to return an empty slice.
func NewFunctionSource(session SessionProvider) *FunctionSource {
	return &FunctionSource{
		priority: FunctionSourcePriority,
		session:  session,
	}
}

// Name implements Source.
func (s *FunctionSource) Name() string { return FunctionSourceName }

// Priority implements Source.
func (s *FunctionSource) Priority() int { return s.priority }

// Suggest implements Source. Returns the cached function-name
// suggestions for the active session, fetching them on the first call
// (or after a session swap). The active session is resolved via the
// SessionProvider every call so reconnects invalidate the cache
// automatically.
func (s *FunctionSource) Suggest(ctx context.Context, _ *Buffer, _ Position) []Suggestion {
	sess := s.activeSession()
	if sess == nil {
		return []Suggestion{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hasCache && s.cachedSess == sess {
		return s.cached
	}
	// Either no cache yet or the session pointer changed — re-fetch.
	names, err := sess.ListFunctions(ctx)
	if err != nil {
		// Don't poison the cache on error; next Suggest will retry.
		return []Suggestion{}
	}
	out := make([]Suggestion, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		out = append(out, Suggestion{
			Text:    n,
			Display: n + "(...)",
			Source:  FunctionSourceName,
		})
	}
	s.cached = out
	s.cachedSess = sess
	s.hasCache = true
	return s.cached
}

// activeSession safely calls the SessionProvider; nil provider returns
// nil.
func (s *FunctionSource) activeSession() drivers.Session {
	if s.session == nil {
		return nil
	}
	return s.session()
}
