// Command frontier replays the multi-tenant Zipfian trace against the real N=4
// llama-server fleet under each of the four routing policies and writes the
// measured cache-hit / cross-tenant service-gap frontier, with confidence intervals
// over held-out seeds, to a markdown report.
//
// It is the authoritative measurement entry point. A short smoke run (-smoke, one
// or two seeds) proves the harness produces a real frontier in a couple of minutes;
// the full multi-seed sweep is what the reported finding rests on.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"prefixfair/internal/frontier"
	"prefixfair/internal/trace"
	"prefixfair/internal/worker"
)

func main() {
	var (
		bin      = flag.String("bin", "/opt/homebrew/bin/llama-server", "llama-server binary")
		model    = flag.String("model", "/Users/vanshverma/models/qwen0.5b/qwen2.5-0.5b-instruct-q4_k_m.gguf", "shared GGUF")
		logDir   = flag.String("logdir", "", "llama-server log directory (default: a temp dir)")
		out      = flag.String("out", "bench_results/frontier.md", "output markdown path")
		seedN    = flag.Int("seeds", 20, "number of held-out seeds to sweep")
		tenants  = flag.Int("tenants", 8, "number of tenants")
		zipf     = flag.Float64("zipf", 1.1, "Zipf skew exponent (>1)")
		requests = flag.Int("requests", 2000, "requests per trace seed (the backlogged pool)")
		prefix   = flag.Int("prefix-sentences", 64, "shared prefix length in sentences (~21 tok each)")
		k        = flag.Int("completions", 400, "snapshot completion count K (must stay inside backlog)")
		npredict = flag.Int("npredict", 32, "generation length per request")
		wIn      = flag.Float64("win", 1.0, "VTC input-token cost weight")
		wOut     = flag.Float64("wout", 2.0, "VTC output-token cost weight")
		smoke    = flag.Bool("smoke", false, "small fast run to prove the harness (overrides sweep sizes)")
	)
	flag.Parse()

	if *smoke {
		*seedN = 1
		*requests = 500
		*k = 100
	}

	ld := *logDir
	if ld == "" {
		d, err := os.MkdirTemp("", "frontier-logs-")
		if err != nil {
			fail("temp log dir: %v", err)
		}
		ld = d
	}

	fleetSpec := worker.DefaultFleetSpec()
	fleetSpec.Bin, fleetSpec.Model, fleetSpec.LogDir = *bin, *model, ld

	cfg := frontier.Config{
		Fleet: fleetSpec,
		Trace: trace.Spec{
			Tenants:         *tenants,
			ZipfS:           *zipf,
			Requests:        *requests,
			PrefixSentences: *prefix,
		},
		Completions: *k,
		NPredict:    *npredict,
		HitFraction: worker.DefaultHitFraction,
		WIn:         *wIn,
		WOut:        *wOut,
	}

	seeds := trace.HeldOutSeeds(*seedN)

	fmt.Printf("frontier: %d policies x %d held-out seeds, K=%d completions, N=%d fleet\n",
		4, len(seeds), cfg.Completions, cfg.Fleet.N)
	fmt.Printf("frontier: one cold fleet per policy (4 fleet starts total)\n")

	start := time.Now()
	results, err := frontier.RunSweep(context.Background(), cfg, seeds, func() (*worker.Fleet, error) {
		return worker.StartFleet(fleetSpec, 120*time.Second)
	})
	if err != nil {
		fail("sweep: %v", err)
	}

	if err := frontier.WriteReport(*out, cfg, seeds, results); err != nil {
		fail("write report: %v", err)
	}
	fmt.Printf("frontier: done in %s -> %s\n", time.Since(start).Round(time.Second), *out)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "frontier: "+format+"\n", args...)
	os.Exit(1)
}
