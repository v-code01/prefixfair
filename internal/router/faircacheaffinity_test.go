package router

import (
	"testing"

	"prefixfair/internal/vtc"
)

// The coupled policy must be BOTH faces at once: it places like cache-affinity (so it
// earns KV reuse) and it schedules like VTC (so it equalizes cross-tenant service).
// These tests pin each face independently, plus the interface contract the harness
// keys on to give it fair admission.

// TestFairCacheAffinityPlacesLikeCacheAffinity: under uniform low load a cold prefix
// must land on its consistent-hash home, identical to the standalone cache-affinity
// policy. That is the cache dimension it inherits by construction.
func TestFairCacheAffinityPlacesLikeCacheAffinity(t *testing.T) {
	fca := NewFairCacheAffinity(4, 1, 2)
	ca := NewCacheAffinity(4, 0, 0, 0, 0)
	prefix := "cold-shared-prefix"
	want := ca.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0))
	for i := 0; i < 5; i++ {
		if got := fca.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0)); got != want {
			t.Fatalf("coupled policy should place like cache-affinity (home %d), got %d", want, got)
		}
	}
}

// TestFairCacheAffinityOverflowsSaturatedHome: it inherits cache-affinity's
// bounded-load overflow, so a buried home still sheds load. Placement is the full
// DualMap mechanism, not a naive prefix pin.
func TestFairCacheAffinityOverflowsSaturatedHome(t *testing.T) {
	fca := NewFairCacheAffinity(4, 1, 2)
	prefix := "hot-but-saturated"
	home := fca.Route(Request{Prefix: prefix}, newMockFleet(0, 0, 0, 0))
	loads := make([]int, 4)
	loads[home] = 500
	if got := fca.Route(Request{Prefix: prefix}, &mockFleet{loads: loads}); got == home {
		t.Fatalf("saturated home %d should overflow under the coupled policy, but stayed", home)
	}
}

// TestFairCacheAffinityOrdersLikeVTC: dispatch order must be least-served-tenant
// first, identical to VTC. That is the fairness dimension it couples in.
func TestFairCacheAffinityOrdersLikeVTC(t *testing.T) {
	fca := NewFairCacheAffinity(4, 1, 0) // cost = input tokens
	pending := []Request{
		{Tenant: "A", InputTokens: 1},
		{Tenant: "A", InputTokens: 1},
		{Tenant: "B", InputTokens: 1},
	}
	got := fca.Order(pending)
	want := []int{0, 2, 1} // A, then B jumps ahead of A's second, then A
	if !equalInts(got, want) {
		t.Fatalf("coupled order = %v, want VTC-interleaved %v", got, want)
	}
}

// TestFairCacheAffinityExposesAccountant: the harness selects VTC admission for any
// router exposing a *vtc.Accountant. The coupled policy must satisfy that interface,
// or it silently falls back to FIFO admission and loses its fairness.
func TestFairCacheAffinityExposesAccountant(t *testing.T) {
	fca := NewFairCacheAffinity(4, 1, 2)
	var i interface{} = fca
	acctHolder, ok := i.(interface{ Accountant() *vtc.Accountant })
	if !ok {
		t.Fatal("coupled policy must expose Accountant() so the harness gives it VTC admission")
	}
	if acctHolder.Accountant() == nil {
		t.Fatal("coupled policy Accountant() must be non-nil")
	}
}

// TestFairCacheAffinityName pins the reporting name.
func TestFairCacheAffinityName(t *testing.T) {
	if n := NewFairCacheAffinity(4, 1, 2).Name(); n != "fair-cache-affinity" {
		t.Fatalf("name = %q, want fair-cache-affinity", n)
	}
}
