package trace

import (
	"context"
	"os"
	"testing"
	"time"

	"prefixfair/internal/worker"
)

const (
	defaultBin   = "/opt/homebrew/bin/llama-server"
	defaultModel = "/Users/vanshverma/models/qwen0.5b/qwen2.5-0.5b-instruct-q4_k_m.gguf"
)

// binModel resolves the llama-server binary and model, honoring env overrides, and
// skips when either is absent so the suite stays portable off-box.
func binModel(t *testing.T) (bin, model string) {
	t.Helper()
	bin = os.Getenv("PREFIXFAIR_LLAMA_BIN")
	if bin == "" {
		bin = defaultBin
	}
	model = os.Getenv("PREFIXFAIR_MODEL")
	if model == "" {
		model = defaultModel
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("llama-server binary not found at %s; skipping real-backend test", bin)
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("model not found at %s; skipping real-backend test", model)
	}
	return bin, model
}

// TestTraceWithinPerSlotContext is the real token-budget guard: it tokenizes the
// generated prompts against a live server and asserts that the longest one, plus
// generation headroom, stays under the per-slot context budget. This is the honest
// version of the width-stability structural test: a prompt that overflows the
// budget is served zero tokens, which would silently corrupt the frontier.
func TestTraceWithinPerSlotContext(t *testing.T) {
	bin, model := binModel(t)

	// Use the real fleet config the frontier runs on, so the budget checked here is
	// the budget enforced there.
	spec := worker.DefaultFleetSpec()
	spec.Bin, spec.Model, spec.LogDir, spec.N = bin, model, t.TempDir(), 1
	cfg := worker.Config{
		Bin: bin, Model: model, Port: spec.BasePort,
		Slots: spec.Slots, CtxTotal: spec.CtxTotal, Threads: spec.Threads,
		CacheReuse: spec.CacheReuse, LogDir: spec.LogDir,
	}
	w, err := worker.Start(cfg)
	if err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(w.Stop)
	if err := w.WaitHealthy(90 * time.Second); err != nil {
		t.Fatalf("worker not healthy: %v", err)
	}

	ctx := context.Background()
	reqs := Generate(DefaultSpec())

	// The prefix is width-stable, so the longest prompt is the one with the longest
	// suffix; check the tenant prefix itself and the worst-case full prompt.
	perSlot := cfg.PerSlotCtx()
	const nPredict = 32 // the frontier's generation length

	// One prefix (all tenants share the same width, so any tenant's is worst-case
	// representative) plus the worst-case suffix in the trace.
	worst := reqs[0]
	for _, r := range reqs {
		if len(r.Prompt) > len(worst.Prompt) {
			worst = r
		}
	}

	prefixTok, err := w.Tokenize(ctx, worst.Prefix)
	if err != nil {
		t.Fatalf("tokenize prefix: %v", err)
	}
	promptTok, err := w.Tokenize(ctx, worst.Prompt)
	if err != nil {
		t.Fatalf("tokenize prompt: %v", err)
	}

	t.Logf("per-slot ctx=%d, prefix=%d tok, worst prompt=%d tok, +%d predict = %d",
		perSlot, prefixTok, promptTok, nPredict, promptTok+nPredict)

	// The shared prefix must be a substantial fraction of the prompt (so cache reuse
	// is meaningful) and land in the intended ~1-2k token band.
	if prefixTok < 800 || prefixTok > 2200 {
		t.Fatalf("prefix %d tokens outside the intended ~1-2k band", prefixTok)
	}
	// The whole prompt plus generation must fit the per-slot budget with margin.
	if promptTok+nPredict >= perSlot {
		t.Fatalf("worst prompt %d + %d predict >= per-slot ctx %d; would be rejected",
			promptTok, nPredict, perSlot)
	}
}
