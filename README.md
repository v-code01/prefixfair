# prefixfair

A fairness-constrained prefix-cache LLM router, and the measured trade-off it exists to map: **how much KV-cache hit rate you give up to close the cross-tenant service gap.** It replays a multi-tenant Zipfian trace against N real `llama-server` backends under five routing policies and reports where each lands on the cache-hit / service-gap plane, with confidence intervals on held-out seeds.

Every latency and every cache-reuse number is measured from a live server slot. Nothing in the measured path is simulated.

## What this is, honestly

This is a real distributed-systems measurement, not a new scheduler theory. The fairness mechanism (VTC, arXiv 2401.00588) and the cache-affinity mechanism (DualMap-style consistent-hash routing with bounded loads and hotspot replication, arXiv 2602.06502) are prior work. The contribution is fusing them against real backends and measuring a frontier the literature never did: VTC never modeled cache, and cache routers are fairness-blind. The finding stands whether or not the coupled policy wins.

## The finding

The price of fairness is a real, statistically resolved knee, and its size depends on whether the cache can hold the working set.

Five policies, real N=4 llama-server fleet, 20 held-out seeds, mean +/- 95% CI. Normalized service gap is the cross-tenant max-min weighted-token service at a mid-backlog snapshot; lower is fairer.

Regime A, tenants=8 (aggregate KV capacity == working set):

| policy | cache-hit% | norm. gap | TTFT p95 |
|---|---|---|---|
| cache-affinity | 88.8 | 2.957 | 627ms |
| vtc-cache-blind | 92.5 | 0.009 | 723ms |
| jsq-d | 88.5 | 2.955 | 689ms |
| consistent-hash | 96.5 | 2.930 | 795ms |
| fair-cache-affinity | 92.1 | 0.005 | 538ms |

Regime B, tenants=12 (working set > capacity):

| policy | cache-hit% | norm. gap | TTFT p95 |
|---|---|---|---|
| cache-affinity | 87.1 | 4.132 | 731ms |
| vtc-cache-blind | 88.1 | 0.033 | 969ms |
| jsq-d | 84.8 | 4.129 | 784ms |
| consistent-hash | 95.3 | 4.096 | 898ms |
| fair-cache-affinity | 88.4 | 0.030 | 656ms |

The knee is present and resolved in both regimes, so the direction is robust: closing the service gap costs cache-hit. The magnitude is regime-bound. When aggregate KV capacity equals the working set and the active-tenant count equals the total slot count, a cache-blind fair scheduler develops emergent slot-affinity that flatters its cache-hit, so the measured cache cost of fairness (about four points, fair corner to cache corner) is a lower bound. Break that coincidence (more tenants than slots, working set larger than cache) and the cost widens (about seven points at twelve tenants on eight slots). The naive sticky policy anchors the cache corner independent of tenant count; the fair scheduler's cache-hit is the number that moves.

## The coupled policy: what it earns, and what it does not

fair-cache-affinity couples VTC least-served admission with DualMap cache-affinity placement. What the held-out CIs support:

- **It Pareto-dominates cache-affinity and JSQ(d) on both axes, in both regimes.** Strictly higher cache-hit and strictly lower service gap, non-overlapping CIs. On a fleet already doing DualMap placement, switching FIFO admission to VTC admission improves fairness by orders of magnitude and nudges cache-hit up.
- **It does not dominate the frontier.** consistent-hash holds the cache corner it cannot reach, because DualMap's load-bounding spreads a hot prefix off its single warm slot: the same mechanism that makes it a good balancer caps its reuse. No policy is claimed to win the frontier.
- **It ties vtc-cache-blind at the fair corner.** The affine placement buys little cache-hit over cache-blind placement, because when the working set exceeds capacity no spreading placement keeps prefixes warm, and when it fits the cache-blind policy already gets the emergent-affinity reuse. The coupling's honest value is the fairness it adds to cache-affinity, not a cache-hit gain over VTC.

As a point estimate without a confidence interval (tail latency is noisy and the report attaches no CI to it), fair-cache-affinity also posts the lowest TTFT p95 of the five policies in both regimes, 538ms and 656ms. Read that as an observation, not a CI-backed result.

## The five policies

- **cache-affinity**: faithful DualMap: consistent-hash home, bounded-load overflow, sliding-window hotspot replication. Trades some reuse to bound load; on a four-backend fleet that trade is aggressive, which depresses its cache-hit (it would recover on a larger fleet).
- **vtc-cache-blind**: the real Virtual Token Counter: least-served-tenant admission, cache-blind least-loaded placement. The fair corner.
- **jsq-d**: join-shortest-of-d-queues, power-of-two-choices load balancing. Fairness- and cache-blind.
- **consistent-hash**: sticky prefix-to-backend routing, no load balancing. Maximal reuse, no fairness. The cache corner.
- **fair-cache-affinity**: the coupled policy: VTC least-served admission on top of DualMap cache-affinity placement. Fairness lives in dispatch order, reuse lives in placement, so coupling them targets both corners at once. A dominance claim is earned only with held-out CIs.

## The sacred anchor

The cross-tenant service gap is measured under VTC's own backlogged-client assumption, at a snapshot taken while every tenant still has pending work. That is the only regime VTC's bounded gap is defined in. The VTC accountant reproduces the single-node 2x service-gap bound as a unit test; the distributed max-min gap is scoped to that same assumption. No fairness claim is made outside it.

## Honesty conditions held

- **Real backend, not simulation.** Every TTFT and reuse fraction comes from a real `llama-server` slot with real per-slot prefix reuse.
- **Non-strawman baselines.** cache-affinity carries all three DualMap dimensions; VTC runs its real least-served mechanism, validated against its published bound.
- **Un-gameable metric.** The fairness metric is charged the full nominal prompt plus generated tokens, so a policy cannot shrink a tenant's charged service by winning it cache hits.
- **Held-out seeds.** The frontier is reported on a seed range disjoint from any tuning, with paired Student-t confidence intervals. Seeds that fail to reach the snapshot are excluded from the aggregate and reported separately, never averaged in as zero.
- **No unearned headline.** No policy is declared the winner unless it dominates the frontier with confidence intervals on held-out seeds.

## Reproduce

```
go test ./...                      # unit + real-backend end-to-end (needs llama-server + the GGUF)
go run ./cmd/frontier -seeds 20 -tenants 8  -out bench_results/frontier.md
go run ./cmd/frontier -seeds 20 -tenants 12 -out bench_results/frontier_t12.md
```

Stack: Go, N x `llama-server` on one small quantized GGUF (mmap-shared), CPU-only. See `bench_results/` for the committed frontiers and `claims.toml` for every headline number mapped to the code and data that produce it.

## References

- Sheng et al., *Fairness in Serving Large Language Models* (VTC), OSDI 2024, arXiv 2401.00588.
- DualMap-style cache-affinity routing, arXiv 2602.06502.

MIT.
