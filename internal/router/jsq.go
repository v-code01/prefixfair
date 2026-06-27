package router

import "math/rand"

// JSQd is join-shortest-of-d-queues: sample d backends uniformly at random and
// route to the least-loaded of the sample by outstanding request count. It is the
// classic power-of-d-choices load balancer (d=2 is the well-known "power of two
// choices"). It is cache-blind: it balances load with no regard for where a
// prefix is cached, so it holds down tail load at the cost of KV reuse. It marks
// the load-balanced, cache-oblivious reference on the frontier.
type JSQd struct {
	d   int
	rng *rand.Rand
}

// NewJSQd builds the policy sampling d of the fleet's backends per decision. d is
// clamped to at least 1; a d at or above the fleet size becomes exact
// join-shortest-queue over all backends. rng is the sampling source; pass a
// seeded one for deterministic tests.
func NewJSQd(d int, rng *rand.Rand) *JSQd {
	if d < 1 {
		d = 1
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(1))
	}
	return &JSQd{d: d, rng: rng}
}

// Route samples d distinct backends and returns the one with the smallest load,
// breaking ties toward the lower index for determinism given a fixed rng.
func (j *JSQd) Route(_ Request, state FleetState) int {
	n := state.N()
	if n <= 0 {
		return 0
	}
	d := j.d
	if d > n {
		d = n
	}

	sample := j.sampleDistinct(n, d)
	best := sample[0]
	bestLoad := state.Load(best)
	for _, i := range sample[1:] {
		if l := state.Load(i); l < bestLoad || (l == bestLoad && i < best) {
			best, bestLoad = i, l
		}
	}
	return best
}

// Name identifies the policy in reports.
func (j *JSQd) Name() string { return "jsq-d" }

// sampleDistinct draws d distinct backend indices from [0,n) uniformly using a
// partial Fisher-Yates shuffle, which is exact (no rejection bias) and O(d).
func (j *JSQd) sampleDistinct(n, d int) []int {
	perm := make([]int, n)
	for i := range perm {
		perm[i] = i
	}
	for i := 0; i < d; i++ {
		k := i + j.rng.Intn(n-i)
		perm[i], perm[k] = perm[k], perm[i]
	}
	return perm[:d]
}
