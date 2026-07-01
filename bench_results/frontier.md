# prefixfair frontier: cache-hit% vs cross-tenant service-gap

Real N=4 llama-server fleet. Every latency and cache-reuse number is measured from a real server slot; nothing in the measured path is simulated. The service gap is the max-min cross-tenant weighted-token service read at the K-th completion, a snapshot taken while every tenant is still backlogged, which is exactly VTC's assumption.

## Configuration

- Tenants: 8, Zipf s=1.10, requests/seed: 2000, prefix sentences: 64 (~1.3k tokens)
- Snapshot at K=400 completions; max in-flight: 8; n_predict: 32
- Fleet: N=4, slots=2, ctx=16384 (8192/slot), threads=3 each
- VTC cost weights: wIn=1.0, wOut=2.0
- Held-out seeds (20): [900000000 900000001 900000002 900000003 900000004 900000005 900000006 900000007 900000008 900000009 900000010 900000011 900000012 900000013 900000014 900000015 900000016 900000017 900000018 900000019]

## Frontier (mean +/- 95% CI over the valid held-out seeds)

Statistics are over the seeds that reached the snapshot; the seeds column shows valid/total. A row marked unreliable (fewer than 75% of seeds valid) is a survivorship-biased sample and is excluded from the frontier shape below.

| policy | seeds | cache-hit% | mean reuse | service gap | norm. gap | TTFT p50 | TTFT p95 | errors |
|---|---|---|---|---|---|---|---|---|
| cache-affinity | 20/20 | 88.8% +/- 0.3 | 0.882 +/- 0.003 | 210201 +/- 5806 | 2.957 +/- 0.082 | 51ms | 627ms | 0 |
| vtc-cache-blind | 20/20 | 92.5% +/- 0.2 | 0.918 +/- 0.002 | 606 +/- 531 | 0.009 +/- 0.007 | 43ms | 723ms | 0 |
| jsq-d | 20/20 | 88.5% +/- 0.3 | 0.879 +/- 0.003 | 210059 +/- 5848 | 2.955 +/- 0.082 | 56ms | 689ms | 0 |
| consistent-hash | 20/20 | 96.5% +/- 0.2 | 0.958 +/- 0.002 | 208282 +/- 5918 | 2.930 +/- 0.083 | 383ms | 795ms | 0 |
| fair-cache-affinity | 20/20 | 92.1% +/- 0.3 | 0.914 +/- 0.003 | 328 +/- 397 | 0.005 +/- 0.006 | 49ms | 538ms | 0 |

## Frontier shape

The fair corner is **fair-cache-affinity** at 92.1% cache-hit and normalized gap 0.005. The cache corner is **consistent-hash** at 96.5% cache-hit and normalized gap 2.930.

Moving from the fair corner to the cache corner trades +4.4 cache-hit points for a paired normalized-gap increase of 2.925 (95% CI 0.082) across the held-out seeds. The paired gap increase is resolvable beyond zero: on this workload there is a measurable price of fairness (a knee), quantified by the table above.

This is the measured finding, reported as it falls. No policy is claimed to dominate here.

## Regime and caveats

This frontier is measured with 8 tenants against N=4 x 2 slots = 8 aggregate llama-server slots, so the working set (8 distinct tenant prefixes) fits inside the aggregate KV capacity (8 slots): the cache holds the entire working set, which compresses the cache-hit axis (even a random load balancer keeps the popular prefixes resident everywhere).

**Magnitude is regime-specific.** With tenants == total slots, a cache-blind least-loaded placement under round-robin fair admission develops emergent slot-affinity: each tenant cycles back to a slot still holding its warm prefix, so the cache-blind fair policy scores a cache-hit well above what prefix-blindness would otherwise give. This flatters cache-blind admission and makes the reported fair-corner-to-cache-corner cache-hit separation a **lower bound** on the cost of fairness. A tenants != slots regime (working set larger than capacity) breaks the phase-lock and is expected to widen that separation. Read the separation's magnitude as regime-bound, not a policy constant; see the sensitivity control run.

cache-affinity's cache-hit is additionally depressed by the small N=4 fleet: with average load near 2 per backend the bounded-load cap and hotspot replication fire aggressively, spreading a hot prefix off its single warm slot. That is the genuine DualMap load-bounding trade (a faithful baseline, not a strawman), and its cache-hit would recover on a larger fleet.

Cache-hit is measured over each policy's own K completions. Round-robin fair admission serves colder tail-tenant requests than FIFO admission does, so the cache-hit samples are not identical across admission disciplines; this handicaps the fair policy (it serves colder requests) rather than flattering it.

## Per-seed raw results

| seed | policy | hit% | reuse | gap | norm.gap | completed | errors |
|---|---|---|---|---|---|---|---|
| 900000000 | cache-affinity | 88.2% | 0.876 | 228903 | 3.220 | 400 | 0 |
| 900000000 | vtc-cache-blind | 92.8% | 0.921 | 50 | 0.001 | 400 | 0 |
| 900000000 | jsq-d | 89.0% | 0.883 | 228903 | 3.220 | 400 | 0 |
| 900000000 | consistent-hash | 96.2% | 0.955 | 226059 | 3.180 | 400 | 0 |
| 900000000 | fair-cache-affinity | 91.8% | 0.911 | 50 | 0.001 | 400 | 0 |
| 900000001 | cache-affinity | 89.5% | 0.889 | 208995 | 2.940 | 400 | 0 |
| 900000001 | vtc-cache-blind | 92.0% | 0.913 | 54 | 0.001 | 400 | 0 |
| 900000001 | jsq-d | 88.5% | 0.879 | 208995 | 2.940 | 400 | 0 |
| 900000001 | consistent-hash | 96.2% | 0.955 | 207573 | 2.920 | 400 | 0 |
| 900000001 | fair-cache-affinity | 92.0% | 0.913 | 54 | 0.001 | 400 | 0 |
| 900000002 | cache-affinity | 89.2% | 0.886 | 233165 | 3.280 | 400 | 0 |
| 900000002 | vtc-cache-blind | 93.0% | 0.923 | 54 | 0.001 | 400 | 0 |
| 900000002 | jsq-d | 88.8% | 0.881 | 233165 | 3.280 | 400 | 0 |
| 900000002 | consistent-hash | 96.5% | 0.958 | 234587 | 3.300 | 400 | 0 |
| 900000002 | fair-cache-affinity | 92.2% | 0.916 | 2799 | 0.039 | 400 | 0 |
| 900000003 | cache-affinity | 89.5% | 0.889 | 208991 | 2.940 | 400 | 0 |
| 900000003 | vtc-cache-blind | 93.5% | 0.928 | 60 | 0.001 | 400 | 0 |
| 900000003 | jsq-d | 88.0% | 0.874 | 210413 | 2.960 | 400 | 0 |
| 900000003 | consistent-hash | 97.0% | 0.963 | 206147 | 2.900 | 400 | 0 |
| 900000003 | fair-cache-affinity | 93.5% | 0.928 | 60 | 0.001 | 400 | 0 |
| 900000004 | cache-affinity | 88.8% | 0.881 | 199047 | 2.800 | 400 | 0 |
| 900000004 | vtc-cache-blind | 92.2% | 0.916 | 40 | 0.001 | 400 | 0 |
| 900000004 | jsq-d | 88.0% | 0.874 | 199047 | 2.800 | 400 | 0 |
| 900000004 | consistent-hash | 96.8% | 0.960 | 199047 | 2.800 | 400 | 0 |
| 900000004 | fair-cache-affinity | 91.8% | 0.911 | 2813 | 0.040 | 400 | 0 |
| 900000005 | cache-affinity | 88.8% | 0.881 | 226046 | 3.180 | 400 | 0 |
| 900000005 | vtc-cache-blind | 92.5% | 0.918 | 67 | 0.001 | 400 | 0 |
| 900000005 | jsq-d | 89.0% | 0.884 | 226046 | 3.180 | 400 | 0 |
| 900000005 | consistent-hash | 96.8% | 0.960 | 226046 | 3.180 | 400 | 0 |
| 900000005 | fair-cache-affinity | 93.2% | 0.926 | 67 | 0.001 | 400 | 0 |
| 900000006 | cache-affinity | 89.8% | 0.891 | 213252 | 3.000 | 400 | 0 |
| 900000006 | vtc-cache-blind | 93.2% | 0.926 | 52 | 0.001 | 400 | 0 |
| 900000006 | jsq-d | 88.8% | 0.881 | 213252 | 3.000 | 400 | 0 |
| 900000006 | consistent-hash | 96.5% | 0.958 | 207564 | 2.920 | 400 | 0 |
| 900000006 | fair-cache-affinity | 91.8% | 0.911 | 52 | 0.001 | 400 | 0 |
| 900000007 | cache-affinity | 88.0% | 0.874 | 201891 | 2.840 | 400 | 0 |
| 900000007 | vtc-cache-blind | 92.5% | 0.918 | 39 | 0.001 | 400 | 0 |
| 900000007 | jsq-d | 88.2% | 0.876 | 201891 | 2.840 | 400 | 0 |
| 900000007 | consistent-hash | 96.0% | 0.953 | 201891 | 2.840 | 400 | 0 |
| 900000007 | fair-cache-affinity | 91.8% | 0.911 | 39 | 0.001 | 400 | 0 |
| 900000008 | cache-affinity | 89.5% | 0.889 | 221783 | 3.120 | 400 | 0 |
| 900000008 | vtc-cache-blind | 92.5% | 0.918 | 71 | 0.001 | 400 | 0 |
| 900000008 | jsq-d | 87.8% | 0.871 | 220361 | 3.100 | 400 | 0 |
| 900000008 | consistent-hash | 96.2% | 0.955 | 217517 | 3.060 | 400 | 0 |
| 900000008 | fair-cache-affinity | 91.2% | 0.906 | 71 | 0.001 | 400 | 0 |
| 900000009 | cache-affinity | 89.0% | 0.884 | 199046 | 2.800 | 400 | 0 |
| 900000009 | vtc-cache-blind | 92.0% | 0.913 | 37 | 0.001 | 400 | 0 |
| 900000009 | jsq-d | 88.5% | 0.879 | 200468 | 2.820 | 400 | 0 |
| 900000009 | consistent-hash | 96.2% | 0.955 | 196202 | 2.760 | 400 | 0 |
| 900000009 | fair-cache-affinity | 92.0% | 0.913 | 37 | 0.001 | 400 | 0 |
| 900000010 | cache-affinity | 89.2% | 0.886 | 194770 | 2.740 | 400 | 0 |
| 900000010 | vtc-cache-blind | 92.0% | 0.913 | 49 | 0.001 | 400 | 0 |
| 900000010 | jsq-d | 88.2% | 0.876 | 194770 | 2.740 | 400 | 0 |
| 900000010 | consistent-hash | 96.0% | 0.953 | 193348 | 2.720 | 400 | 0 |
| 900000010 | fair-cache-affinity | 92.2% | 0.916 | 49 | 0.001 | 400 | 0 |
| 900000011 | cache-affinity | 87.5% | 0.869 | 186236 | 2.620 | 400 | 0 |
| 900000011 | vtc-cache-blind | 92.0% | 0.913 | 51 | 0.001 | 400 | 0 |
| 900000011 | jsq-d | 87.5% | 0.869 | 184814 | 2.600 | 400 | 0 |
| 900000011 | consistent-hash | 96.5% | 0.958 | 183392 | 2.580 | 400 | 0 |
| 900000011 | fair-cache-affinity | 92.8% | 0.921 | 51 | 0.001 | 400 | 0 |
| 900000012 | cache-affinity | 88.8% | 0.881 | 201891 | 2.840 | 400 | 0 |
| 900000012 | vtc-cache-blind | 92.0% | 0.913 | 43 | 0.001 | 400 | 0 |
| 900000012 | jsq-d | 89.5% | 0.889 | 201891 | 2.840 | 400 | 0 |
| 900000012 | consistent-hash | 97.0% | 0.963 | 200469 | 2.820 | 400 | 0 |
| 900000012 | fair-cache-affinity | 92.0% | 0.913 | 43 | 0.001 | 400 | 0 |
| 900000013 | cache-affinity | 87.5% | 0.869 | 196197 | 2.760 | 400 | 0 |
| 900000013 | vtc-cache-blind | 92.0% | 0.913 | 2832 | 0.040 | 400 | 0 |
| 900000013 | jsq-d | 88.5% | 0.879 | 194775 | 2.740 | 400 | 0 |
| 900000013 | consistent-hash | 96.5% | 0.958 | 193353 | 2.720 | 400 | 0 |
| 900000013 | fair-cache-affinity | 91.8% | 0.911 | 41 | 0.001 | 400 | 0 |
| 900000014 | cache-affinity | 88.0% | 0.874 | 216100 | 3.040 | 400 | 0 |
| 900000014 | vtc-cache-blind | 92.5% | 0.918 | 55 | 0.001 | 400 | 0 |
| 900000014 | jsq-d | 88.5% | 0.879 | 216100 | 3.040 | 400 | 0 |
| 900000014 | consistent-hash | 97.0% | 0.963 | 214678 | 3.020 | 400 | 0 |
| 900000014 | fair-cache-affinity | 91.5% | 0.908 | 55 | 0.001 | 400 | 0 |
| 900000015 | cache-affinity | 88.2% | 0.876 | 204734 | 2.880 | 400 | 0 |
| 900000015 | vtc-cache-blind | 92.2% | 0.916 | 2821 | 0.040 | 400 | 0 |
| 900000015 | jsq-d | 88.2% | 0.876 | 204734 | 2.880 | 400 | 0 |
| 900000015 | consistent-hash | 96.5% | 0.958 | 203312 | 2.860 | 400 | 0 |
| 900000015 | fair-cache-affinity | 92.2% | 0.916 | 46 | 0.001 | 400 | 0 |
| 900000016 | cache-affinity | 89.5% | 0.889 | 220366 | 3.100 | 400 | 0 |
| 900000016 | vtc-cache-blind | 92.0% | 0.913 | 58 | 0.001 | 400 | 0 |
| 900000016 | jsq-d | 89.2% | 0.886 | 220366 | 3.100 | 400 | 0 |
| 900000016 | consistent-hash | 97.0% | 0.963 | 217522 | 3.060 | 400 | 0 |
| 900000016 | fair-cache-affinity | 91.8% | 0.911 | 58 | 0.001 | 400 | 0 |
| 900000017 | cache-affinity | 88.5% | 0.879 | 218944 | 3.080 | 400 | 0 |
| 900000017 | vtc-cache-blind | 92.5% | 0.918 | 57 | 0.001 | 400 | 0 |
| 900000017 | jsq-d | 88.0% | 0.874 | 218944 | 3.080 | 400 | 0 |
| 900000017 | consistent-hash | 96.8% | 0.960 | 216100 | 3.040 | 400 | 0 |
| 900000017 | fair-cache-affinity | 91.2% | 0.906 | 57 | 0.001 | 400 | 0 |
| 900000018 | cache-affinity | 89.8% | 0.891 | 213256 | 3.000 | 400 | 0 |
| 900000018 | vtc-cache-blind | 93.5% | 0.928 | 2788 | 0.039 | 400 | 0 |
| 900000018 | jsq-d | 89.2% | 0.886 | 211834 | 2.980 | 400 | 0 |
| 900000018 | consistent-hash | 96.8% | 0.960 | 211834 | 2.980 | 400 | 0 |
| 900000018 | fair-cache-affinity | 93.2% | 0.926 | 57 | 0.001 | 400 | 0 |
| 900000019 | cache-affinity | 89.2% | 0.886 | 210415 | 2.960 | 400 | 0 |
| 900000019 | vtc-cache-blind | 92.0% | 0.913 | 2833 | 0.040 | 400 | 0 |
| 900000019 | jsq-d | 89.5% | 0.888 | 210415 | 2.960 | 400 | 0 |
| 900000019 | consistent-hash | 96.2% | 0.955 | 208993 | 2.940 | 400 | 0 |
| 900000019 | fair-cache-affinity | 92.0% | 0.913 | 63 | 0.001 | 400 | 0 |

