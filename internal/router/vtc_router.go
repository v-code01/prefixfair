package router

import "prefixfair/internal/vtc"

// VTCCacheBlind is the fairness baseline built on the real Virtual Token Counter
// (arXiv 2401.00588). It is deliberately cache-agnostic: it never looks at where a
// prefix is cached, so it trades away KV reuse for fairness. It marks the fair,
// cache-oblivious corner of the frontier, opposite the cache-maximal
// consistent-hash corner.
//
// VTC has two faces here, both faithful to the paper:
//
//   - Scheduling (the paper's mechanism). The embedded accountant maintains a
//     per-tenant virtual counter and, via Order, dispatches requests in
//     least-served-tenant order. This is where VTC's fairness actually comes from
//     and where its bounded service-gap guarantee lives (see the vtc package's
//     sacred bound test). The harness charges the accountant the real served
//     tokens so the counters track real service.
//
//   - Placement. Across homogeneous backends VTC has no cache preference, so it
//     places each request on the least-loaded backend: spreading load is the
//     cache-blind choice that keeps every tenant's requests moving and prevents
//     one backend's backlog from starving a tenant.
type VTCCacheBlind struct {
	acct *vtc.Accountant
}

// NewVTCCacheBlind builds the policy with the paper's weighted cost function. wIn
// weights input tokens and wOut weights output tokens; pass the same weights the
// fairness metric is reported in.
func NewVTCCacheBlind(wIn, wOut float64) *VTCCacheBlind {
	return &VTCCacheBlind{acct: vtc.New(wIn, wOut)}
}

// Accountant exposes the underlying VTC accountant so the harness can charge real
// served tokens and read the cross-tenant service gap for the metric.
func (v *VTCCacheBlind) Accountant() *vtc.Accountant { return v.acct }

// Route places a request on the least-loaded backend, the cache-blind choice. It
// does not charge the accountant: charging is the harness's job once the real
// served token counts are known, so a request is never double-counted.
func (v *VTCCacheBlind) Route(_ Request, state FleetState) int {
	return globalLeastLoaded(state)
}

// Name identifies the policy in reports.
func (v *VTCCacheBlind) Name() string { return "vtc-cache-blind" }

// Order returns the indices of pending requests in the order VTC would dispatch
// them: least-served tenant first, so service equalizes across tenants. It is a
// stable simulation of the scheduler over a fixed batch and does not mutate the
// live accountant, so repeatedly ordering the same batch is idempotent. Ties in
// counter and in per-batch accrued cost fall back to input order for determinism.
func (v *VTCCacheBlind) Order(pending []Request) []int {
	// Work on a local copy of the counters so the live accountant is untouched.
	local := make(map[string]float64, len(pending))
	for _, r := range pending {
		if _, ok := local[r.Tenant]; !ok {
			local[r.Tenant] = v.acct.Counter(r.Tenant)
		}
	}

	// Repeatedly emit the pending request whose tenant currently has the smallest
	// virtual counter, then advance that tenant's local counter by the request's
	// cost, mirroring serve-least-then-charge. Iterating candidates in input order
	// with a strict less-than keeps ties in input order.
	used := make([]bool, len(pending))
	order := make([]int, 0, len(pending))
	for len(order) < len(pending) {
		best := -1
		for idx := range pending {
			if used[idx] {
				continue
			}
			if best == -1 || local[pending[idx].Tenant] < local[pending[best].Tenant] {
				best = idx
			}
		}
		used[best] = true
		order = append(order, best)
		r := pending[best]
		local[r.Tenant] += v.acct.Cost(r.InputTokens, r.OutputTokens)
	}
	return order
}
