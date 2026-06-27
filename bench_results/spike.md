# prefixfair spike results

Two mandatory gates, measured against real `llama-server` processes on the tiny
Qwen2.5-0.5B-Instruct Q4_K_M GGUF (mmap-shared). CPU-only, M4 Pro (10P+4E, 48GB).
No simulated latency, no simulated cache: every number below comes from a real
server slot.

Reproduce:

```
go run ./cmd/spike -gate both -prefix-sentences 200 \
  -hit-trials 25 -miss-trials 25 -fleet-n 4 -threads-per 3 -load-reqs 8
```

## Gate 1 - real-cache-TTFT-above-noise: PASS

One server (`-np 2 -c 16384 -t 6 --cache-reuse 256`). HIT sends one identical
shared prefix repeatedly (the server skips its prefill); MISS sends a distinct
prefix each time. Both prefixes are width-stable (fixed 9-digit sentence ids), so
HIT and MISS tokenize to the SAME length and the only variable is cache reuse.

- Shared prefix length: **4200 tokens**.
- Trials: 25 HIT, 25 MISS, plus a discarded warmup.

| condition | wall-clock TTFT mean | std | p50 | p95 | server prefill mean |
|-----------|---------------------|-----|-----|-----|---------------------|
| HIT (cache reused)  | 16.8 ms  | 0.4 ms  | 16.9 ms  | 17.4 ms  | 14.8 ms  |
| MISS (full prefill) | 738.3 ms | 18.1 ms | 728.3 ms | 767.1 ms | 732.2 ms |

- Separation: **delta = 721.6 ms**, pooled std = 12.8 ms, **ratio = 56.2x**.
- Gate bar: ratio >= 10x AND delta >= 300 ms. Both cleared with wide margin.
- Mechanism cross-check (`timings.cache_n`, tokens reused from KV): HIT reuses
  **4202 tok/req** (the entire prefix); MISS reuses **15 tok/req**. The TTFT gap
  is the real per-slot prefix skip, not noise.

## Gate 2 - N-instances-on-the-box: PASS at N=4

Four servers on ports 8110-8113, 3 threads each (4*3 = 12 <= 14 cores), one GGUF
mmap-shared. Each request is a distinct width-stable full-prefill (the heaviest,
honest workload). Baseline = one instance alone; concurrent = all four driven
simultaneously.

| instance | concurrent TTFT mean | std | p95 | concurrent / baseline |
|----------|---------------------|-----|-----|-----------------------|
| baseline (solo) | 760.1 ms | 67.5 ms | 934.3 ms | 1.00x |
| instance[0] | 2590.8 ms | 56.6 ms  | 2701.0 ms | 3.41x |
| instance[1] | 2532.2 ms | 75.7 ms  | 2688.0 ms | 3.33x |
| instance[2] | 2579.6 ms | 65.7 ms  | 2719.6 ms | 3.39x |
| instance[3] | 2583.9 ms | 152.6 ms | 2931.2 ms | 3.40x |

- All 4 instances served every request concurrently.
- Per-instance TTFT is stable: coefficient of variation 2-6%, and the four
  instances agree to within 0.08x of each other (3.33-3.41x). No instance is
  starved; the slowdown is uniform, fair CPU-sharing, not instability.
- The ~3.4x per-request inflation at 4-way concurrency is the physical cost of 12
  prefill threads on 14 cores; it is deterministic and bounded (under the <=N bar),
  so the hit%-vs-fairness frontier is measurable at N=4.

**Working fleet size: N = 4, 3 threads each, ctx 16384 (8192 per slot).**

## Decision

**VERDICT: PROCEED.** Both gates pass with wide margin. The premise holds: real
prefix caching gives a ~56x, ~720 ms TTFT benefit well above noise, and four real
`llama-server` backends run concurrently locally with stable per-instance TTFT.

Recorded for the build:
- prefix length used: 4200 tokens (200 width-stable sentences).
- measured TTFT separation: 721.6 ms mean, 56.2x pooled-std ratio.
- working N: 4 backends, 3 threads each.
