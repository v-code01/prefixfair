package frontier

import (
	"math"
	"strings"
	"testing"
	"time"

	"prefixfair/internal/trace"
	"prefixfair/internal/vtc"
	"prefixfair/internal/worker"
)

// TestFIFOAdmitterPreservesArrivalOrder pins that FIFO admission dispatches in
// exactly arrival order, then reports exhaustion. This is the discipline the three
// placement-only policies run under.
func TestFIFOAdmitterPreservesArrivalOrder(t *testing.T) {
	reqs := []trace.Request{
		{Tenant: "t000", Suffix: "a"},
		{Tenant: "t001", Suffix: "b"},
		{Tenant: "t000", Suffix: "c"},
	}
	a := &fifoAdmitter{reqs: reqs}
	for i, want := range []string{"a", "b", "c"} {
		got, ok := a.next()
		if !ok || got.Suffix != want {
			t.Fatalf("admission %d: got %q ok=%v, want %q", i, got.Suffix, ok, want)
		}
	}
	if _, ok := a.next(); ok {
		t.Fatal("admitter should be exhausted")
	}
}

// TestVTCAdmitterEqualizesTenants is the fairness mechanism under test: with a
// head-heavy backlog, VTC admission serves tenants in least-served-first order, so
// the first round of admissions is spread one-per-tenant rather than draining the
// heavy tenant first (which is what FIFO would do). This is exactly why VTC lands at
// a small service gap.
func TestVTCAdmitterEqualizesTenants(t *testing.T) {
	// Head-heavy pool: t000 has many requests, t001 and t002 few, all interleaved so
	// FIFO would admit t000 repeatedly first.
	var reqs []trace.Request
	for i := 0; i < 10; i++ {
		reqs = append(reqs, trace.Request{Tenant: "t000", Suffix: "h"})
	}
	reqs = append(reqs, trace.Request{Tenant: "t001"}, trace.Request{Tenant: "t002"})

	acct := vtc.New(1, 0)
	a := newVTCAdmitter(acct, reqs)

	// The first three admissions must cover all three tenants exactly once: VTC does
	// not let the heavy tenant monopolize the head of the schedule.
	seen := map[string]int{}
	for i := 0; i < 3; i++ {
		r, ok := a.next()
		if !ok {
			t.Fatalf("admission %d unexpectedly empty", i)
		}
		seen[r.Tenant]++
	}
	for _, tn := range []string{"t000", "t001", "t002"} {
		if seen[tn] != 1 {
			t.Fatalf("after 3 admissions tenant %s served %d times, want 1 (not equalized)", tn, seen[tn])
		}
	}

	// After t001 and t002 drain, remaining admissions come from t000 (still
	// backlogged) until the pool empties, totaling all 12 requests.
	total := 3
	for {
		if _, ok := a.next(); !ok {
			break
		}
		total++
	}
	if total != len(reqs) {
		t.Fatalf("VTC admitter emitted %d requests, want %d", total, len(reqs))
	}
}

// TestMeanCI checks the confidence-interval math against hand-computed values.
func TestMeanCI(t *testing.T) {
	// Zero variance -> zero half-width.
	if s := meanCI([]float64{1, 1, 1}); s.Mean != 1 || s.CI != 0 {
		t.Fatalf("constant sample: got mean=%v ci=%v, want 1,0", s.Mean, s.CI)
	}
	// Single sample -> mean only, no interval.
	if s := meanCI([]float64{5}); s.Mean != 5 || s.CI != 0 {
		t.Fatalf("single sample: got mean=%v ci=%v, want 5,0", s.Mean, s.CI)
	}
	// Two samples {2,4}: mean 3, sd sqrt(2), sem 1, t(df=1)=12.706 -> CI 12.706.
	s := meanCI([]float64{2, 4})
	if math.Abs(s.Mean-3) > 1e-9 {
		t.Fatalf("mean: got %v want 3", s.Mean)
	}
	if math.Abs(s.CI-12.706) > 1e-3 {
		t.Fatalf("ci: got %v want ~12.706", s.CI)
	}
}

// TestSummarizePreservesPolicyOrderAndAveraging pins aggregation: policy reporting
// order is preserved and per-policy means are correct across seeds.
func TestSummarizePreservesPolicyOrderAndAveraging(t *testing.T) {
	results := []SeedResult{
		{Seed: 1, Policies: []PolicyResult{
			{Policy: "cache-affinity", HitRate: 0.8, ServiceGap: 100, Valid: true, Backlogged: true},
			{Policy: "vtc-cache-blind", HitRate: 0.2, ServiceGap: 10, Valid: true, Backlogged: true},
		}},
		{Seed: 2, Policies: []PolicyResult{
			{Policy: "cache-affinity", HitRate: 0.6, ServiceGap: 200, Valid: true, Backlogged: true},
			{Policy: "vtc-cache-blind", HitRate: 0.4, ServiceGap: 20, Valid: true, Backlogged: false},
		}},
	}
	aggs := Summarize(results)
	if len(aggs) != 2 {
		t.Fatalf("want 2 aggregates, got %d", len(aggs))
	}
	if aggs[0].Policy != "cache-affinity" || aggs[1].Policy != "vtc-cache-blind" {
		t.Fatalf("policy order not preserved: %s, %s", aggs[0].Policy, aggs[1].Policy)
	}
	if math.Abs(aggs[0].HitRate.Mean-0.7) > 1e-9 {
		t.Fatalf("cache-affinity mean hit: got %v want 0.7", aggs[0].HitRate.Mean)
	}
	if math.Abs(aggs[0].Gap.Mean-150) > 1e-9 {
		t.Fatalf("cache-affinity mean gap: got %v want 150", aggs[0].Gap.Mean)
	}
	// Backlogged is AND across seeds: cache-affinity all true, vtc has a false.
	if !aggs[0].Backlogged {
		t.Fatal("cache-affinity should be backlogged on all seeds")
	}
	if aggs[1].Backlogged {
		t.Fatal("vtc-cache-blind had a non-backlogged seed; aggregate must reflect it")
	}
}

// TestSummarizeExcludesInvalidSeeds is the honesty guard against averaging crashed
// seeds. A seed whose policy never reached the snapshot (Valid=false, hit=0, gap=0)
// must be excluded from the mean, not diluted in as a real (0,0) point, and it must
// be counted in the valid/total tally so the reliability of the row is visible.
func TestSummarizeExcludesInvalidSeeds(t *testing.T) {
	results := []SeedResult{
		{Seed: 1, Policies: []PolicyResult{{Policy: "p", HitRate: 0.9, ServiceGap: 100, NormalizedGap: 2, Completed: 400, Valid: true, Backlogged: true}}},
		{Seed: 2, Policies: []PolicyResult{{Policy: "p", HitRate: 0.9, ServiceGap: 100, NormalizedGap: 2, Completed: 400, Valid: true, Backlogged: true}}},
		{Seed: 3, Policies: []PolicyResult{{Policy: "p", HitRate: 0, ServiceGap: 0, NormalizedGap: 0, Completed: 0, Valid: false}}},
	}
	aggs := Summarize(results)
	if len(aggs) != 1 {
		t.Fatalf("want 1 aggregate, got %d", len(aggs))
	}
	a := aggs[0]
	if a.TotalSeeds != 3 || a.ValidSeeds != 2 {
		t.Fatalf("seed tally: got valid=%d total=%d, want 2/3", a.ValidSeeds, a.TotalSeeds)
	}
	if math.Abs(a.HitRate.Mean-0.9) > 1e-9 {
		t.Fatalf("hit mean must exclude the crashed seed: got %v, want 0.9", a.HitRate.Mean)
	}
	if math.Abs(a.Gap.Mean-100) > 1e-9 {
		t.Fatalf("gap mean must exclude the crashed seed: got %v, want 100", a.Gap.Mean)
	}
}

// TestReliableThreshold pins the rule that decides whether a policy's row can be
// trusted as a frontier point: enough valid seeds in both absolute and fractional
// terms. A policy that crashed most of its seeds is unreliable and must not be
// placed on the frontier.
func TestReliableThreshold(t *testing.T) {
	if reliable(8, 20) {
		t.Fatal("8/20 valid must be unreliable")
	}
	if reliable(2, 2) {
		t.Fatal("2 valid seeds is too few for a confidence interval")
	}
	if !reliable(15, 20) {
		t.Fatal("15/20 = 75% must be reliable")
	}
	if !reliable(19, 20) {
		t.Fatal("19/20 valid must be reliable")
	}
}

// TestWriteCaveatsRegimeDisclosure pins that the regime caveat fires the emergent
// slot-affinity warning exactly when the active-tenant count equals the total slot
// count (the phase-lock regime), and drops it otherwise. This is the disclosure the
// headline's generalization depends on.
func TestWriteCaveatsRegimeDisclosure(t *testing.T) {
	locked := Config{Trace: trace.Spec{Tenants: 8}, Fleet: worker.FleetSpec{N: 4, Slots: 2}} // 8 == 4*2
	var b1 strings.Builder
	writeCaveats(&b1, locked)
	if !strings.Contains(b1.String(), "lower bound") || !strings.Contains(b1.String(), "emergent slot-affinity") {
		t.Fatalf("tenants==slots must warn about emergent slot-affinity and a lower-bound separation; got:\n%s", b1.String())
	}

	broken := Config{Trace: trace.Spec{Tenants: 12}, Fleet: worker.FleetSpec{N: 4, Slots: 2}} // 12 != 8
	var b2 strings.Builder
	writeCaveats(&b2, broken)
	if strings.Contains(b2.String(), "emergent slot-affinity") {
		t.Fatalf("tenants!=slots must NOT claim the phase-lock warning; got:\n%s", b2.String())
	}
	if !strings.Contains(b2.String(), "exceeds the aggregate KV capacity") {
		t.Fatalf("working set > capacity should be disclosed; got:\n%s", b2.String())
	}
}

// TestNormalizedGap checks the dimensionless gap normalization.
func TestNormalizedGap(t *testing.T) {
	served := map[string]float64{"a": 100, "b": 300} // mean 200
	if got := normalizedGap(100, served, 2); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("normalized gap: got %v want 0.5", got)
	}
	if got := normalizedGap(50, map[string]float64{}, 0); got != 0 {
		t.Fatalf("empty served: got %v want 0", got)
	}
}

// TestTTFTStats checks percentile and mean extraction.
func TestTTFTStats(t *testing.T) {
	ds := []time.Duration{10, 20, 30, 40, 50}
	p50, p95, mean := ttftStats(ds)
	if p50 != 30 {
		t.Fatalf("p50: got %v want 30", p50)
	}
	if p95 != 50 {
		t.Fatalf("p95: got %v want 50", p95)
	}
	if mean != 30 {
		t.Fatalf("mean: got %v want 30", mean)
	}
	if a, b, c := ttftStats(nil); a != 0 || b != 0 || c != 0 {
		t.Fatalf("empty: got %v %v %v want zeros", a, b, c)
	}
}

// TestPairedNormGapDiff checks the paired per-seed gap difference, the test the
// frontier-shape conclusion rests on. Pairing must cancel the seed-common swing:
// two seeds whose absolute gaps differ wildly but whose per-seed difference is
// stable should yield a tight paired mean.
func TestPairedNormGapDiff(t *testing.T) {
	results := []SeedResult{
		{Seed: 1, Policies: []PolicyResult{
			{Policy: "consistent-hash", NormalizedGap: 3.12, Valid: true},
			{Policy: "vtc-cache-blind", NormalizedGap: 0.08, Valid: true},
		}},
		{Seed: 2, Policies: []PolicyResult{
			{Policy: "consistent-hash", NormalizedGap: 2.48, Valid: true},
			{Policy: "vtc-cache-blind", NormalizedGap: 0.08, Valid: true},
		}},
	}
	// Per-seed diffs: 3.04 and 2.40, mean 2.72.
	got := pairedNormGapDiff(results, "consistent-hash", "vtc-cache-blind")
	if math.Abs(got.Mean-2.72) > 1e-9 {
		t.Fatalf("paired mean: got %v want 2.72", got.Mean)
	}
	if got.CI <= 0 {
		t.Fatalf("paired CI should be positive for two differing seeds, got %v", got.CI)
	}
	// A seed missing one policy must be skipped, not panic.
	results = append(results, SeedResult{Seed: 3, Policies: []PolicyResult{
		{Policy: "consistent-hash", NormalizedGap: 9, Valid: true},
	}})
	if got2 := pairedNormGapDiff(results, "consistent-hash", "vtc-cache-blind"); got2.Mean != got.Mean {
		t.Fatalf("incomplete seed should be skipped: mean changed %v -> %v", got.Mean, got2.Mean)
	}
	// A seed where one policy crashed (Valid=false) must also be skipped: pairing an
	// invalid (0,0) point would fabricate a difference. Adding such a seed must not
	// move the paired mean.
	results = append(results, SeedResult{Seed: 4, Policies: []PolicyResult{
		{Policy: "consistent-hash", NormalizedGap: 0, Valid: false},
		{Policy: "vtc-cache-blind", NormalizedGap: 0.08, Valid: true},
	}})
	if got3 := pairedNormGapDiff(results, "consistent-hash", "vtc-cache-blind"); math.Abs(got3.Mean-got.Mean) > 1e-9 {
		t.Fatalf("invalid seed should be skipped: mean changed %v -> %v", got.Mean, got3.Mean)
	}
}

// TestAllBacklogged checks the honesty guard for VTC's backlogged assumption: a
// trace whose tail tenant has more than a fair share of the served count is
// backlogged; one whose tail is thinner than the fair share is not.
func TestAllBacklogged(t *testing.T) {
	// 3 tenants, tail (t002) has 5 requests. Fair share at served=9 is 3, so 5>3 is
	// backlogged.
	var reqs []trace.Request
	for i := 0; i < 20; i++ {
		reqs = append(reqs, trace.Request{Tenant: "t000"})
	}
	for i := 0; i < 10; i++ {
		reqs = append(reqs, trace.Request{Tenant: "t001"})
	}
	for i := 0; i < 5; i++ {
		reqs = append(reqs, trace.Request{Tenant: "t002"})
	}
	tenants := []string{"t000", "t001", "t002"}
	if !allBacklogged(reqs, tenants, 9, nil) {
		t.Fatal("tail 5 > fair share 3 at served=9 should be backlogged")
	}
	// At served=30, fair share is 10 > tail 5, so not backlogged.
	if allBacklogged(reqs, tenants, 30, nil) {
		t.Fatal("tail 5 < fair share 10 at served=30 should not be backlogged")
	}
}
