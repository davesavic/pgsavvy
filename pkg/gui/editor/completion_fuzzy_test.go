package editor

import (
	"fmt"
	"testing"
)

func TestMatch_Subsequence(t *testing.T) {
	ok, score, pos := Match("usr", "user_sessions")
	if !ok {
		t.Fatalf("Match(usr, user_sessions): want ok=true, got false")
	}
	if score <= 0 {
		t.Errorf("score: want >0, got %d", score)
	}
	// positions must be valid rune indices into the candidate, ascending,
	// and each must fold-equal the corresponding pattern rune.
	pat := []rune("usr")
	cand := []rune("user_sessions")
	if len(pos) != len(pat) {
		t.Fatalf("positions length: want %d, got %d (%v)", len(pat), len(pos), pos)
	}
	for i, p := range pos {
		if p < 0 || p >= len(cand) {
			t.Errorf("position %d out of range: %d", i, p)
		}
		if i > 0 && p <= pos[i-1] {
			t.Errorf("positions not ascending: %v", pos)
		}
		if !runeEqualFold(cand[p], pat[i]) {
			t.Errorf("position %d (rune %q) does not match pattern rune %q", p, cand[p], pat[i])
		}
	}
}

func TestMatch_WordBoundaries(t *testing.T) {
	ok, _, pos := Match("oeml", "order_email")
	if !ok {
		t.Fatalf("Match(oeml, order_email): want ok=true, got false")
	}
	cand := []rune("order_email")
	pat := []rune("oeml")
	for i, p := range pos {
		if !runeEqualFold(cand[p], pat[i]) {
			t.Errorf("position %d (rune %q) does not match pattern rune %q", p, cand[p], pat[i])
		}
	}
}

func TestMatch_EmptyPattern(t *testing.T) {
	for _, cand := range []string{"", "anything", "user_sessions"} {
		ok, score, pos := Match("", cand)
		if !ok || score != 0 || pos != nil {
			t.Errorf("Match(\"\", %q) = (%v, %d, %v); want (true, 0, nil)", cand, ok, score, pos)
		}
	}
}

func TestMatch_EmptyCandidate(t *testing.T) {
	ok, score, pos := Match("usr", "")
	if ok || score != 0 || pos != nil {
		t.Errorf("Match(usr, \"\") = (%v, %d, %v); want (false, 0, nil)", ok, score, pos)
	}
}

func TestMatch_OneSharedChar(t *testing.T) {
	// A candidate sharing only a single char with a multi-char pattern is not
	// even a subsequence -> ok=false.
	ok, _, _ := Match("xqz", "user_sessions") // only 's' shared, not a subsequence anyway
	if ok {
		t.Errorf("Match(xqz, user_sessions): want ok=false")
	}
	ok2, _, _ := Match("ab", "a") // pattern longer than candidate
	if ok2 {
		t.Errorf("Match(ab, a): want ok=false")
	}
}

func TestMatch_QualityFloor(t *testing.T) {
	// "az" is a subsequence of "abracadabra" (a@0 ... z? no z). Build a case
	// that IS a subsequence but scattered with no contiguity/boundary so the
	// floor rejects it. Pattern "ad" in "xaxxxxxxxxxxd": a and d are far apart,
	// mid-word, with heavy gap penalties.
	scattered := "xaxxxxxxxxxxxxxxxxxxxxd"
	okMid, scoreMid, _ := Match("ad", scattered)
	if okMid {
		t.Errorf("Match(ad, %q) = ok=true (score=%d); want ok=false (quality floor)", scattered, scoreMid)
	}

	// Sanity: the same pattern against a tight, boundary-aligned candidate
	// clears the floor.
	okTight, _, _ := Match("ad", "a_d")
	if !okTight {
		t.Errorf("Match(ad, a_d): want ok=true")
	}
}

func TestMatch_CaseInsensitive(t *testing.T) {
	okLower, _, posLower := Match("usr", "user_sessions")
	okUpper, _, posUpper := Match("USR", "user_sessions")
	if okLower != okUpper {
		t.Errorf("case sensitivity mismatch: lower ok=%v, upper ok=%v", okLower, okUpper)
	}
	if len(posLower) != len(posUpper) {
		t.Fatalf("position length differs by case: %v vs %v", posLower, posUpper)
	}
	for i := range posLower {
		if posLower[i] != posUpper[i] {
			t.Errorf("positions differ by case at %d: %v vs %v", i, posLower, posUpper)
		}
	}

	// Mixed case in both pattern and candidate.
	ok, _, _ := Match("OeM", "Order_Email")
	if !ok {
		t.Errorf("Match(OeM, Order_Email): want ok=true")
	}
}

func TestMatch_NonASCIIRuneOffsets(t *testing.T) {
	// "café" is 4 runes but 5 bytes ('é' is 2 bytes). Matching "cé" must
	// return rune offsets 0 and 3, NOT byte offsets.
	ok, _, pos := Match("cé", "café")
	if !ok {
		t.Fatalf("Match(cé, café): want ok=true")
	}
	want := []int{0, 3}
	if len(pos) != len(want) {
		t.Fatalf("positions: want %v, got %v", want, pos)
	}
	for i := range want {
		if pos[i] != want[i] {
			t.Errorf("position %d: want %d (rune offset), got %d", i, want[i], pos[i])
		}
	}

	// Match the accented rune itself, case-insensitively.
	ok2, _, pos2 := Match("É", "café")
	if !ok2 {
		t.Fatalf("Match(É, café): want ok=true (case-insensitive)")
	}
	if len(pos2) != 1 || pos2[0] != 3 {
		t.Errorf("É position: want [3], got %v", pos2)
	}
}

func TestMatch_OutOfOrder(t *testing.T) {
	// "rsu" is not a subsequence of "user_sessions" (order matters).
	ok, _, _ := Match("rsu", "user_sessions")
	if ok {
		t.Errorf("Match(rsu, user_sessions): want ok=false (out of order)")
	}
}

// --- Benchmarks -------------------------------------------------------------

// genCandidates builds n pseudo-realistic identifier-like candidates without
// any randomness so benchmarks are reproducible.
func genCandidates(n int) []string {
	prefixes := []string{"user", "order", "product", "account", "session", "payment", "invoice", "customer"}
	suffixes := []string{"id", "email", "name", "created_at", "updated_at", "status", "total", "metadata"}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		p := prefixes[i%len(prefixes)]
		s := suffixes[(i/len(prefixes))%len(suffixes)]
		out = append(out, fmt.Sprintf("%s_%s_%d", p, s, i))
	}
	return out
}

// BenchmarkMatch runs a 5-rune pattern over 2000 candidates per op. The AC
// requires < 5ms/op.
func BenchmarkMatch(b *testing.B) {
	cands := genCandidates(2000)
	const pattern = "usrem" // 5 runes
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range cands {
			_, _, _ = Match(pattern, c)
		}
	}
}

// BenchmarkMatch10k mirrors the downstream perf gate: full match+score
// over ~10k candidates within one frame budget (~8ms p99, no debounce).
func BenchmarkMatch10k(b *testing.B) {
	cands := genCandidates(10000)
	const pattern = "usrem" // 5 runes
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, c := range cands {
			_, _, _ = Match(pattern, c)
		}
	}
}
