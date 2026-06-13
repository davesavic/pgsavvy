package editor

import (
	"unicode"
)

// fzf-style fuzzy subsequence matching and scoring.
//
// Match reports whether pattern occurs as a case-insensitive subsequence of
// candidate, and if so returns a relevance score plus the RUNE offsets in
// candidate where each pattern rune was matched. Higher scores are better.
//
// The scorer favours, in roughly descending weight: contiguous runs, matches
// at word boundaries (start, after a separator, or a camelCase hump), and an
// overall prefix match. A quality floor discards subsequence matches that are
// too scattered to be useful so callers do not have to post-filter junk.

// Scoring weights. These are tuning constants with low blast radius; they only
// affect ranking order and the quality floor, never the public signature.
const (
	scoreMatch        = 16 // base award per matched rune
	scoreContiguous   = 16 // extra when the previous rune also matched
	scoreBoundary     = 18 // extra when the match lands on a word boundary
	scorePrefixBonus  = 12 // extra when the very first rune matches at offset 0
	penaltyGapLeading = 3  // penalty per skipped rune before the first match
	penaltyGap        = 1  // penalty per skipped rune between matches
)

// qualityFloorPerRune is the minimum average score-per-pattern-rune required
// for a multi-rune pattern to be accepted. Single contiguous boundary-aligned
// matches clear it easily; fully scattered matches across a long candidate do
// not. A 1-rune pattern is always accepted when it is a subsequence.
const qualityFloorPerRune = scoreMatch / 2

// Match performs an fzf-style fuzzy subsequence match of pattern against
// candidate.
//
//   - ok reports whether pattern is a (case-insensitive) subsequence of
//     candidate that also clears the quality floor.
//   - score is a relevance score, higher is better; 0 for the empty pattern.
//   - positions are the RUNE offsets into candidate of each matched pattern
//     rune, in order. nil for the empty pattern.
//
// Match("", x) returns exactly (true, 0, nil). Matching is Unicode-aware and
// iterates by rune, never by byte. Match is a pure function with no external
// dependencies.
func Match(pattern, candidate string) (ok bool, score int, positions []int) {
	pr := []rune(pattern)
	if len(pr) == 0 {
		return true, 0, nil
	}

	cr := []rune(candidate)
	if len(cr) == 0 {
		return false, 0, nil
	}

	pos := make([]int, 0, len(pr))
	total := 0
	pi := 0         // index into pattern runes
	prevMatch := -2 // candidate rune index of the previous match (-2 = none)
	gapPenalty := penaltyGapLeading

	for ci := 0; ci < len(cr) && pi < len(pr); ci++ {
		if !runeEqualFold(cr[ci], pr[pi]) {
			continue
		}

		award := scoreMatch

		if isBoundary(cr, ci) {
			award += scoreBoundary
		}
		if ci == prevMatch+1 {
			award += scoreContiguous
		}
		if ci == 0 && pi == 0 {
			award += scorePrefixBonus
		}

		// Penalise the run of skipped candidate runes since the last match.
		skipped := ci - prevMatch - 1
		if prevMatch == -2 {
			skipped = ci // leading skip from the start of the candidate
		}
		award -= skipped * gapPenalty

		total += award
		pos = append(pos, ci)
		prevMatch = ci
		pi++
		gapPenalty = penaltyGap
	}

	if pi != len(pr) {
		// pattern was not fully consumed: not a subsequence.
		return false, 0, nil
	}

	// The quality floor only filters multi-rune patterns; any single-rune
	// subsequence match is accepted (it cannot be "scattered").
	if len(pr) > 1 && total < len(pr)*qualityFloorPerRune {
		return false, 0, nil
	}

	return true, total, pos
}

// runeEqualFold reports whether a and b are equal under simple Unicode case
// folding.
func runeEqualFold(a, b rune) bool {
	if a == b {
		return true
	}
	return unicode.ToLower(a) == unicode.ToLower(b)
}

// isBoundary reports whether the candidate rune at index i begins a word: the
// first rune, a rune following a separator (underscore, space, dot, dash,
// slash), or a camelCase hump (lowercase/digit -> uppercase transition).
func isBoundary(cr []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := cr[i-1]
	if isSeparator(prev) {
		return true
	}
	cur := cr[i]
	return unicode.IsUpper(cur) && !unicode.IsUpper(prev)
}

// isSeparator reports whether r is a word-separating rune for boundary
// detection.
func isSeparator(r rune) bool {
	switch r {
	case '_', ' ', '.', '-', '/':
		return true
	default:
		return false
	}
}
