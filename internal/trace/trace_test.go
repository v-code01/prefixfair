package trace

import (
	"strings"
	"testing"
)

// TestZipfianPopularitySkew is the anti-tautology anchor for the trace: the tenant
// mix must be genuinely head-heavy (a principled Zipf), not uniform and not
// hand-placed. It asserts the head dominates the tail and popularity falls with
// rank, which is the skew a fair scheduler has to work against.
func TestZipfianPopularitySkew(t *testing.T) {
	spec := Spec{Tenants: 8, ZipfS: 1.1, Requests: 20000, PrefixSentences: 8, Seed: 42}
	reqs := Generate(spec)

	counts := make([]int, spec.Tenants)
	for _, r := range reqs {
		counts[r.TenantIndex]++
	}

	// The most popular tenant is rank 0 and must dominate the least popular.
	if counts[0] <= counts[spec.Tenants-1] {
		t.Fatalf("not skewed: head tenant %d <= tail tenant %d", counts[0], counts[spec.Tenants-1])
	}
	// A moderate Zipf head should be several times the tail; require at least 5x so
	// a near-uniform draw fails this test.
	if got := float64(counts[0]) / float64(counts[spec.Tenants-1]); got < 5 {
		t.Fatalf("head/tail ratio %.1f too flat to be Zipfian", got)
	}
	// The head must hold a large share of all traffic (a real popularity skew), but
	// not everything (the tail still appears, so fairness is actually contested).
	headShare := float64(counts[0]) / float64(len(reqs))
	if headShare < 0.25 || headShare > 0.95 {
		t.Fatalf("head share %.2f outside a realistic Zipf band", headShare)
	}
	// Every tenant must appear at least once, or the tail is not really contested.
	for k, c := range counts {
		if c == 0 {
			t.Fatalf("tenant %d never appeared; tail is empty", k)
		}
	}
}

// TestReproducible pins that a trace is a pure function of its spec: same seed,
// identical trace; different seed, a different trace. Held-out CIs depend on this.
func TestReproducible(t *testing.T) {
	spec := Spec{Tenants: 6, ZipfS: 1.2, Requests: 500, PrefixSentences: 8, Seed: 7}
	a := Generate(spec)
	b := Generate(spec)
	if len(a) != len(b) {
		t.Fatalf("length differs across identical specs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("request %d differs across identical specs", i)
		}
	}

	spec.Seed = 8
	c := Generate(spec)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatalf("different seeds produced identical traces")
	}
}

// TestPrefixSharedWithinTenantDistinctAcross is the cache-realness precondition: a
// cache-aware router can only reuse KV if a tenant's requests really do share one
// prefix, and cross-tenant spread is only real if tenants have different prefixes.
func TestPrefixSharedWithinTenantDistinctAcross(t *testing.T) {
	spec := Spec{Tenants: 8, ZipfS: 1.1, Requests: 3000, PrefixSentences: 16, Seed: 3}
	reqs := Generate(spec)

	perTenant := make(map[int]string)
	for _, r := range reqs {
		if prev, ok := perTenant[r.TenantIndex]; ok {
			if prev != r.Prefix {
				t.Fatalf("tenant %d has two different prefixes; within-tenant sharing broken", r.TenantIndex)
			}
		} else {
			perTenant[r.TenantIndex] = r.Prefix
		}
		// The full prompt must begin with the shared prefix and carry the varied
		// suffix, so the cacheable head really is a prefix of what the server sees.
		if !strings.HasPrefix(r.Prompt, r.Prefix) {
			t.Fatalf("tenant %d prompt does not begin with its shared prefix", r.TenantIndex)
		}
		if !strings.Contains(r.Prompt, r.Suffix) {
			t.Fatalf("tenant %d prompt does not contain its suffix", r.TenantIndex)
		}
	}

	// Distinct tenants must have distinct prefixes.
	seen := make(map[string]int)
	for k, p := range perTenant {
		if other, ok := seen[p]; ok {
			t.Fatalf("tenants %d and %d share a prefix; cross-tenant distinctness broken", other, k)
		}
		seen[p] = k
	}
}

// TestWidthStablePrefixes guards the context budget structurally: because every
// prefix is width-stable, all tenant prefixes have the identical byte length
// regardless of seed, so no seed can silently produce a longer prompt that
// overflows the per-slot context. (The real token-budget check is the real-server
// test.)
func TestWidthStablePrefixes(t *testing.T) {
	spec := Spec{Tenants: 8, ZipfS: 1.1, Requests: 2000, PrefixSentences: 32, Seed: 5}
	reqs := Generate(spec)

	var wantLen int
	first := true
	for _, r := range reqs {
		if first {
			wantLen = len(r.Prefix)
			first = false
			continue
		}
		if len(r.Prefix) != wantLen {
			t.Fatalf("prefix byte length varies (%d vs %d); width stability broken", len(r.Prefix), wantLen)
		}
	}

	// A different seed must still yield the same width, so seed choice can never
	// change how much context a prompt consumes.
	spec.Seed = 999
	other := Generate(spec)
	if len(other[0].Prefix) != wantLen {
		t.Fatalf("prefix width changed across seeds: %d vs %d", len(other[0].Prefix), wantLen)
	}
}

// TestHeldOutSeedsDisjointFromTuning pins that the evaluation seeds sit in a high,
// distinctive range clearly separated from any small seed a policy would be tuned
// on, which is what makes "reported on held-out seeds" verifiable.
func TestHeldOutSeedsDisjoint(t *testing.T) {
	seeds := HeldOutSeeds(20)
	if len(seeds) != 20 {
		t.Fatalf("want 20 held-out seeds, got %d", len(seeds))
	}
	seen := make(map[int64]bool)
	for _, s := range seeds {
		if s < 900_000_000 {
			t.Fatalf("held-out seed %d is not in the reserved high range", s)
		}
		if seen[s] {
			t.Fatalf("held-out seeds are not distinct: %d repeats", s)
		}
		seen[s] = true
	}
}

// TestDefaultsFillPartialSpec confirms a partially specified Spec is completed from
// defaults rather than producing a degenerate (single-tenant, empty) trace.
func TestDefaultsFillPartialSpec(t *testing.T) {
	reqs := Generate(Spec{Seed: 2})
	if len(reqs) == 0 {
		t.Fatal("empty trace from defaulted spec")
	}
	tenants := make(map[int]bool)
	for _, r := range reqs {
		tenants[r.TenantIndex] = true
	}
	if len(tenants) < 2 {
		t.Fatalf("defaulted trace has %d tenants; need at least 2", len(tenants))
	}
}
