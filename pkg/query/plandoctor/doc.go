// Package plandoctor is a pure, offline EXPLAIN-plan insight engine. It takes a
// parsed *models.Plan and returns ranked, deterministic Findings describing
// likely performance problems (bad estimates, selective seq scans, nested-loop
// blowups, ...).
//
// It is intentionally dependency-free beyond pkg/models and the standard
// library: NO LLM, NO catalog lookups, NO GUI. Every heuristic threshold is a
// documented constant in this package so the analysis is reproducible and
// reviewable. The TUI (T6) consumes Findings; this package never renders.
package plandoctor
