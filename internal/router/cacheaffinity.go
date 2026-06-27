package router

// CacheAffinity is the faithful DualMap-style cache-aware policy (the 2026 SOTA
// line, arXiv 2602.06502; same family as SGLang-Router's cache-aware balancing).
// It carries all three dimensions a non-strawman cache router must have, and a
// naive "always route to the prefix holder ignoring load" would be a strawman it
// deliberately is not:
//
//  1. Cache affinity. A prefix maps to a deterministic home backend on a
//     consistent-hash ring, so repeated requests on that prefix reuse its KV
//     cache. Determinism is what makes the reuse real: the same prefix returns to
//     the same backend(s), so the slot is warm.
//
//  2. Load balancing (consistent hashing with bounded loads). The home is only
//     preferred while its load stays under a cap of (1+epsilon)*avgLoad+1. When a
//     prefix's home (and its replicas) are all over the cap, the request overflows
//     clockwise along the ring to the next backend under the cap. This bounds the
//     worst-case load any backend absorbs instead of letting a popular prefix pin
//     unbounded load onto one slot.
//
//  3. Hotspot handling (replication / rebalancing). Per-prefix request counts are
//     tracked over a sliding window. A cold prefix keeps a single home; a hot
//     prefix earns a replica set that grows with its popularity, and each request
//     is placed on the least-loaded replica. A hot prefix is thus spread across
//     several backends rather than pinned to one, which is the rebalancing a
//     cache-aware router must do to survive a skewed (Zipfian) prefix
//     distribution.
type CacheAffinity struct {
	ring       *ring
	epsilon    float64 // bounded-load slack over the average
	hotAt      int     // window count at which a prefix begins replicating
	hotStep    int     // additional window count per extra replica
	windowSpan int     // requests between hotness-window resets
	counts     map[string]int
	seen       int // requests since the last window reset
}

// NewCacheAffinity builds the policy over n backends. epsilon is the bounded-load
// slack (a home over (1+epsilon)*avg load overflows); hotAt is the window request
// count at which a prefix starts replicating; hotStep is how many further requests
// buy each additional replica; windowSpan is how many requests a hotness window
// spans before it resets so hotness tracks recent, not all-time, traffic. Zero or
// negative tuning values fall back to sensible defaults.
func NewCacheAffinity(n int, epsilon float64, hotAt, hotStep, windowSpan int) *CacheAffinity {
	if epsilon <= 0 {
		epsilon = defaultEpsilon
	}
	if hotAt < 1 {
		hotAt = defaultHotAt
	}
	if hotStep < 1 {
		hotStep = defaultHotStep
	}
	if windowSpan < 1 {
		windowSpan = defaultWindowSpan
	}
	return &CacheAffinity{
		ring:       newRing(n, defaultVirtualNodes),
		epsilon:    epsilon,
		hotAt:      hotAt,
		hotStep:    hotStep,
		windowSpan: windowSpan,
		counts:     make(map[string]int),
	}
}

// Route places a request. It first records the prefix's recent popularity, then
// builds the prefix's candidate backends (home plus, for a hot prefix, its replica
// set), picks the least-loaded candidate under the bounded-load cap, and overflows
// clockwise along the ring only when every candidate is saturated.
func (c *CacheAffinity) Route(req Request, state FleetState) int {
	n := state.N()
	if n <= 0 {
		return 0
	}
	replicas := c.observe(req.Prefix)
	candidates := c.ring.walk(req.Prefix, replicas)

	loadCap := (1+c.epsilon)*avgLoad(state) + 1

	// Prefer the least-loaded candidate that is under the cap: cache reuse when a
	// warm backend still has headroom.
	if best, _, ok := leastLoadedUnderCap(candidates, state, loadCap); ok {
		return best
	}

	// Every replica is saturated: overflow clockwise to the first ring backend
	// under the cap, so a hot prefix's excess load spreads instead of piling on.
	overflow := c.ring.walk(req.Prefix, n)
	if b, _, found := leastLoadedUnderCap(overflow, state, loadCap); found {
		return b
	}

	// The whole fleet is over the cap; fall back to the globally least-loaded
	// backend so a decision is always made.
	return globalLeastLoaded(state)
}

// Name identifies the policy in reports.
func (c *CacheAffinity) Name() string { return "cache-affinity" }

// observe records one request against the prefix's sliding hotness window and
// returns the replica count the prefix currently earns: 1 for a cold prefix,
// growing by one per hotStep requests once the count passes hotAt, capped at the
// fleet size. The window resets every windowSpan requests so a prefix that cools
// off loses its replicas.
func (c *CacheAffinity) observe(prefix string) int {
	if c.seen >= c.windowSpan {
		c.counts = make(map[string]int)
		c.seen = 0
	}
	c.counts[prefix]++
	c.seen++

	count := c.counts[prefix]
	if count < c.hotAt {
		return 1
	}
	return 1 + (count-c.hotAt)/c.hotStep + 1
}

// leastLoadedUnderCap returns the least-loaded candidate whose load is under cap.
// ok is false when every candidate is at or over the cap.
func leastLoadedUnderCap(candidates []int, state FleetState, loadCap float64) (best int, bestLoad int, ok bool) {
	for _, i := range candidates {
		l := state.Load(i)
		if float64(l) >= loadCap {
			continue
		}
		if !ok || l < bestLoad || (l == bestLoad && i < best) {
			best, bestLoad, ok = i, l, true
		}
	}
	return best, bestLoad, ok
}

// globalLeastLoaded returns the least-loaded backend across the whole fleet, the
// last-resort placement when every backend is over the bounded-load cap.
func globalLeastLoaded(state FleetState) int {
	best, bestLoad := 0, state.Load(0)
	for i := 1; i < state.N(); i++ {
		if l := state.Load(i); l < bestLoad {
			best, bestLoad = i, l
		}
	}
	return best
}

const (
	defaultEpsilon    = 0.25
	defaultHotAt      = 8
	defaultHotStep    = 8
	defaultWindowSpan = 1024
)
