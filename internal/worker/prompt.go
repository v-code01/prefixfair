package worker

import (
	"fmt"
	"strings"
)

// idModulus bounds every embedded id below 1e9 so the fixed 9-digit format never
// overflows its width. Keeping the width fixed is what makes token length
// independent of the seed.
const idModulus = 1_000_000_000

// WidthStablePrefix builds a prefix of n sentences whose token length is
// constant regardless of seed. Each sentence embeds a fixed 9-digit id, so
// seed 0 (a shared hit prefix) and any other seed (a distinct miss prefix)
// tokenize to the SAME length. The only variable between them is whether the KV
// cache is reused, not how much text there is to prefill.
//
// This directly answers the spike's near-demote bug: a seed-dependent width let
// larger seeds overflow the per-slot context and get silently served zero
// tokens. Fixing the width keeps every generated prompt inside the per-slot
// budget and makes hit-vs-miss a clean controlled comparison.
//
// Distinct seeds diverge from the first sentence, so no two seeds share a
// cacheable prefix.
func WidthStablePrefix(n, seed int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		id := (seed*1000 + i) % idModulus
		fmt.Fprintf(&b, "The quick brown fox jumps over the lazy dog number %09d.", id)
	}
	return b.String()
}

// HitPrompt is the shared prefix (seed 0) with a short varied suffix. Repeated
// hit prompts share the whole prefix, so a warm slot skips its prefill.
func HitPrompt(prefixSentences, suffix int) string {
	return fmt.Sprintf("%s Question %d: what is %d plus %d?",
		WidthStablePrefix(prefixSentences, 0), suffix, suffix, suffix+1)
}

// MissPrompt is a distinct prefix (seed > 0) with a short suffix. Each miss
// prompt has a unique prefix, so nothing is reusable and the server prefills it
// in full. seed must be > 0 to stay distinct from the shared hit prefix.
func MissPrompt(prefixSentences, seed int) string {
	return fmt.Sprintf("%s Question: what is %d plus %d?",
		WidthStablePrefix(prefixSentences, seed), seed, seed+1)
}
