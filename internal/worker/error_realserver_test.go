package worker

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExceedContextSurfacesError guards the spike's near-demote bug directly: a
// prompt that exceeds the per-slot context must come back as a surfaced error,
// NOT as a fast zero-token "success" that gets counted as a datapoint. A silent
// 0-token pass here would poison the whole frontier, so this is a load-bearing
// honesty test.
func TestExceedContextSurfacesError(t *testing.T) {
	bin, model := binModel(t)

	// ctx 1024 with 2 slots -> 512 tokens per slot, a deliberately tiny budget.
	cfg := Config{
		Bin:      bin,
		Model:    model,
		Port:     8122,
		Slots:    2,
		CtxTotal: 1024,
		Threads:  4,
		LogDir:   t.TempDir(),
	}
	w, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(w.Stop)
	if err := w.WaitHealthy(90 * time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}

	ctx := context.Background()

	// ~1700 tokens, well past the 512-token per-slot budget.
	oversized := WidthStablePrefix(80, 3)
	if n, err := w.Tokenize(ctx, oversized); err == nil {
		t.Logf("oversized prompt token length: %d (per-slot budget %d)", n, cfg.PerSlotCtx())
		if n <= cfg.PerSlotCtx() {
			t.Fatalf("test setup wrong: prompt %d tokens does not exceed per-slot budget %d", n, cfg.PerSlotCtx())
		}
	}

	res, rejErr := w.Complete(ctx, Request{Prompt: oversized, NPredict: 8, CachePrompt: false})
	if rejErr == nil {
		t.Fatalf("oversized prompt returned no error; got %d tokens, TTFT %v (silent 0-token success is the bug)",
			res.Tokens, res.TTFT)
	}
	t.Logf("oversized prompt correctly surfaced: %v", rejErr)

	// A control request within budget must still succeed on the same worker,
	// proving the worker was rejecting the oversized prompt, not simply broken.
	ctl, err := w.Complete(ctx, Request{Prompt: "Say hi.", NPredict: 4})
	if err != nil {
		t.Fatalf("control request after rejection failed: %v", err)
	}
	if ctl.TTFT <= 0 || ctl.Tokens < 1 {
		t.Fatalf("control request degenerate: TTFT=%v tokens=%d", ctl.TTFT, ctl.Tokens)
	}

	// Sanity: the rejection should reference the real server error, not a client
	// timeout or connection drop.
	if !strings.Contains(strings.ToLower(rejErr.Error()), "context") && !strings.Contains(rejErr.Error(), "status") {
		t.Logf("note: rejection error did not mention context/status: %v", rejErr)
	}
}
