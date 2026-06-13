package editor

import "context"

// FunctionSourceName is the stable Name() for the function-aware
// completion source. Z1 wiring references this string when
// constructing the engine.
const FunctionSourceName = "functions"

// FunctionSourcePriority is the default Priority() the function source
// declares. Lower than schema (80) so a schema-aware match wins ties in
// Engine dedupe; higher than keywords/history so a function name wins
// over an identically-spelled keyword. It derives from the central
// FunctionSourceBias (completion_source.go) so the function rank lives
// in ONE place (dbsavvy-ko4m.3, Finding B4) — do not redeclare a
// separate 60 here.
const FunctionSourcePriority = FunctionSourceBias

// FunctionSource implements Source by reading FUNCTION-routine names from the
// background-warmed metadata snapshot (SchemaMetadata.FunctionNames),
// decorating each with a `<name>(...)` Display so the popup hints at
// callability.
//
// The names are populated eagerly per-connection by the SchemaWarmer; this
// source only READS the snapshot synchronously — no driver call, no cache of
// its own. Suggest never returns nil — callers can range freely. When the
// names have not been loaded yet (or there is no connection) Suggest returns an
// empty slice; the completion popup MUST NOT block.
type FunctionSource struct {
	priority int
	meta     SchemaMetadata
	// detail is the injected signature-help seam (dbsavvy-ko4m.5.3). Optional
	// and nil-safe: Suggest never touches it (the function-name candidates are
	// unchanged). It is held here so the selection-driven signature population in
	// ko4m.5.4 can resolve the chosen suggestion's signature via FunctionDetail /
	// WarmFunctionDetail without the editor importing controllers/helpers/data.
	detail FunctionDetailProvider
}

// NewFunctionSource constructs a FunctionSource over the metadata snapshot. A
// nil meta is tolerated — Suggest then returns an empty slice.
func NewFunctionSource(meta SchemaMetadata) *FunctionSource {
	return &FunctionSource{
		priority: FunctionSourcePriority,
		meta:     meta,
	}
}

// SetDetailProvider injects the function-signature-help provider seam
// (dbsavvy-ko4m.5.3). Optional and nil-safe: passing nil (or never calling this)
// leaves the source emitting the same function-name candidates with empty
// Signature — there is no visible behavior change until ko4m.5.4 consumes the
// provider. Wired from the orchestrator over ConnectHelper, mirroring the
// SchemaMetadata / TableWarmer injection.
func (s *FunctionSource) SetDetailProvider(p FunctionDetailProvider) {
	s.detail = p
}

// DetailProvider returns the injected signature-help provider, or nil when none
// was wired. ko4m.5.4 reads it to populate the selected suggestion's Signature.
func (s *FunctionSource) DetailProvider() FunctionDetailProvider {
	return s.detail
}

// Name implements Source.
func (s *FunctionSource) Name() string { return FunctionSourceName }

// Priority implements Source.
func (s *FunctionSource) Priority() int { return s.priority }

// Suggest implements Source. Returns the snapshot's function-name suggestions
// fuzzily filtered by the typed identifier prefix and read synchronously. Each
// surviving candidate carries a composite Score = matchQuality +
// FunctionSourceBias (>0) and the rune-offset Matches from the fuzzy matcher
// (ko4m.3.2 ranking contract; closes dbsavvy-ek4, where unscored Score=0
// functions sorted below every keyword). An empty prefix keeps every function
// at the baseline Score = FunctionSourceBias via the Match("", x) == (true, 0,
// nil) contract. Never returns nil.
func (s *FunctionSource) Suggest(_ context.Context, buf *Buffer, pos Position) []Suggestion {
	if s.meta == nil {
		return []Suggestion{}
	}
	prefix := identifierPrefixAt(buf, pos)
	names := s.meta.FunctionNames()
	out := make([]Suggestion, 0, len(names))
	for _, n := range names {
		if n == "" {
			continue
		}
		ok, quality, positions := Match(prefix, n)
		if !ok {
			continue
		}
		out = append(out, Suggestion{
			Text:    n,
			Display: n + "(...)",
			Source:  FunctionSourceName,
			Score:   quality + FunctionSourceBias,
			Matches: positions,
			Kind:    KindFunction,
			Detail:  "fn",
		})
	}
	return out
}
