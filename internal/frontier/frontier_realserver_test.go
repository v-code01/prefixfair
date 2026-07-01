package frontier

import (
	"context"
	"os"
	"testing"
	"time"

	"prefixfair/internal/trace"
	"prefixfair/internal/worker"
)

const (
	defaultBin   = "/opt/homebrew/bin/llama-server"
	defaultModel = "/Users/vanshverma/models/qwen0.5b/qwen2.5-0.5b-instruct-q4_k_m.gguf"
)

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

// TestFrontierRealBackendEndToEnd runs a small real sweep (a light N=2 fleet, one
// seed, short trace) and asserts the harness produces a sane frontier against live
// servers: every policy reaches the snapshot with real completions, cache and reuse
// rates are well-formed, the transient-retry path keeps real errors negligible, and
// VTC's scheduling yields the smallest cross-tenant service gap (which is its whole
// point). It is the automated proof that the measured path is real end to end.
func TestFrontierRealBackendEndToEnd(t *testing.T) {
	bin, model := binModel(t)

	fleetSpec := worker.DefaultFleetSpec()
	fleetSpec.Bin, fleetSpec.Model, fleetSpec.LogDir, fleetSpec.N = bin, model, t.TempDir(), 2
	// Disjoint from the worker package's real-server ports (8100, 8120+): `go test
	// ./...` runs package binaries in parallel, so this fleet must not bind the same
	// ports as the worker fleet or the two collide and one fails with a refused dial.
	fleetSpec.BasePort = 8110

	const k = 24
	cfg := Config{
		Fleet: fleetSpec,
		Trace: trace.Spec{
			Tenants:         4,
			ZipfS:           1.1,
			Requests:        160,
			PrefixSentences: 32,
		},
		Completions: k,
		NPredict:    16,
		HitFraction: worker.DefaultHitFraction,
		WIn:         1,
		WOut:        2,
	}

	seeds := trace.HeldOutSeeds(1)
	results, err := RunSweep(context.Background(), cfg, seeds, func() (*worker.Fleet, error) {
		return worker.StartFleet(fleetSpec, 90*time.Second)
	})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(results) != 1 || len(results[0].Policies) != 5 {
		t.Fatalf("want 1 seed x 5 policies, got %d seeds x %d policies", len(results), len(results[0].Policies))
	}

	byName := map[string]PolicyResult{}
	totalErrors := 0
	for _, pr := range results[0].Policies {
		byName[pr.Policy] = pr
		totalErrors += pr.Errors
		if pr.Completed != k {
			t.Errorf("%s: completed %d, want the full snapshot %d", pr.Policy, pr.Completed, k)
		}
		if pr.HitRate < 0 || pr.HitRate > 1 {
			t.Errorf("%s: hit rate %.3f out of [0,1]", pr.Policy, pr.HitRate)
		}
		if pr.MeanReuse < 0 || pr.MeanReuse > 1 {
			t.Errorf("%s: mean reuse %.3f out of [0,1]", pr.Policy, pr.MeanReuse)
		}
		if pr.TTFTp50 <= 0 {
			t.Errorf("%s: non-positive TTFT p50 %v (not a real measurement)", pr.Policy, pr.TTFTp50)
		}
	}

	// The transient-retry path must keep real errors negligible under load.
	if totalErrors > 4*k/10 {
		t.Errorf("too many real errors across policies: %d (retry path not holding)", totalErrors)
	}

	// The two fair-admission policies (VTC least-served scheduling: vtc-cache-blind
	// and the coupled fair-cache-affinity) must each achieve a smaller cross-tenant
	// service gap than every FIFO-admission placement policy. That separation is the
	// fairness axis the whole finding rests on, and it must hold regardless of which
	// placement the fair scheduler sits on top of.
	fairPolicies := []string{"vtc-cache-blind", "fair-cache-affinity"}
	fifoPolicies := []string{"cache-affinity", "jsq-d", "consistent-hash"}
	for _, fn := range fairPolicies {
		fair, ok := byName[fn]
		if !ok {
			t.Fatalf("%s result missing", fn)
		}
		for _, pn := range fifoPolicies {
			if fifo, ok := byName[pn]; ok && fair.ServiceGap > fifo.ServiceGap {
				t.Errorf("%s gap %.0f exceeds FIFO %s gap %.0f; fair admission should be fairer",
					fn, fair.ServiceGap, pn, fifo.ServiceGap)
			}
		}
	}
}
