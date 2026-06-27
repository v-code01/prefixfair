package router

import (
	"fmt"
	"math/rand"
	"testing"
)

// mockFleet is a fixed fleet-state view for routing unit tests: N backends with
// caller-controlled per-backend load. It lets the policies be exercised as pure
// routing logic with no llama-server in sight.
type mockFleet struct{ loads []int }

func (m *mockFleet) N() int                { return len(m.loads) }
func (m *mockFleet) Load(i int) int        { return m.loads[i] }
func newMockFleet(loads ...int) *mockFleet { return &mockFleet{loads: loads} }

// --- consistent-hash: sticky, cache-friendly, load- and fairness-blind ---

func TestConsistentHashIsStickyAndLoadBlind(t *testing.T) {
	ch := NewConsistentHash(4, 0)
	prefix := "tenant-system-prompt-A"

	// Sticky: the same prefix routes to the same backend every time.
	home := ch.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0))
	for i := 0; i < 100; i++ {
		if got := ch.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0)); got != home {
			t.Fatalf("consistent-hash not sticky: got %d want %d", got, home)
		}
	}
	// Load-blind: even when the home is buried under load, it still routes there.
	buried := make([]int, 4)
	buried[home] = 1_000_000
	if got := ch.Route(Request{Prefix: prefix}, &mockFleet{loads: buried}); got != home {
		t.Fatalf("consistent-hash should ignore load: got %d want %d", got, home)
	}
}

func TestConsistentHashSpreadsDistinctPrefixes(t *testing.T) {
	ch := NewConsistentHash(4, 0)
	used := map[int]bool{}
	for i := 0; i < 200; i++ {
		b := ch.Route(Request{Prefix: fmt.Sprintf("prefix-%d", i)}, newMockFleet(0, 0, 0, 0))
		used[b] = true
	}
	if len(used) != 4 {
		t.Fatalf("distinct prefixes should spread across all 4 backends, used %d", len(used))
	}
}

// --- JSQ(d): join shortest of d sampled queues ---

func TestJSQdWithFullSamplePicksGlobalMin(t *testing.T) {
	// d >= N samples every backend, so it must return the unique least-loaded one.
	j := NewJSQd(4, rand.New(rand.NewSource(1)))
	fleet := newMockFleet(5, 1, 3, 4)
	for i := 0; i < 1000; i++ {
		if got := j.Route(Request{}, fleet); got != 1 {
			t.Fatalf("JSQ(d=N) must pick global min index 1, got %d", got)
		}
	}
}

func TestJSQdPicksShorterQueuesThanRandom(t *testing.T) {
	// Power-of-d-choices: sampling d=2 and taking the shorter queue must pull the
	// chosen load well below the fleet mean; d=1 (blind) sits at the mean. This is
	// the behavioral signature of JSQ(d) and proves d matters.
	loads := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	mean := 4.5
	fleet := &mockFleet{loads: loads}

	avgChosen := func(d, trials int) float64 {
		j := NewJSQd(d, rand.New(rand.NewSource(42)))
		sum := 0
		for i := 0; i < trials; i++ {
			sum += loads[j.Route(Request{}, fleet)]
		}
		return float64(sum) / float64(trials)
	}

	const trials = 200_000
	d1 := avgChosen(1, trials)
	d2 := avgChosen(2, trials)
	if d1 < mean-0.1 || d1 > mean+0.1 {
		t.Fatalf("d=1 chosen-load mean %.3f should sit at fleet mean %.1f", d1, mean)
	}
	if d2 >= d1-0.5 {
		t.Fatalf("d=2 chosen-load mean %.3f should be well below d=1 %.3f", d2, d1)
	}
	t.Logf("JSQ chosen-load mean: d=1 %.3f (~fleet mean), d=2 %.3f (shorter)", d1, d2)
}

// --- cache-affinity: reuse + bounded-load balancing + hotspot rebalance ---

func TestCacheAffinityReusesColdPrefixHome(t *testing.T) {
	// Under uniform low load a cold prefix always lands on its consistent-hash
	// home, and that home matches the pure consistent-hash policy: real KV reuse.
	ca := NewCacheAffinity(4, 0, 0, 0, 0)
	ch := NewConsistentHash(4, 0)
	prefix := "cold-shared-prefix"
	want := ch.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0))
	for i := 0; i < 5; i++ { // stay well under the hotness threshold
		if got := ca.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0)); got != want {
			t.Fatalf("cold prefix should reuse home %d, got %d", want, got)
		}
	}
}

func TestCacheAffinityOverflowsSaturatedHome(t *testing.T) {
	// Bounded loads: when a prefix's home is buried far over the load cap, the
	// request overflows to a lightly loaded backend instead of pinning to the
	// warm-but-saturated home. This is the load-balancing dimension.
	ca := NewCacheAffinity(4, 0, 0, 0, 0)
	prefix := "hot-but-saturated"
	home := ca.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0))

	loads := make([]int, 4)
	loads[home] = 500 // far above (1+eps)*avg+1
	got := ca.Route(Request{Prefix: prefix}, &mockFleet{loads: loads})
	if got == home {
		t.Fatalf("saturated home %d should overflow, but stayed", home)
	}
	if loads[got] != 0 {
		t.Fatalf("overflow target %d should be a lightly loaded backend", got)
	}
}

func TestCacheAffinityRebalancesHotspotAcrossBackends(t *testing.T) {
	// Hotspot handling: a single hot prefix driven under rising load must spread
	// across several backends via replication, whereas pure consistent-hash pins
	// the entire hotspot to one backend. This is the anti-strawman dimension: the
	// cache router does not collapse a hot prefix onto its home.
	const requests = 400
	hot := "the-one-hot-prefix"

	spread := func(r Router) map[int]int {
		loads := make([]int, 4)
		fleet := &mockFleet{loads: loads}
		hits := map[int]int{}
		for i := 0; i < requests; i++ {
			b := r.Route(Request{Prefix: hot, Tenant: "t"}, fleet)
			hits[b]++
			loads[b]++ // simulate the placed request occupying the backend
		}
		return hits
	}

	chHits := spread(NewConsistentHash(4, 0))
	if len(chHits) != 1 {
		t.Fatalf("consistent-hash should pin the hotspot to 1 backend, used %d", len(chHits))
	}

	caHits := spread(NewCacheAffinity(4, 0, 0, 0, 0))
	if len(caHits) < 2 {
		t.Fatalf("cache-affinity should rebalance the hotspot across >=2 backends, used %d", len(caHits))
	}
	// And it should genuinely balance, not just leak a few: the busiest backend
	// must not hold the overwhelming majority.
	busiest := 0
	for _, c := range caHits {
		if c > busiest {
			busiest = c
		}
	}
	if busiest > requests*3/4 {
		t.Fatalf("hotspot not balanced: busiest backend took %d/%d", busiest, requests)
	}
	t.Logf("hotspot spread: consistent-hash=%v cache-affinity=%v", chHits, caHits)
}

// --- VTC-cache-blind: fair scheduling + cache-blind placement ---

func TestVTCPlacesOnLeastLoaded(t *testing.T) {
	v := NewVTCCacheBlind(1, 2)
	if got := v.Route(Request{Prefix: "anything"}, newMockFleet(3, 0, 5)); got != 1 {
		t.Fatalf("VTC placement should pick least-loaded index 1, got %d", got)
	}
}

func TestVTCOrderInterleavesTenantsFairly(t *testing.T) {
	// Two tenants, equal per-request cost. VTC must interleave so a tenant with
	// more queued work does not run all of it before the other gets served.
	v := NewVTCCacheBlind(1, 0) // cost = input tokens
	pending := []Request{
		{Tenant: "A", InputTokens: 1},
		{Tenant: "A", InputTokens: 1},
		{Tenant: "B", InputTokens: 1},
	}
	got := v.Order(pending)
	// A(0) first, then B(0) jumps ahead of A's second request, then A again.
	want := []int{0, 2, 1}
	if !equalInts(got, want) {
		t.Fatalf("VTC order = %v, want interleaved %v", got, want)
	}
	// Idempotent: ordering does not mutate the live accountant.
	if got2 := v.Order(pending); !equalInts(got2, want) {
		t.Fatalf("VTC order not idempotent: %v then %v", got, got2)
	}
}

func TestVTCOrderRespectsChargedHistory(t *testing.T) {
	// A tenant already heavily served drops behind a fresh tenant in dispatch
	// order: VTC serves the least-served tenant first.
	v := NewVTCCacheBlind(1, 0)
	v.Accountant().Activate("heavy")
	v.Accountant().Activate("light")
	v.Accountant().Charge("heavy", 100, 0)

	pending := []Request{
		{Tenant: "heavy", InputTokens: 1},
		{Tenant: "light", InputTokens: 1},
	}
	got := v.Order(pending)
	if got[0] != 1 {
		t.Fatalf("VTC should serve the least-served tenant (light) first, order %v", got)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
