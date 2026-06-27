// Package vtc is a faithful single-node reimplementation of the Virtual Token
// Counter fair scheduler (Sheng et al., "Fairness in Serving Large Language
// Models", OSDI'24, arXiv 2401.00588). It is two things at once: the fairness
// baseline policy's scheduling core, and the accountant that defines the
// cross-tenant service metric the frontier is measured against.
//
// The mechanism is fair queueing over LLM service. Each tenant carries a virtual
// counter measuring the weighted tokens it has been served (input tokens at wIn,
// output tokens at wOut). The scheduler always dispatches the backlogged tenant
// with the smallest counter, then charges that tenant the request's cost. A
// tenant that goes idle and returns has its counter lifted to the active minimum
// so it cannot hoard credit while absent and then monopolize the server.
//
// The scoped correctness guarantee (the sacred anchor) is VTC's bounded
// service-gap under the continuously-backlogged client assumption: while every
// tenant always has work pending, the difference in accumulated service between
// any two tenants stays within a single max-cost request for the whole horizon.
// The guarantee is claimed only under that assumption; the lift mechanism is
// exactly what makes the scoping honest.
package vtc

import "math"

// Accountant tracks per-tenant virtual counters and schedules the least-served
// backlogged tenant. It is not safe for concurrent use; the routing harness owns
// one accountant and serializes updates through it.
type Accountant struct {
	wIn, wOut float64
	counter   map[string]float64
	active    map[string]bool
}

// New builds an accountant with the paper's weighted cost function. wIn weights
// input (prefill) tokens and wOut weights output (decode) tokens; the paper's
// illustrative setting charges decode more than prefill (for example 1 and 2).
// Both must be finite and non-negative.
func New(wIn, wOut float64) *Accountant {
	if wIn < 0 || wOut < 0 || math.IsNaN(wIn) || math.IsNaN(wOut) {
		panic("vtc: weights must be finite and non-negative")
	}
	return &Accountant{
		wIn:     wIn,
		wOut:    wOut,
		counter: make(map[string]float64),
		active:  make(map[string]bool),
	}
}

// Cost is the weighted service of a request: wIn*inTok + wOut*outTok. This is the
// unit every counter accrues in and the unit the service gap is measured in.
func (a *Accountant) Cost(inTok, outTok int) float64 {
	return a.wIn*float64(inTok) + a.wOut*float64(outTok)
}

// Counter returns a tenant's current virtual counter (its accumulated weighted
// service). An unseen tenant reads 0.
func (a *Accountant) Counter(tenant string) float64 { return a.counter[tenant] }

// Activate marks a tenant as backlogged (having work pending). If the tenant was
// not already active, its counter is lifted to the minimum counter among the
// currently active tenants, so a returning-from-idle tenant starts level with
// the field instead of carrying a stale low counter. A tenant that is already
// active is unaffected, so continuous backlog never triggers a lift, matching
// VTC's assumption exactly.
func (a *Accountant) Activate(tenant string) {
	if a.active[tenant] {
		return
	}
	if m, ok := a.minActive(); ok && a.counter[tenant] < m {
		a.counter[tenant] = m
	}
	a.active[tenant] = true
}

// Deactivate marks a tenant as no longer backlogged (its queue drained). It stays
// in the counter map so its service history persists for a later lift.
func (a *Accountant) Deactivate(tenant string) { delete(a.active, tenant) }

// NextTenant returns the backlogged tenant with the smallest virtual counter,
// the one VTC serves next. Ties are broken by tenant id so scheduling is
// deterministic. ok is false when no tenant is backlogged.
func (a *Accountant) NextTenant() (string, bool) {
	best := ""
	bestC := math.Inf(1)
	found := false
	for tenant := range a.active {
		c := a.counter[tenant]
		if !found || c < bestC || (c == bestC && tenant < best) {
			best, bestC, found = tenant, c, true
		}
	}
	return best, found
}

// Charge adds a served request's weighted cost to a tenant's counter. This is the
// single point where service is accounted; the scheduler only ever advances the
// tenant it just picked (the current minimum), which is what keeps the gap
// bounded under backlog.
func (a *Accountant) Charge(tenant string, inTok, outTok int) {
	a.counter[tenant] += a.Cost(inTok, outTok)
}

// Gap is the max-min spread of virtual counters across active tenants, the
// cross-tenant service gap the fairness metric reports. Fewer than two active
// tenants have no gap.
func (a *Accountant) Gap() float64 {
	lo := math.Inf(1)
	hi := math.Inf(-1)
	n := 0
	for tenant := range a.active {
		c := a.counter[tenant]
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
		n++
	}
	if n < 2 {
		return 0
	}
	return hi - lo
}

// minActive returns the smallest counter among active tenants, or ok=false when
// none are active.
func (a *Accountant) minActive() (float64, bool) {
	m := math.Inf(1)
	ok := false
	for tenant := range a.active {
		if c := a.counter[tenant]; c < m {
			m, ok = c, true
		}
	}
	return m, ok
}
