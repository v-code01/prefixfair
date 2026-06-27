// Package trace generates the multi-tenant Zipfian request trace the frontier is
// measured on. A trace is a sequence of requests whose tenants are drawn from a
// Zipf popularity distribution (a few heavy tenants, a long tail of light ones),
// where every request from a tenant shares one long system-prompt prefix and
// carries a varied suffix. That shape is what makes the cache-hit / fairness
// trade-off real: the shared prefix is what a cache-aware router reuses KV on, and
// the skewed popularity is what a fair scheduler has to equalize against.
//
// Two honesty properties are load-bearing and enforced by tests:
//
//   - Not tautological. The generator draws tenants from a principled Zipf
//     distribution with a seed; it is never hand-tuned to manufacture a knee. The
//     frontier is reported on held-out seeds (HeldOutSeeds) that are disjoint from
//     any seed one might tune a policy on.
//
//   - Within the context budget. Every prefix is width-stable (its token length
//     does not depend on the seed), so a trace realization can never silently
//     drift a prompt over the per-slot context budget and get served zero tokens.
//     PrefixSentences is chosen so prefix plus suffix plus generation stays under
//     the fleet's per-slot context.
package trace

import (
	"fmt"
	"math/rand"

	"prefixfair/internal/worker"
)

// Request is one trace entry. Prefix is the tenant's shared system prompt (the
// long, cacheable head every one of that tenant's requests repeats); Suffix is the
// short per-request tail that varies; Prompt is the full text sent to a backend
// (Prefix, a separator, then Suffix). TenantIndex is the tenant's Zipf rank (0 is
// the most popular), and Tenant is its stable string id used for fairness
// accounting.
type Request struct {
	Tenant      string
	TenantIndex int
	Prefix      string
	Suffix      string
	Prompt      string
}

// Spec parameterizes a trace. Tenants is the number of distinct clients; ZipfS is
// the Zipf skew exponent (larger means a steeper head, so a few tenants dominate;
// must be > 1); Requests is the total number of entries in the trace (the
// backlogged request pool the frontier replays); PrefixSentences sets the shared
// prefix length (about 21 tokens per sentence, so ~64 gives a ~1.3k-token prefix
// well inside a 8k per-slot budget). Seed makes the whole trace reproducible.
type Spec struct {
	Tenants         int
	ZipfS           float64
	Requests        int
	PrefixSentences int
	Seed            int64
}

// DefaultSpec is a principled starting point: 8 tenants, a moderate Zipf skew
// (s=1.1, a realistic head-heavy but not degenerate popularity), 600 requests, and
// a ~1.3k-token shared prefix. These are workload parameters, not policy tuning:
// no routing policy's constants are fit to them.
func DefaultSpec() Spec {
	return Spec{
		Tenants:         8,
		ZipfS:           1.1,
		Requests:        600,
		PrefixSentences: 64,
		Seed:            1,
	}
}

// Generate builds the trace described by spec. Requests are returned in arrival
// order with tenants drawn from the Zipf distribution, so replaying them FIFO
// reproduces a realistic head-heavy arrival stream. The result is a pure function
// of spec: the same spec (same seed) always yields the identical trace.
//
// Each tenant is assigned one width-stable shared prefix, distinct per tenant and
// per seed, so different seeds are independent trace realizations (their prefixes
// do not overlap, which also means replaying two seeds back to back on one warm
// fleet cannot leak a cache hit from one into the other).
func Generate(spec Spec) []Request {
	spec = spec.withDefaults()

	prng := rand.New(rand.NewSource(spec.Seed))
	prefixes := tenantPrefixes(prng, spec.Tenants, spec.PrefixSentences)

	// Zipf over tenant ranks: value k in [0, Tenants-1] with P(k) proportional to
	// 1/(k+1)^s, so tenant 0 is the most popular and popularity decays with rank.
	// v=1 anchors the head at rank 0.
	zipf := rand.NewZipf(prng, spec.ZipfS, 1, uint64(spec.Tenants-1))

	reqs := make([]Request, spec.Requests)
	for i := range reqs {
		k := int(zipf.Uint64())
		reqs[i] = Request{
			Tenant:      TenantID(k),
			TenantIndex: k,
			Prefix:      prefixes[k],
			Suffix:      fmt.Sprintf("Request %d: summarize the passage above in one sentence.", i),
			Prompt:      prefixes[k] + " " + fmt.Sprintf("Request %d: summarize the passage above in one sentence.", i),
		}
	}
	return reqs
}

// TenantID is the stable string id for a tenant of the given Zipf rank.
func TenantID(k int) string { return fmt.Sprintf("t%03d", k) }

// tenantPrefixes assigns each tenant one distinct, width-stable shared prefix.
// Prefix seeds are drawn distinct so no two tenants collide onto the same prefix;
// width stability (fixed 9-digit sentence ids) keeps every prefix the same token
// length regardless of which seed produced it.
func tenantPrefixes(prng *rand.Rand, tenants, sentences int) []string {
	// idModulus/1000 keeps each prefix seed small enough that WidthStablePrefix's
	// embedded id never wraps, so distinct seeds give genuinely distinct prefixes.
	const seedSpace = 1_000_000
	used := make(map[int]bool, tenants)
	out := make([]string, tenants)
	for k := 0; k < tenants; k++ {
		s := 0
		for {
			s = prng.Intn(seedSpace) + 1 // avoid 0, the worker's canonical shared prefix
			if !used[s] {
				used[s] = true
				break
			}
		}
		out[k] = worker.WidthStablePrefix(sentences, s)
	}
	return out
}

// withDefaults fills any unset field from DefaultSpec so a partially specified
// Spec is still valid, and clamps Tenants to at least 2 (a gap needs two tenants).
func (s Spec) withDefaults() Spec {
	d := DefaultSpec()
	if s.Tenants < 2 {
		s.Tenants = d.Tenants
	}
	if s.ZipfS <= 1 {
		s.ZipfS = d.ZipfS
	}
	if s.Requests < 1 {
		s.Requests = d.Requests
	}
	if s.PrefixSentences < 1 {
		s.PrefixSentences = d.PrefixSentences
	}
	return s
}

// HeldOutSeeds returns n reproducible trace seeds reserved for evaluation. They sit
// in a high, distinctive range so they are visibly disjoint from any small seed a
// policy might be tuned on: the frontier is measured on these, unseen during any
// tuning, which is the honesty condition against a hand-tuned trace.
func HeldOutSeeds(n int) []int64 {
	const base = 900_000_000
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(base + i)
	}
	return out
}
