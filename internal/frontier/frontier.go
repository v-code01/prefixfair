// Package frontier is the real-backend measurement harness: it replays a
// multi-tenant Zipfian trace against N live llama-server backends under each of the
// four routing policies and records, per policy, where it lands on the
// cache-hit / cross-tenant service-gap plane. The output is the Pareto frontier the
// whole project exists to measure, reported however it falls.
//
// Realness rules, all enforced here:
//
//   - Every latency and every cache-reuse number comes from a real server slot via
//     the worker package. Nothing in the measured path is simulated.
//
//   - Routing is single-threaded. The four policies keep mutable state
//     (cache-affinity's popularity window, JSQ's rng, VTC's counters) that is not
//     safe for concurrent use, so every Route call and every accountant update
//     happens on one control goroutine. The concurrency lives in the fleet serving
//     the dispatched requests, never in the routing decision. This is the load-
//     bearing invariant behind the whole harness.
//
//   - Fairness is measured under VTC's own assumption. The trace keeps every tenant
//     backlogged through the measurement horizon, and the gap is read at a fixed
//     completion count (a snapshot mid-backlog), which is exactly the regime VTC's
//     bounded service-gap is defined in. Running a trace to full drain would serve
//     every tenant its whole demand and collapse the gap for every policy alike (a
//     tautology); the snapshot under backlog is what makes the gap policy-dependent.
package frontier

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"prefixfair/internal/router"
	"prefixfair/internal/trace"
	"prefixfair/internal/vtc"
	"prefixfair/internal/worker"
)

// Config parameterizes a sweep. Trace describes the workload; Fleet describes the
// real backends; the rest are measurement knobs.
type Config struct {
	Fleet worker.FleetSpec
	Trace trace.Spec

	// Completions is K: the gap and cache-hit numbers are read at the K-th
	// successful completion, a snapshot taken while every tenant is still
	// backlogged. It must be small enough that the tail tenant has not run out of
	// work by then (checked at run time and surfaced if violated).
	Completions int

	// MaxInFlight caps concurrent in-flight requests; 0 defaults to the fleet's
	// total slot count (N*Slots), which keeps every slot busy under backlog without
	// an unbounded stampede.
	MaxInFlight int

	NPredict    int     // generation length per request
	HitFraction float64 // reuse bar for HIT classification (worker.DefaultHitFraction)
	WIn, WOut   float64 // VTC cost weights for the service metric
}

// PolicyResult is one policy's landing point on the frontier for one seed.
type PolicyResult struct {
	Policy string

	// Cache axis.
	HitRate   float64 // fraction of the K completions classified HIT
	MeanReuse float64 // mean per-request reuse fraction over the K completions

	// Fairness axis, from the metric accountant charged with real served tokens.
	ServiceGap    float64 // max-min cross-tenant service (weighted tokens)
	NormalizedGap float64 // ServiceGap / mean per-tenant service (dimensionless)

	// Latency, under real concurrency.
	TTFTp50  time.Duration
	TTFTp95  time.Duration
	TTFTMean time.Duration

	Completed  int  // successful completions actually measured (K unless the trace ran short)
	Errors     int  // real (non-cancellation) errors surfaced
	Backlogged bool // whether every tenant still had pending work at the snapshot
}

// SeedResult is every policy's result on one trace seed.
type SeedResult struct {
	Seed     int64
	Policies []PolicyResult
}

// liveState is the fleet load view a policy routes against. It is read and mutated
// only on the single control goroutine, so plain ints are correct and no
// synchronization is needed; that is the point of single-threaded routing.
type liveState struct{ inflight []int }

func (s *liveState) N() int { return len(s.inflight) }
func (s *liveState) Load(i int) int {
	if i < 0 || i >= len(s.inflight) {
		return 0
	}
	return s.inflight[i]
}

// completion is a finished (or failed) request handed back from a worker goroutine
// to the control goroutine.
type completion struct {
	req  trace.Request
	widx int
	res  worker.Result
	err  error
}

// admitter decides which pending request is dispatched next. FIFO admission
// (arrival order) drives the three placement-only policies; VTC admission
// (least-served tenant first) drives the VTC policy, because scheduling order is
// where VTC's fairness actually comes from. Separating admission from placement is
// what lets each policy run its real mechanism.
type admitter interface {
	next() (trace.Request, bool)
}

// fifoAdmitter dispatches requests in arrival order.
type fifoAdmitter struct {
	reqs []trace.Request
	i    int
}

func (f *fifoAdmitter) next() (trace.Request, bool) {
	if f.i >= len(f.reqs) {
		return trace.Request{}, false
	}
	r := f.reqs[f.i]
	f.i++
	return r, true
}

// vtcAdmitter dispatches the least-served backlogged tenant's next request, VTC's
// real scheduling mechanism. It charges the policy's accountant a unit cost at
// admission so the counter advances immediately (otherwise the least-served tenant
// would be admitted in a burst before any completion charged it). Trace requests
// are near-uniform in size, so unit-per-admission is request-count fairness, which
// for this workload equals VTC's token-weighted fairness; the reported gap is still
// measured on real served tokens by a separate metric accountant.
type vtcAdmitter struct {
	acct   *vtc.Accountant
	queues map[string][]trace.Request
}

func newVTCAdmitter(acct *vtc.Accountant, reqs []trace.Request) *vtcAdmitter {
	q := make(map[string][]trace.Request)
	for _, r := range reqs {
		q[r.Tenant] = append(q[r.Tenant], r)
	}
	for tenant := range q {
		acct.Activate(tenant) // every tenant starts backlogged
	}
	return &vtcAdmitter{acct: acct, queues: q}
}

func (v *vtcAdmitter) next() (trace.Request, bool) {
	tenant, ok := v.acct.NextTenant()
	if !ok {
		return trace.Request{}, false
	}
	qq := v.queues[tenant]
	r := qq[0]
	v.queues[tenant] = qq[1:]
	v.acct.Charge(tenant, 1, 0) // unit admission cost; advance the counter now
	if len(v.queues[tenant]) == 0 {
		v.acct.Deactivate(tenant) // this tenant is no longer backlogged
	}
	return r, true
}

// completeWithRetry issues a completion and retries a small number of times on a
// transient connection-pool race. Under the frontier's bursty load, llama-server
// closes idle keep-alive connections and the client can pick a just-closed one,
// yielding "server closed idle connection", EOF, or a reset before the request is
// ever processed. These fail before any token is served, so no service was
// rendered and the retry double-counts nothing; a fresh attempt also re-measures
// TTFT cleanly. Non-transient failures (bad status, no tokens generated) are
// returned immediately and surfaced as real errors.
func completeWithRetry(ctx context.Context, w *worker.Worker, r worker.Request) (worker.Result, error) {
	const maxAttempts = 4
	var res worker.Result
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		res, err = w.Complete(ctx, r)
		if err == nil || ctx.Err() != nil || !transientConnErr(err) {
			return res, err
		}
	}
	return res, err
}

// transientConnErr reports whether an error is a pre-token connection-pool race
// that is safe to retry, as opposed to a real backend rejection.
func transientConnErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "server closed idle connection") ||
		strings.Contains(s, "use of closed network connection") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "connection reset")
}

// newAdmitter picks the admission discipline for a policy: VTC scheduling if the
// policy exposes a VTC accountant, FIFO otherwise.
func newAdmitter(p router.Router, reqs []trace.Request) admitter {
	if fa, ok := p.(interface{ Accountant() *vtc.Accountant }); ok {
		return newVTCAdmitter(fa.Accountant(), reqs)
	}
	return &fifoAdmitter{reqs: reqs}
}

// runPolicy replays reqs through one policy against the live fleet and returns its
// frontier point. All routing, accounting, and load bookkeeping happen on this
// goroutine; worker goroutines only perform the real HTTP completion and hand the
// result back over a channel.
func runPolicy(ctx context.Context, f *worker.Fleet, reqs []trace.Request, p router.Router, cfg Config) PolicyResult {
	n := f.Len()
	state := &liveState{inflight: make([]int, n)}
	adm := newAdmitter(p, reqs)

	maxInFlight := cfg.MaxInFlight
	if maxInFlight < 1 {
		maxInFlight = n * cfg.Fleet.Slots
	}
	if maxInFlight < 1 {
		maxInFlight = n
	}
	k := cfg.Completions
	if k < 1 {
		k = len(reqs)
	}

	// The metric accountant is the uniform fairness yardstick for every policy: all
	// tenants activated and kept active (the backlogged assumption), charged the
	// real served tokens of the measured completions.
	metric := vtc.New(cfg.WIn, cfg.WOut)
	tenants := distinctTenants(reqs)
	for _, t := range tenants {
		metric.Activate(t)
	}
	servedCost := make(map[string]float64, len(tenants))

	results := make(chan completion, maxInFlight)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		inFlight  int
		okCount   int
		errCount  int
		hitCount  int
		reuseSum  float64
		ttfts     []time.Duration
		gapAtK    float64
		admitting = true
	)

	for {
		for admitting && inFlight < maxInFlight {
			req, ok := adm.next()
			if !ok {
				admitting = false
				break
			}
			widx := p.Route(router.Request{Tenant: req.Tenant, Prefix: req.Prefix}, state)
			if widx < 0 || widx >= n {
				widx = 0
			}
			state.inflight[widx]++
			inFlight++
			go func(r trace.Request, wi int) {
				res, err := completeWithRetry(runCtx, f.Workers[wi], worker.Request{
					Prompt: r.Prompt, NPredict: cfg.NPredict, CachePrompt: true,
				})
				results <- completion{req: r, widx: wi, res: res, err: err}
			}(req, widx)
		}

		if inFlight == 0 {
			break // nothing pending and nothing running
		}

		c := <-results
		state.inflight[c.widx]--
		inFlight--

		if c.err != nil {
			// A cancellation after the snapshot is expected teardown, not a failure.
			if runCtx.Err() == nil {
				errCount++
			}
			continue
		}
		if okCount >= k {
			continue // past the snapshot; just drain the tail
		}

		// Real served tokens: full prompt (prefilled plus cache-reused) as input,
		// generated tokens as output. Charging the full nominal prompt (not only the
		// prefilled part) keeps the metric un-gameable: a policy cannot shrink a
		// tenant's charged service by getting it more cache hits.
		inTok := c.res.Timings.PromptN + c.res.Timings.CacheN
		outTok := c.res.Tokens
		metric.Charge(c.req.Tenant, inTok, outTok)
		servedCost[c.req.Tenant] += metric.Cost(inTok, outTok)

		reuseSum += c.res.Timings.ReuseFraction()
		if worker.ClassifyCache(c.res.Timings, cfg.HitFraction) == worker.CacheHit {
			hitCount++
		}
		ttfts = append(ttfts, c.res.TTFT)
		okCount++

		if okCount == k {
			gapAtK = metric.Gap()
			admitting = false
			cancel() // stop the in-flight tail; its completions drain as errors
		}
	}

	res := PolicyResult{
		Policy:     p.Name(),
		Completed:  okCount,
		Errors:     errCount,
		ServiceGap: gapAtK,
		Backlogged: allBacklogged(reqs, tenants, okCount, p),
	}
	if okCount > 0 {
		res.MeanReuse = reuseSum / float64(okCount)
		res.HitRate = float64(hitCount) / float64(okCount)
	}
	if okCount < k {
		// The snapshot was never reached; report the gap at whatever was served.
		res.ServiceGap = metric.Gap()
	}
	res.NormalizedGap = normalizedGap(res.ServiceGap, servedCost, len(tenants))
	res.TTFTp50, res.TTFTp95, res.TTFTMean = ttftStats(ttfts)
	return res
}

// policyFactory builds a fresh instance of one policy for a given seed. A fresh
// instance per seed is required: cache-affinity's popularity window, JSQ's rng, and
// VTC's counters must not carry across seeds.
type policyFactory struct {
	name  string
	build func(n int, seed int64, cfg Config) router.Router
}

// policyFactories is the fixed set of four policies, in reporting order. Cache-
// affinity takes its documented defaults (never fit to the trace); JSQ's rng is
// seeded from the trace seed for reproducibility.
func policyFactories() []policyFactory {
	return []policyFactory{
		{"cache-affinity", func(n int, _ int64, _ Config) router.Router {
			return router.NewCacheAffinity(n, 0, 0, 0, 0)
		}},
		{"vtc-cache-blind", func(_ int, _ int64, cfg Config) router.Router {
			return router.NewVTCCacheBlind(cfg.WIn, cfg.WOut)
		}},
		{"jsq-d", func(_ int, seed int64, _ Config) router.Router {
			return router.NewJSQd(2, rand.New(rand.NewSource(seed)))
		}},
		{"consistent-hash", func(n int, _ int64, _ Config) router.Router {
			return router.NewConsistentHash(n, 0)
		}},
	}
}

// RunSweep replays every seed through every policy and returns one SeedResult per
// seed. It is policy-major: each policy gets its own freshly started, cold fleet,
// which is the honest way to keep the cache-hit measurement clean. No policy ever
// inherits another policy's warm KV slots (separate fleets), and within a policy
// different seeds use disjoint tenant prefixes, so a warm slot from one seed can
// never register a false hit on the next. This costs one fleet start per policy
// (four total) regardless of how many seeds are swept.
//
// The caller supplies a started-and-reaped fleet through startFleet; the harness
// starts a fresh one per policy and reaps it before the next.
func RunSweep(ctx context.Context, cfg Config, seeds []int64, startFleet func() (*worker.Fleet, error)) ([]SeedResult, error) {
	results := make([]SeedResult, len(seeds))
	for i, s := range seeds {
		results[i] = SeedResult{Seed: s}
	}

	for _, pf := range policyFactories() {
		if err := runPolicyOverSeeds(ctx, cfg, seeds, pf, startFleet, results); err != nil {
			return results, err
		}
	}
	return results, nil
}

// runPolicyOverSeeds starts one cold fleet, replays every seed through one policy
// on it, and reaps the fleet. Results are written into the per-seed slots in place.
func runPolicyOverSeeds(ctx context.Context, cfg Config, seeds []int64, pf policyFactory, startFleet func() (*worker.Fleet, error), results []SeedResult) error {
	f, err := startFleet()
	if err != nil {
		return fmt.Errorf("frontier: start fleet for %s: %w", pf.name, err)
	}
	defer f.Stop()
	n := f.Len()

	for i, seed := range seeds {
		spec := cfg.Trace
		spec.Seed = seed
		reqs := trace.Generate(spec)

		if i == 0 {
			if err := validateBudget(ctx, f, reqs, cfg.NPredict); err != nil {
				return err
			}
		}
		p := pf.build(n, seed, cfg)
		results[i].Policies = append(results[i].Policies, runPolicy(ctx, f, reqs, p, cfg))
	}
	return nil
}

// distinctTenants returns the sorted set of tenant ids appearing in a trace.
func distinctTenants(reqs []trace.Request) []string {
	set := make(map[string]bool)
	for _, r := range reqs {
		set[r.Tenant] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// allBacklogged reports whether, at the snapshot, no tenant had exhausted its
// pending work under the policy's admission. For FIFO admission the worst case is
// the head tenant; for VTC admission it is the least-served tail. The check is a
// conservative honesty guard: a false here means the snapshot ran past backlog for
// some tenant and the gap is no longer strictly inside VTC's assumption.
func allBacklogged(reqs []trace.Request, tenants []string, served int, p router.Router) bool {
	if served == 0 {
		return true
	}
	total := make(map[string]int)
	for _, r := range reqs {
		total[r.Tenant]++
	}
	// Under any admission, a tenant is still backlogged at the snapshot only if its
	// total demand exceeds what it could have been served. The tightest universal
	// check: the least-demanded tenant must have more requests than an equal share
	// of the served count (VTC would drive it to that share).
	minTotal := len(reqs)
	for _, t := range tenants {
		if total[t] < minTotal {
			minTotal = total[t]
		}
	}
	fairShare := served / len(tenants)
	return minTotal > fairShare
}

// normalizedGap expresses the service gap relative to the mean per-tenant service,
// a dimensionless fairness number comparable across seeds and trace sizes.
func normalizedGap(gap float64, served map[string]float64, tenants int) float64 {
	if tenants == 0 {
		return 0
	}
	var total float64
	for _, c := range served {
		total += c
	}
	mean := total / float64(tenants)
	if mean <= 0 {
		return 0
	}
	return gap / mean
}

// ttftStats returns p50, p95, and mean of a TTFT sample.
func ttftStats(d []time.Duration) (p50, p95, mean time.Duration) {
	if len(d) == 0 {
		return 0, 0, 0
	}
	s := make([]time.Duration, len(d))
	copy(s, d)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	p50 = s[len(s)*50/100]
	p95 = s[min(len(s)*95/100, len(s)-1)]
	var sum time.Duration
	for _, x := range s {
		sum += x
	}
	mean = sum / time.Duration(len(s))
	return p50, p95, mean
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// validateBudget tokenizes the worst-case prompt against the fleet and returns an
// error if it would exceed the per-slot context. A prompt over budget is served
// zero tokens, which would silently corrupt the frontier, so this fails fast.
func validateBudget(ctx context.Context, f *worker.Fleet, reqs []trace.Request, nPredict int) error {
	if f.Len() == 0 || len(reqs) == 0 {
		return fmt.Errorf("frontier: empty fleet or trace")
	}
	worst := reqs[0]
	for _, r := range reqs {
		if len(r.Prompt) > len(worst.Prompt) {
			worst = r
		}
	}
	tok, err := f.Workers[0].Tokenize(ctx, worst.Prompt)
	if err != nil {
		return fmt.Errorf("frontier: tokenize worst prompt: %w", err)
	}
	perSlot := f.Workers[0].Config().PerSlotCtx()
	if tok+nPredict >= perSlot {
		return fmt.Errorf("frontier: worst prompt %d + %d predict >= per-slot ctx %d",
			tok, nPredict, perSlot)
	}
	return nil
}
