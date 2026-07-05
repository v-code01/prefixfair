package worker

import "testing"

// TestReuseFraction pins the cache arithmetic: reuse over total prompt tokens.
func TestReuseFraction(t *testing.T) {
	cases := []struct {
		name string
		tm   Timings
		want float64
	}{
		{"full miss", Timings{PromptN: 2000, CacheN: 0}, 0.0},
		{"full hit", Timings{PromptN: 1, CacheN: 1999}, 1999.0 / 2000.0},
		{"half", Timings{PromptN: 1000, CacheN: 1000}, 0.5},
		{"empty", Timings{}, 0.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.tm.ReuseFraction()
			if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("ReuseFraction()=%v want %v", got, c.want)
			}
		})
	}
}

// TestClassifyCache pins the HIT/MISS decision around the reuse bar.
func TestClassifyCache(t *testing.T) {
	// A near-full prefix skip is a HIT.
	if got := ClassifyCache(Timings{PromptN: 3, CacheN: 2100}, DefaultHitFraction); got != CacheHit {
		t.Fatalf("near-full reuse classified %v, want HIT", got)
	}
	// A near-full prefill is a MISS.
	if got := ClassifyCache(Timings{PromptN: 2100, CacheN: 8}, DefaultHitFraction); got != CacheMiss {
		t.Fatalf("near-zero reuse classified %v, want MISS", got)
	}
	// Exactly at the bar counts as a HIT (inclusive).
	if got := ClassifyCache(Timings{PromptN: 100, CacheN: 100}, DefaultHitFraction); got != CacheHit {
		t.Fatalf("at-bar reuse classified %v, want HIT", got)
	}
}

// TestPerSlotCtx pins the per-slot budget arithmetic that keeps prompts legal.
func TestPerSlotCtx(t *testing.T) {
	if got := (Config{CtxTotal: 16384, Slots: 2}).PerSlotCtx(); got != 8192 {
		t.Fatalf("PerSlotCtx=%d want 8192", got)
	}
	// Slots unset must not divide by zero.
	if got := (Config{CtxTotal: 4096, Slots: 0}).PerSlotCtx(); got != 4096 {
		t.Fatalf("PerSlotCtx with 0 slots=%d want 4096", got)
	}
}

// TestWidthStablePrefixConstantWidth is the pure-unit half of the spike's
// near-demote lesson: the prefix byte length must not depend on the seed, so no
// seed can silently balloon a prompt past the per-slot context. The token-level
// version of this invariant is asserted against a real tokenizer in the
// integration test.
func TestWidthStablePrefixConstantWidth(t *testing.T) {
	const n = 120
	base := len(WidthStablePrefix(n, 0))
	for _, seed := range []int{1, 7, 42, 999, 123456} {
		if got := len(WidthStablePrefix(n, seed)); got != base {
			t.Fatalf("WidthStablePrefix(%d,%d) byte len=%d, want %d (width not seed-stable)", n, seed, got, base)
		}
	}
	// Distinct seeds must actually differ, or "distinct miss" prefixes would
	// collide and reuse each other's cache.
	if WidthStablePrefix(n, 0) == WidthStablePrefix(n, 1) {
		t.Fatal("distinct seeds produced identical prefixes")
	}
}

// TestStartValidatesConfig rejects an empty binary or model rather than spawning
// a doomed process.
func TestStartValidatesConfig(t *testing.T) {
	if _, err := Start(Config{Port: 8199}); err == nil {
		t.Fatal("Start without Bin/Model must error")
	}
}
