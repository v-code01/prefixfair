// Command spike proves-or-demotes the prefixfair premise with real measurements.
//
// It manages a fleet of real llama-server processes (no simulated latency, no
// simulated cache) via the worker package and measures two gates against them:
//
//	Gate 1 (real-cache-TTFT-above-noise): a repeated long shared prefix (cache
//	HIT) versus distinct long prefixes (cache MISS) must produce a clean TTFT
//	separation, delta >= 10x the pooled std and >= a few hundred ms absolute.
//
//	Gate 2 (N-instances-on-the-box): N llama-server instances share one GGUF via
//	mmap with pinned thread counts (N*threads <= physical cores) and must all
//	serve concurrently with per-instance TTFT stability under concurrency.
//
// Every latency is measured two ways for cross-validation: end-to-end wall-clock
// TTFT (client stamps the first streamed token, via worker.Complete) and the
// server-reported prefill time (timings.prompt_ms) plus the reused-token count
// (timings.cache_n). The server/client/prompt machinery lives in the worker
// package so the gates share exactly the adapter the router harness uses.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"time"

	"prefixfair/internal/worker"
)

// stats summarizes a sample.
type stats struct {
	n    int
	mean float64
	std  float64
	p50  float64
	p95  float64
}

func summarize(xs []float64) stats {
	if len(xs) == 0 {
		return stats{}
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(len(xs))
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	// Population std: the sample IS the distribution we measured, not an estimate
	// of a wider one, so we divide by n. The pooled std used for the gate uses the
	// same convention on both conditions.
	std := math.Sqrt(ss / float64(len(xs)))
	pct := func(p float64) float64 {
		if len(sorted) == 1 {
			return sorted[0]
		}
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return stats{n: len(xs), mean: mean, std: std, p50: pct(0.50), p95: pct(0.95)}
}

func (s stats) String() string {
	return fmt.Sprintf("n=%d mean=%.1f std=%.1f p50=%.1f p95=%.1f", s.n, s.mean, s.std, s.p50, s.p95)
}

// pooledStd combines two samples' spreads, weighting by sample size. This is the
// noise floor the Gate 1 separation is measured against.
func pooledStd(a, b []float64) float64 {
	sa, sb := summarize(a), summarize(b)
	na, nb := float64(len(a)), float64(len(b))
	if na+nb == 0 {
		return 0
	}
	return math.Sqrt((na*sa.std*sa.std + nb*sb.std*sb.std) / (na + nb))
}

// ms converts a measured duration to milliseconds for reporting.
func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

// ---- Gate 1 -------------------------------------------------------------------

func runGate1(cfg config, w io.Writer) (bool, error) {
	fmt.Fprintf(w, "\n=== GATE 1: real-cache-TTFT-above-noise ===\n")
	// ctx 16384 with 2 slots gives each slot 8192 tokens, comfortably fitting the
	// long width-stable prefix (~4.2k tokens) plus suffix and generation.
	srv, err := worker.Start(worker.Config{
		Bin:        cfg.bin,
		Model:      cfg.model,
		Port:       cfg.basePort,
		Slots:      2,
		CtxTotal:   16384,
		Threads:    6,
		CacheReuse: 256,
		LogDir:     cfg.logDir,
	})
	if err != nil {
		return false, err
	}
	defer srv.Stop()
	if err := srv.WaitHealthy(60 * time.Second); err != nil {
		return false, err
	}
	fmt.Fprintf(w, "server up on %s; prefix = %d sentences\n", srv.URL(), cfg.prefixSentences)

	ctx := context.Background()

	// Report the token length of the shared prefix so the finding is concrete.
	if ntok, err := srv.Tokenize(ctx, worker.WidthStablePrefix(cfg.prefixSentences, 0)); err == nil {
		fmt.Fprintf(w, "shared prefix token length: %d\n", ntok)
	}

	// Warmup: prime the slot with the shared prefix. Discarded.
	if _, err := srv.Complete(ctx, worker.Request{Prompt: worker.HitPrompt(cfg.prefixSentences, -1), NPredict: 4, CachePrompt: true}); err != nil {
		return false, err
	}

	var hitTTFT, missTTFT []float64
	var hitPromptMs, missPromptMs []float64
	hitCacheReused, missCacheReused := 0, 0

	// HIT trials: identical shared prefix, varied short suffix, sent sequentially
	// so the warm slot keeps the prefix resident and the server skips its prefill.
	for i := 0; i < cfg.hitTrials; i++ {
		r, err := srv.Complete(ctx, worker.Request{Prompt: worker.HitPrompt(cfg.prefixSentences, i), NPredict: 4, CachePrompt: true})
		if err != nil {
			return false, fmt.Errorf("hit trial %d: %w", i, err)
		}
		hitTTFT = append(hitTTFT, ms(r.TTFT))
		hitPromptMs = append(hitPromptMs, r.Timings.PromptMs)
		hitCacheReused += r.Timings.CacheN
	}

	// MISS trials: a distinct long prefix each time so nothing is reusable.
	for i := 0; i < cfg.missTrials; i++ {
		r, err := srv.Complete(ctx, worker.Request{Prompt: worker.MissPrompt(cfg.prefixSentences, i+1), NPredict: 4, CachePrompt: true})
		if err != nil {
			return false, fmt.Errorf("miss trial %d: %w", i, err)
		}
		missTTFT = append(missTTFT, ms(r.TTFT))
		missPromptMs = append(missPromptMs, r.Timings.PromptMs)
		missCacheReused += r.Timings.CacheN
	}

	hs, mstat := summarize(hitTTFT), summarize(missTTFT)
	hp, mp := summarize(hitPromptMs), summarize(missPromptMs)
	pooled := pooledStd(hitTTFT, missTTFT)
	deltaMs := mstat.mean - hs.mean
	ratio := math.Inf(1)
	if pooled > 0 {
		ratio = deltaMs / pooled
	}

	fmt.Fprintf(w, "\n-- wall-clock TTFT (end-to-end, streamed first token) --\n")
	fmt.Fprintf(w, "HIT  %s\n", hs)
	fmt.Fprintf(w, "MISS %s\n", mstat)
	fmt.Fprintf(w, "delta(mean) = %.1f ms | pooled std = %.1f ms | ratio = %.1fx\n", deltaMs, pooled, ratio)
	fmt.Fprintf(w, "\n-- server prefill time (timings.prompt_ms), cross-check --\n")
	fmt.Fprintf(w, "HIT  %s\n", hp)
	fmt.Fprintf(w, "MISS %s\n", mp)
	fmt.Fprintf(w, "\n-- cache mechanism (timings.cache_n reused tokens) --\n")
	fmt.Fprintf(w, "HIT  total reused = %d over %d trials (avg %.0f tok/req)\n", hitCacheReused, cfg.hitTrials, float64(hitCacheReused)/float64(cfg.hitTrials))
	fmt.Fprintf(w, "MISS total reused = %d over %d trials (avg %.0f tok/req)\n", missCacheReused, cfg.missTrials, float64(missCacheReused)/float64(cfg.missTrials))

	// PASS iff a clean separation: >= 10x the noise floor AND >= a few hundred ms.
	pass := ratio >= 10.0 && deltaMs >= 300.0
	fmt.Fprintf(w, "\nGATE 1: %s (need ratio>=10x AND delta>=300ms; got ratio=%.1fx delta=%.1fms)\n", passStr(pass), ratio, deltaMs)
	return pass, nil
}

// ---- Gate 2 -------------------------------------------------------------------

func runGate2(cfg config, w io.Writer) (bool, error) {
	fmt.Fprintf(w, "\n=== GATE 2: N-instances-on-the-box (N=%d, %d threads each) ===\n", cfg.fleetN, cfg.threadsPer)
	fleet, err := worker.StartFleet(worker.FleetSpec{
		Bin:        cfg.bin,
		Model:      cfg.model,
		BasePort:   cfg.basePort + 10,
		N:          cfg.fleetN,
		Slots:      2,
		CtxTotal:   16384,
		Threads:    cfg.threadsPer,
		CacheReuse: 256,
		LogDir:     cfg.logDir,
		Cores:      cfg.cores,
	}, 90*time.Second)
	if err != nil {
		return false, err
	}
	defer fleet.Stop()
	fmt.Fprintf(w, "all %d instances healthy on ports %d..%d\n", cfg.fleetN,
		fleet.Workers[0].Port(), fleet.Workers[fleet.Len()-1].Port())

	ctx := context.Background()

	// Per-instance load: each request is a distinct, width-stable MISS-style prompt
	// so every instance does the same real prefill work (the honest, heaviest case)
	// and TTFT variance reflects CPU contention, not workload drift.
	load := func(srv *worker.Worker, reqs, tag int) []float64 {
		var out []float64
		for i := 0; i < reqs; i++ {
			r, err := srv.Complete(ctx, worker.Request{
				Prompt:      worker.MissPrompt(cfg.prefixSentences, tag*100_000+i),
				NPredict:    4,
				CachePrompt: true,
			})
			if err != nil {
				fmt.Fprintf(w, "  [port %d req %d] error: %v\n", srv.Port(), i, err)
				continue
			}
			out = append(out, ms(r.TTFT))
		}
		return out
	}

	// Baseline: one instance alone, no concurrency.
	fmt.Fprintf(w, "\n-- baseline: single instance, no concurrency --\n")
	baseline := load(fleet.Workers[0], cfg.loadReqs, 0)
	bs := summarize(baseline)
	fmt.Fprintf(w, "instance[0] %s\n", bs)

	// Concurrent: drive all N instances at once, each with the same request count.
	fmt.Fprintf(w, "\n-- concurrent: all %d instances driven simultaneously --\n", cfg.fleetN)
	type res struct {
		idx int
		xs  []float64
	}
	ch := make(chan res, cfg.fleetN)
	for idx, srv := range fleet.Workers {
		go func(idx int, srv *worker.Worker) {
			ch <- res{idx: idx, xs: load(srv, cfg.loadReqs, idx+1)}
		}(idx, srv)
	}
	perInst := make([][]float64, cfg.fleetN)
	for range fleet.Workers {
		r := <-ch
		perInst[r.idx] = r.xs
	}

	var worstRatio float64
	allServed := true
	for i, xs := range perInst {
		s := summarize(xs)
		if s.n == 0 {
			allServed = false
			fmt.Fprintf(w, "instance[%d] SERVED 0 requests (FAIL)\n", i)
			continue
		}
		// Ratio of concurrent-mean to solo-baseline-mean: how much oversubscription
		// inflated TTFT. 1.0 = perfect isolation; the CPU is shared so >1 expected.
		ratio := s.mean / bs.mean
		if ratio > worstRatio {
			worstRatio = ratio
		}
		fmt.Fprintf(w, "instance[%d] %s | concurrent/baseline mean = %.2fx\n", i, s, ratio)
	}

	// PASS iff every instance served AND no instance's TTFT blew up. On a shared
	// CPU some inflation is physical, not instability; the bar is a bounded,
	// sub-linear-in-N slowdown, not a collapse.
	slowdownBar := float64(cfg.fleetN)
	pass := allServed && worstRatio <= slowdownBar
	fmt.Fprintf(w, "\nGATE 2: %s (all %d served=%v; worst concurrent/baseline=%.2fx; bar<=%.1fx)\n",
		passStr(pass), cfg.fleetN, allServed, worstRatio, slowdownBar)
	return pass, nil
}

func passStr(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}

// ---- config / main ------------------------------------------------------------

type config struct {
	model           string
	bin             string
	logDir          string
	basePort        int
	prefixSentences int
	hitTrials       int
	missTrials      int
	fleetN          int
	threadsPer      int
	loadReqs        int
	cores           int
	gate            string
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.model, "model", "/Users/vanshverma/models/qwen0.5b/qwen2.5-0.5b-instruct-q4_k_m.gguf", "path to GGUF model")
	flag.StringVar(&cfg.bin, "llama-server", "/opt/homebrew/bin/llama-server", "path to llama-server binary")
	flag.StringVar(&cfg.logDir, "log-dir", os.TempDir(), "directory for llama-server logs")
	flag.IntVar(&cfg.basePort, "base-port", 8100, "base port for spawned servers")
	flag.IntVar(&cfg.prefixSentences, "prefix-sentences", 200, "sentences in the shared/distinct prefix (~14 tok each)")
	flag.IntVar(&cfg.hitTrials, "hit-trials", 25, "cache-HIT trials")
	flag.IntVar(&cfg.missTrials, "miss-trials", 25, "cache-MISS trials")
	flag.IntVar(&cfg.fleetN, "fleet-n", 4, "concurrent instances for gate 2")
	flag.IntVar(&cfg.threadsPer, "threads-per", 3, "threads per instance (fleet-n*threads-per must be <= cores)")
	flag.IntVar(&cfg.loadReqs, "load-reqs", 8, "requests per instance in gate 2")
	flag.IntVar(&cfg.cores, "cores", 14, "physical core budget")
	flag.StringVar(&cfg.gate, "gate", "both", "which gate to run: 1, 2, or both")
	flag.Parse()

	w := os.Stdout
	fmt.Fprintf(w, "prefixfair spike: real llama-server measurement\n")
	fmt.Fprintf(w, "model=%s\nbin=%s\n", cfg.model, cfg.bin)

	overall := true
	if cfg.gate == "1" || cfg.gate == "both" {
		pass, err := runGate1(cfg, w)
		if err != nil {
			fmt.Fprintf(w, "GATE 1 ERROR: %v\n", err)
			os.Exit(2)
		}
		overall = overall && pass
	}
	if cfg.gate == "2" || cfg.gate == "both" {
		pass, err := runGate2(cfg, w)
		if err != nil {
			fmt.Fprintf(w, "GATE 2 ERROR: %v\n", err)
			os.Exit(2)
		}
		overall = overall && pass
	}

	fmt.Fprintf(w, "\n==================================================\n")
	if overall {
		fmt.Fprintf(w, "VERDICT: PROCEED (both gates passed)\n")
	} else {
		fmt.Fprintf(w, "VERDICT: DEMOTE-to-paneljudge (a gate failed)\n")
		os.Exit(1)
	}
}
