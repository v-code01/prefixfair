package router

import "prefixfair/internal/vtc"

// FairCacheAffinity couples the two mechanisms the frontier holds apart: VTC's
// least-served-tenant admission (where cross-tenant fairness comes from) and DualMap
// cache-affinity placement (where KV reuse comes from). The frontier measurement
// shows fairness lives in dispatch ORDER and cache reuse lives in PLACEMENT, so a
// policy that schedules like VTC and places like cache-affinity should, if the two do
// not fight, reach a low cross-tenant service gap AND a high cache-hit at once.
//
// Whether it actually DOMINATES the frontier is an empirical question the held-out-
// seed measurement answers, not this type. The coupling is the mechanism; the claim
// is earned only with confidence intervals on unseen seeds, else it is reported
// attempted-and-null. That is the whole discipline of the ambitious layer.
//
// Placement is inherited wholesale from CacheAffinity (consistent-hash home +
// bounded-load overflow + sliding-window hotspot replication), so the coupled policy
// carries the same non-strawman cache mechanism. Fairness is the embedded VTC
// accountant: exposing Accountant() makes the harness admit this policy's requests in
// least-served-tenant order, exactly as it does for VTCCacheBlind.
type FairCacheAffinity struct {
	*CacheAffinity
	acct *vtc.Accountant
}

// NewFairCacheAffinity builds the coupled policy over n backends with the VTC cost
// weights (the same weights the fairness metric is reported in). Placement uses
// cache-affinity's documented defaults, never fit to the trace.
func NewFairCacheAffinity(n int, wIn, wOut float64) *FairCacheAffinity {
	return &FairCacheAffinity{
		CacheAffinity: NewCacheAffinity(n, 0, 0, 0, 0),
		acct:          vtc.New(wIn, wOut),
	}
}

// Accountant exposes the VTC accountant so the harness charges real served tokens and
// admits in least-served-tenant order. Satisfying this interface is what couples
// fairness onto the inherited cache-affinity placement; without it the harness would
// fall back to FIFO admission and the policy would lose its fairness.
func (f *FairCacheAffinity) Accountant() *vtc.Accountant { return f.acct }

// Order dispatches pending requests least-served-tenant first, VTC's fair schedule,
// identical to VTCCacheBlind's ordering.
func (f *FairCacheAffinity) Order(pending []Request) []int {
	return orderLeastServed(f.acct, pending)
}

// Name identifies the policy in reports, overriding the embedded cache-affinity name.
func (f *FairCacheAffinity) Name() string { return "fair-cache-affinity" }
