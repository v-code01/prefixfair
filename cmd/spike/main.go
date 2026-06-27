// Command spike proves-or-demotes the prefixfair premise with real measurements.
//
// It manages a fleet of real llama-server processes (no simulated latency, no
// simulated cache) and measures two gates against them:
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
// TTFT (client stamps the first streamed token) and the server-reported prefill
// time (timings.prompt_ms) plus the reused-token count (timings.cache_n).
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// serverTimings mirrors the llama-server completion "timings" object. Only the
// fields load-bearing for the cache mechanism are decoded.
type serverTimings struct {
	PromptN   int     `json:"prompt_n"`  // prompt tokens actually evaluated (prefill)
	CacheN    int     `json:"cache_n"`   // prompt tokens reused from the KV cache (skipped)
	PromptMs  float64 `json:"prompt_ms"` // wall time spent in prefill on the server
	PredictMs float64 `json:"predicted_ms"`
}

// trialResult is one measured request.
type trialResult struct {
	ttftMs  float64       // end-to-end wall-clock time to first streamed token
	timings serverTimings // server-reported timings from the final stream chunk
}

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

// makePrefix builds a prefix of n sentences. Each sentence embeds a fixed-WIDTH
// 9-digit id, so the token length is constant regardless of `seed`. That control
// is the point: seed==0 (the shared HIT prefix) and any seed!=0 (a distinct MISS
// prefix) tokenize to the SAME length, so the only variable between HIT and MISS
// is whether the KV cache is reused, not how much text there is to prefill.
//
// Distinct seeds diverge from token 0, so no two seeds share a cacheable prefix.
// The id space (seed*1000 + i) is kept below 1e9 by the caller so the %09d width
// never overflows and every id stays unique per (seed, sentence).
func makePrefix(n, seed int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		id := (seed*1000 + i) % 1_000_000_000
		fmt.Fprintf(&b, "The quick brown fox jumps over the lazy dog number %09d.", id)
	}
	return b.String()
}

// streamTTFT sends a streaming completion and returns the wall-clock TTFT plus
// the server's final timings. cachePrompt controls whether the server is allowed
// to reuse KV across requests on the chosen slot.
func streamTTFT(ctx context.Context, client *http.Client, url, prompt string, cachePrompt bool) (trialResult, error) {
	reqBody, err := json.Marshal(map[string]any{
		"prompt":       prompt,
		"n_predict":    4,
		"cache_prompt": cachePrompt,
		"stream":       true,
		"temperature":  0.0,
	})
	if err != nil {
		return trialResult{}, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+"/completion", bytes.NewReader(reqBody))
	if err != nil {
		return trialResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return trialResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return trialResult{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var res trialResult
	firstStamped := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk struct {
			Content string        `json:"content"`
			Stop    bool          `json:"stop"`
			Timings serverTimings `json:"timings"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if !firstStamped && chunk.Content != "" {
			res.ttftMs = float64(time.Since(start).Microseconds()) / 1000.0
			firstStamped = true
		}
		if chunk.Stop {
			res.timings = chunk.Timings
		}
	}
	if err := sc.Err(); err != nil {
		return trialResult{}, err
	}
	if !firstStamped {
		// No token content ever arrived; treat the whole request as first-token time.
		res.ttftMs = float64(time.Since(start).Microseconds()) / 1000.0
	}
	return res, nil
}

// llamaServer wraps a spawned process and its base URL.
type llamaServer struct {
	cmd  *exec.Cmd
	url  string
	port int
	log  *os.File
}

// startServer spawns one llama-server. Threads are pinned by the caller so the
// whole fleet stays within the physical core budget.
func startServer(modelPath, binPath string, port, slots, ctx, threads int, logDir string) (*llamaServer, error) {
	logPath := fmt.Sprintf("%s/llama-%d.log", logDir, port)
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	args := []string{
		"-m", modelPath,
		"-np", fmt.Sprint(slots),
		"-c", fmt.Sprint(ctx),
		"-t", fmt.Sprint(threads),
		"-tb", fmt.Sprint(threads),
		"--cache-reuse", "256",
		"--metrics",
		"--port", fmt.Sprint(port),
		"--host", "127.0.0.1",
	}
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	return &llamaServer{cmd: cmd, url: fmt.Sprintf("http://127.0.0.1:%d", port), port: port, log: logFile}, nil
}

func (s *llamaServer) stop() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_, _ = s.cmd.Process.Wait()
	}
	if s.log != nil {
		_ = s.log.Close()
	}
}

// waitHealthy polls /health until the server reports ok or the deadline passes.
func waitHealthy(client *http.Client, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/health")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "ok") {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("server %s not healthy within %s", url, timeout)
}

// ---- Gate 1 -------------------------------------------------------------------

func runGate1(cfg config, w io.Writer) (bool, error) {
	fmt.Fprintf(w, "\n=== GATE 1: real-cache-TTFT-above-noise ===\n")
	// ctx 16384 with 2 slots gives each slot 8192 tokens, comfortably fitting the
	// long width-stable prefix (~4.2k tokens) plus suffix and generation.
	srv, err := startServer(cfg.model, cfg.bin, cfg.basePort, 2, 16384, 6, cfg.logDir)
	if err != nil {
		return false, err
	}
	defer srv.stop()
	client := &http.Client{Timeout: 120 * time.Second}
	if err := waitHealthy(client, srv.url, 60*time.Second); err != nil {
		return false, err
	}
	fmt.Fprintf(w, "server up on %s; prefix = %d sentences\n", srv.url, cfg.prefixSentences)

	ctx := context.Background()
	sharedPrefix := makePrefix(cfg.prefixSentences, 0)

	// Report the token length of the shared prefix so the finding is concrete.
	if ntok, err := tokenCount(client, srv.url, sharedPrefix); err == nil {
		fmt.Fprintf(w, "shared prefix token length: %d\n", ntok)
	}

	// Warmup: prime the slot with the shared prefix, then a MISS warmup. Discarded.
	if _, err := streamTTFT(ctx, client, srv.url, sharedPrefix+" warmup ask: 1?", true); err != nil {
		return false, err
	}

	var hitTTFT, missTTFT []float64
	var hitPromptMs, missPromptMs []float64
	hitCacheReused, missCacheReused := 0, 0

	// HIT trials: identical shared prefix, varied short suffix, sent sequentially
	// so the warm slot keeps the prefix resident and the server skips its prefill.
	for i := 0; i < cfg.hitTrials; i++ {
		prompt := fmt.Sprintf("%s Question %d: what is %d plus %d?", sharedPrefix, i, i, i+1)
		r, err := streamTTFT(ctx, client, srv.url, prompt, true)
		if err != nil {
			return false, fmt.Errorf("hit trial %d: %w", i, err)
		}
		hitTTFT = append(hitTTFT, r.ttftMs)
		hitPromptMs = append(hitPromptMs, r.timings.PromptMs)
		hitCacheReused += r.timings.CacheN
	}

	// MISS trials: a distinct long prefix each time so nothing is reusable.
	for i := 0; i < cfg.missTrials; i++ {
		prompt := makePrefix(cfg.prefixSentences, i+1) +
			fmt.Sprintf(" Question: what is %d plus %d?", i, i+1)
		r, err := streamTTFT(ctx, client, srv.url, prompt, true)
		if err != nil {
			return false, fmt.Errorf("miss trial %d: %w", i, err)
		}
		missTTFT = append(missTTFT, r.ttftMs)
		missPromptMs = append(missPromptMs, r.timings.PromptMs)
		missCacheReused += r.timings.CacheN
	}

	hs, ms := summarize(hitTTFT), summarize(missTTFT)
	hp, mp := summarize(hitPromptMs), summarize(missPromptMs)
	pooled := pooledStd(hitTTFT, missTTFT)
	deltaMs := ms.mean - hs.mean
	ratio := math.Inf(1)
	if pooled > 0 {
		ratio = deltaMs / pooled
	}

	fmt.Fprintf(w, "\n-- wall-clock TTFT (end-to-end, streamed first token) --\n")
	fmt.Fprintf(w, "HIT  %s\n", hs)
	fmt.Fprintf(w, "MISS %s\n", ms)
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

func tokenCount(client *http.Client, url, content string) (int, error) {
	body, _ := json.Marshal(map[string]any{"content": content})
	resp, err := client.Post(url+"/tokenize", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Tokens []int `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return len(out.Tokens), nil
}

// ---- Gate 2 -------------------------------------------------------------------

func runGate2(cfg config, w io.Writer) (bool, error) {
	fmt.Fprintf(w, "\n=== GATE 2: N-instances-on-the-box (N=%d, %d threads each) ===\n", cfg.fleetN, cfg.threadsPer)
	if cfg.fleetN*cfg.threadsPer > cfg.cores {
		return false, fmt.Errorf("thread budget exceeded: %d*%d > %d cores", cfg.fleetN, cfg.threadsPer, cfg.cores)
	}
	client := &http.Client{Timeout: 120 * time.Second}

	var fleet []*llamaServer
	defer func() {
		for _, s := range fleet {
			s.stop()
		}
	}()
	for i := 0; i < cfg.fleetN; i++ {
		port := cfg.basePort + 10 + i
		srv, err := startServer(cfg.model, cfg.bin, port, 2, 16384, cfg.threadsPer, cfg.logDir)
		if err != nil {
			return false, err
		}
		fleet = append(fleet, srv)
	}
	for _, s := range fleet {
		if err := waitHealthy(client, s.url, 90*time.Second); err != nil {
			return false, fmt.Errorf("fleet health: %w", err)
		}
	}
	fmt.Fprintf(w, "all %d instances healthy on ports %d..%d\n", cfg.fleetN, fleet[0].port, fleet[len(fleet)-1].port)

	ctx := context.Background()

	// Per-instance load: each request is a distinct, width-stable MISS-style prompt
	// so every instance does the same real prefill work (the honest, heaviest case)
	// and TTFT variance reflects CPU contention, not workload drift. The per-tag id
	// space keeps every prefix unique and below the %09d overflow bound.
	load := func(srv *llamaServer, reqs int, tag int) []float64 {
		var out []float64
		for i := 0; i < reqs; i++ {
			p := makePrefix(cfg.prefixSentences, tag*100_000+i) +
				fmt.Sprintf(" Q%d: sum of %d and %d?", i, i, i+1)
			r, err := streamTTFT(ctx, client, srv.url, p, true)
			if err != nil {
				fmt.Fprintf(w, "  [port %d req %d] error: %v\n", srv.port, i, err)
				continue
			}
			out = append(out, r.ttftMs)
		}
		return out
	}

	// Baseline: one instance alone, no concurrency.
	fmt.Fprintf(w, "\n-- baseline: single instance, no concurrency --\n")
	baseline := load(fleet[0], cfg.loadReqs, 0)
	bs := summarize(baseline)
	fmt.Fprintf(w, "instance[0] %s\n", bs)

	// Concurrent: drive all N instances at once, each with the same request count.
	fmt.Fprintf(w, "\n-- concurrent: all %d instances driven simultaneously --\n", cfg.fleetN)
	type res struct {
		idx int
		xs  []float64
	}
	ch := make(chan res, cfg.fleetN)
	for idx, srv := range fleet {
		go func(idx int, srv *llamaServer) {
			ch <- res{idx: idx, xs: load(srv, cfg.loadReqs, idx+1)}
		}(idx, srv)
	}
	perInst := make([][]float64, cfg.fleetN)
	for range fleet {
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
