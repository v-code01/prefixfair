package worker

import (
	"context"
	"testing"
	"time"
)

// TestWorkerAgainstRealServer exercises the adapter against a real llama-server
// on one model load. It proves the two facts the whole finding depends on at the
// single-worker level: a completion returns a real measured TTFT, and the server
// reports a real per-slot prefix-cache HIT for a repeated prefix and a MISS for a
// distinct one. Every assertion is against real server counters, not a model.
func TestWorkerAgainstRealServer(t *testing.T) {
	bin, model := binModel(t)

	// Slots 2, ctx 8192 -> 4096 tokens per slot, comfortably fitting the ~2.5k
	// token prefix used below plus its suffix and generation.
	w, err := Start(Config{
		Bin:        bin,
		Model:      model,
		Port:       8120,
		Slots:      2,
		CtxTotal:   8192,
		Threads:    4,
		CacheReuse: 256,
		LogDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Reap the process even if an assertion below fails. This is the no-orphans
	// guarantee for the whole test.
	t.Cleanup(w.Stop)

	if err := w.WaitHealthy(90 * time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	ctx := context.Background()
	const prefixSentences = 120

	// Token-level width stability against the real tokenizer: seed 0 and a distinct
	// seed must tokenize to the same length, so a miss prompt cannot silently
	// overflow the per-slot context relative to a hit prompt.
	shared := WidthStablePrefix(prefixSentences, 0)
	distinct := WidthStablePrefix(prefixSentences, 5)
	nShared, err := w.Tokenize(ctx, shared)
	if err != nil {
		t.Fatalf("Tokenize shared: %v", err)
	}
	nDistinct, err := w.Tokenize(ctx, distinct)
	if err != nil {
		t.Fatalf("Tokenize distinct: %v", err)
	}
	if nShared != nDistinct {
		t.Fatalf("width not token-stable: shared=%d distinct=%d tokens", nShared, nDistinct)
	}
	t.Logf("prefix token length (both seeds): %d", nShared)

	t.Run("CompleteMeasuresRealTTFT", func(t *testing.T) {
		res, err := w.Complete(ctx, Request{Prompt: "Reply with one word.", NPredict: 4})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if res.TTFT <= 0 {
			t.Fatalf("TTFT must be positive, got %v", res.TTFT)
		}
		if res.Tokens < 1 {
			t.Fatalf("expected >= 1 generated token, got %d", res.Tokens)
		}
		if res.Timings.PredictMs <= 0 {
			t.Fatalf("expected positive server predict time, got %v", res.Timings.PredictMs)
		}
		t.Logf("TTFT=%v tokens=%d prompt_ms=%.1f", res.TTFT, res.Tokens, res.Timings.PromptMs)
	})

	// The realness anchor: a repeated long prefix must yield a measured cache HIT
	// (reuse ~= prefix length) and a distinct prefix a MISS (reuse ~= 0). This
	// asserts against the server's own cache_n counter.
	t.Run("CacheHitVsMissDetection", func(t *testing.T) {
		// Warm the slot with the shared prefix (first touch is a full prefill).
		if _, err := w.Complete(ctx, Request{Prompt: HitPrompt(prefixSentences, 0), NPredict: 4, CachePrompt: true}); err != nil {
			t.Fatalf("warmup: %v", err)
		}

		// HIT: send the shared prefix again; the warm slot should skip its prefill.
		hit, err := w.Complete(ctx, Request{Prompt: HitPrompt(prefixSentences, 1), NPredict: 4, CachePrompt: true})
		if err != nil {
			t.Fatalf("hit request: %v", err)
		}
		// MISS: a distinct prefix shares nothing, so the server prefills it in full.
		miss, err := w.Complete(ctx, Request{Prompt: MissPrompt(prefixSentences, 777), NPredict: 4, CachePrompt: true})
		if err != nil {
			t.Fatalf("miss request: %v", err)
		}

		hitOutcome := ClassifyCache(hit.Timings, DefaultHitFraction)
		missOutcome := ClassifyCache(miss.Timings, DefaultHitFraction)
		t.Logf("HIT  cache_n=%d prompt_n=%d reuse=%.3f -> %s (prompt_ms=%.1f)",
			hit.Timings.CacheN, hit.Timings.PromptN, hit.Timings.ReuseFraction(), hitOutcome, hit.Timings.PromptMs)
		t.Logf("MISS cache_n=%d prompt_n=%d reuse=%.3f -> %s (prompt_ms=%.1f)",
			miss.Timings.CacheN, miss.Timings.PromptN, miss.Timings.ReuseFraction(), missOutcome, miss.Timings.PromptMs)

		if hitOutcome != CacheHit {
			t.Fatalf("repeated prefix did not register a cache HIT: reuse=%.3f cache_n=%d",
				hit.Timings.ReuseFraction(), hit.Timings.CacheN)
		}
		if missOutcome != CacheMiss {
			t.Fatalf("distinct prefix did not register a cache MISS: reuse=%.3f cache_n=%d",
				miss.Timings.ReuseFraction(), miss.Timings.CacheN)
		}
		// Reuse magnitude: the hit must reuse most of the prefix; the miss almost
		// none. This guards against a marginal classification passing on noise.
		if hit.Timings.CacheN < nShared/2 {
			t.Fatalf("hit reuse too small: cache_n=%d, want >= %d", hit.Timings.CacheN, nShared/2)
		}
		if float64(miss.Timings.CacheN) > 0.1*float64(nShared) {
			t.Fatalf("miss reused too much: cache_n=%d, want <= %d", miss.Timings.CacheN, nShared/10)
		}
		// The real prefill skip must also show up as time: a hit prefills far faster.
		if hit.Timings.PromptMs >= miss.Timings.PromptMs {
			t.Fatalf("hit prefill (%.1f ms) not faster than miss (%.1f ms)", hit.Timings.PromptMs, miss.Timings.PromptMs)
		}
	})
}
