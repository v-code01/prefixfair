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
| cache-affinity | 20/20 | 88.7% +/- 0.3 | 0.881 +/- 0.003 | 210272 +/- 5654 | 2.958 +/- 0.080 | 50ms | 651ms | 0 |
| vtc-cache-blind | 20/20 | 92.5% +/- 0.3 | 0.918 +/- 0.003 | 468 +/- 475 | 0.007 +/- 0.007 | 44ms | 744ms | 1 |
| jsq-d | 20/20 | 88.4% +/- 0.3 | 0.878 +/- 0.003 | 209846 +/- 5933 | 2.952 +/- 0.083 | 60ms | 700ms | 1 |
| consistent-hash | 20/20 | 96.5% +/- 0.2 | 0.958 +/- 0.002 | 208282 +/- 5918 | 2.930 +/- 0.083 | 391ms | 806ms | 0 |

## Frontier shape

The fair corner is **vtc-cache-blind** at 92.5% cache-hit and normalized gap 0.007. The cache corner is **consistent-hash** at 96.5% cache-hit and normalized gap 2.930.

Moving from the fair corner to the cache corner trades +4.0 cache-hit points for a paired normalized-gap increase of 2.923 (95% CI 0.085) across the held-out seeds. The paired gap increase is resolvable beyond zero: on this workload there is a measurable price of fairness (a knee), quantified by the table above.

This is the measured finding, reported as it falls. No policy is claimed to dominate here.

## Per-seed raw results

| seed | policy | hit% | reuse | gap | norm.gap | completed | errors |
|---|---|---|---|---|---|---|---|
| 900000000 | cache-affinity | 88.8% | 0.881 | 228903 | 3.220 | 400 | 0 |
| 900000000 | vtc-cache-blind | 93.8% | 0.930 | 50 | 0.001 | 400 | 0 |
| 900000000 | jsq-d | 88.8% | 0.881 | 228903 | 3.220 | 400 | 0 |
| 900000000 | consistent-hash | 96.2% | 0.955 | 226059 | 3.180 | 400 | 0 |
| 900000001 | cache-affinity | 89.8% | 0.891 | 208995 | 2.940 | 400 | 0 |
| 900000001 | vtc-cache-blind | 93.2% | 0.926 | 2831 | 0.040 | 400 | 0 |
| 900000001 | jsq-d | 89.2% | 0.886 | 208995 | 2.940 | 400 | 0 |
| 900000001 | consistent-hash | 96.2% | 0.955 | 207573 | 2.920 | 400 | 0 |
| 900000002 | cache-affinity | 89.8% | 0.891 | 233165 | 3.280 | 400 | 0 |
| 900000002 | vtc-cache-blind | 94.5% | 0.938 | 54 | 0.001 | 400 | 0 |
| 900000002 | jsq-d | 88.2% | 0.876 | 233165 | 3.280 | 400 | 0 |
| 900000002 | consistent-hash | 96.5% | 0.958 | 234587 | 3.300 | 400 | 0 |
| 900000003 | cache-affinity | 89.2% | 0.886 | 208991 | 2.940 | 400 | 0 |
| 900000003 | vtc-cache-blind | 93.2% | 0.926 | 2829 | 0.040 | 400 | 0 |
| 900000003 | jsq-d | 88.2% | 0.876 | 208991 | 2.940 | 400 | 0 |
| 900000003 | consistent-hash | 97.0% | 0.963 | 206147 | 2.900 | 400 | 0 |
| 900000004 | cache-affinity | 89.0% | 0.884 | 200469 | 2.820 | 400 | 0 |
| 900000004 | vtc-cache-blind | 92.2% | 0.916 | 40 | 0.001 | 400 | 0 |
| 900000004 | jsq-d | 87.0% | 0.864 | 197625 | 2.780 | 400 | 0 |
| 900000004 | consistent-hash | 96.8% | 0.960 | 199047 | 2.800 | 400 | 0 |
| 900000005 | cache-affinity | 88.0% | 0.874 | 226046 | 3.180 | 400 | 0 |
| 900000005 | vtc-cache-blind | 92.2% | 0.916 | 67 | 0.001 | 400 | 0 |
| 900000005 | jsq-d | 88.0% | 0.874 | 226046 | 3.180 | 400 | 0 |
| 900000005 | consistent-hash | 96.8% | 0.960 | 226046 | 3.180 | 400 | 0 |
| 900000006 | cache-affinity | 89.2% | 0.886 | 210408 | 2.960 | 400 | 0 |
| 900000006 | vtc-cache-blind | 92.8% | 0.921 | 52 | 0.001 | 400 | 0 |
| 900000006 | jsq-d | 88.0% | 0.874 | 213252 | 3.000 | 400 | 0 |
| 900000006 | consistent-hash | 96.5% | 0.958 | 207564 | 2.920 | 400 | 0 |
| 900000007 | cache-affinity | 88.0% | 0.874 | 201891 | 2.840 | 400 | 0 |
| 900000007 | vtc-cache-blind | 92.2% | 0.916 | 39 | 0.001 | 400 | 0 |
| 900000007 | jsq-d | 87.8% | 0.871 | 201891 | 2.840 | 400 | 0 |
| 900000007 | consistent-hash | 96.0% | 0.953 | 201891 | 2.840 | 400 | 0 |
| 900000008 | cache-affinity | 89.2% | 0.886 | 220361 | 3.100 | 400 | 0 |
| 900000008 | vtc-cache-blind | 92.0% | 0.913 | 71 | 0.001 | 400 | 0 |
| 900000008 | jsq-d | 88.2% | 0.876 | 218939 | 3.080 | 400 | 0 |
| 900000008 | consistent-hash | 96.2% | 0.955 | 217517 | 3.060 | 400 | 0 |
| 900000009 | cache-affinity | 88.2% | 0.876 | 199046 | 2.800 | 400 | 0 |
| 900000009 | vtc-cache-blind | 92.2% | 0.916 | 2816 | 0.040 | 400 | 1 |
| 900000009 | jsq-d | 88.8% | 0.881 | 199046 | 2.800 | 400 | 0 |
| 900000009 | consistent-hash | 96.2% | 0.955 | 196202 | 2.760 | 400 | 0 |
| 900000010 | cache-affinity | 87.8% | 0.871 | 196192 | 2.760 | 400 | 0 |
| 900000010 | vtc-cache-blind | 92.2% | 0.916 | 49 | 0.001 | 400 | 0 |
| 900000010 | jsq-d | 88.8% | 0.881 | 194770 | 2.740 | 400 | 0 |
| 900000010 | consistent-hash | 96.0% | 0.953 | 193348 | 2.720 | 400 | 0 |
| 900000011 | cache-affinity | 87.8% | 0.871 | 186236 | 2.620 | 400 | 0 |
| 900000011 | vtc-cache-blind | 92.0% | 0.913 | 51 | 0.001 | 400 | 0 |
| 900000011 | jsq-d | 88.8% | 0.881 | 184814 | 2.600 | 400 | 0 |
| 900000011 | consistent-hash | 96.5% | 0.958 | 183392 | 2.580 | 400 | 0 |
| 900000012 | cache-affinity | 89.2% | 0.886 | 204735 | 2.880 | 400 | 0 |
| 900000012 | vtc-cache-blind | 92.0% | 0.913 | 43 | 0.001 | 400 | 0 |
| 900000012 | jsq-d | 89.5% | 0.889 | 200469 | 2.820 | 400 | 0 |
| 900000012 | consistent-hash | 97.0% | 0.963 | 200469 | 2.820 | 400 | 0 |
| 900000013 | cache-affinity | 87.8% | 0.871 | 196197 | 2.760 | 400 | 0 |
| 900000013 | vtc-cache-blind | 92.0% | 0.913 | 41 | 0.001 | 400 | 0 |
| 900000013 | jsq-d | 87.8% | 0.871 | 194775 | 2.740 | 400 | 0 |
| 900000013 | consistent-hash | 96.5% | 0.958 | 193353 | 2.720 | 400 | 0 |
| 900000014 | cache-affinity | 89.0% | 0.884 | 216100 | 3.040 | 400 | 0 |
| 900000014 | vtc-cache-blind | 92.5% | 0.918 | 55 | 0.001 | 400 | 0 |
| 900000014 | jsq-d | 87.5% | 0.869 | 217522 | 3.060 | 400 | 1 |
| 900000014 | consistent-hash | 97.0% | 0.963 | 214678 | 3.020 | 400 | 0 |
| 900000015 | cache-affinity | 88.2% | 0.876 | 204734 | 2.880 | 400 | 0 |
| 900000015 | vtc-cache-blind | 92.2% | 0.916 | 46 | 0.001 | 400 | 0 |
| 900000015 | jsq-d | 89.0% | 0.884 | 204734 | 2.880 | 400 | 0 |
| 900000015 | consistent-hash | 96.5% | 0.958 | 203312 | 2.860 | 400 | 0 |
| 900000016 | cache-affinity | 89.5% | 0.889 | 220366 | 3.100 | 400 | 0 |
| 900000016 | vtc-cache-blind | 92.0% | 0.913 | 58 | 0.001 | 400 | 0 |
| 900000016 | jsq-d | 89.5% | 0.889 | 220366 | 3.100 | 400 | 0 |
| 900000016 | consistent-hash | 97.0% | 0.963 | 217522 | 3.060 | 400 | 0 |
| 900000017 | cache-affinity | 88.2% | 0.876 | 218944 | 3.080 | 400 | 0 |
| 900000017 | vtc-cache-blind | 92.5% | 0.918 | 57 | 0.001 | 400 | 0 |
| 900000017 | jsq-d | 87.8% | 0.871 | 218944 | 3.080 | 400 | 0 |
| 900000017 | consistent-hash | 96.8% | 0.960 | 216100 | 3.040 | 400 | 0 |
| 900000018 | cache-affinity | 88.2% | 0.876 | 213256 | 3.000 | 400 | 0 |
| 900000018 | vtc-cache-blind | 92.0% | 0.913 | 57 | 0.001 | 400 | 0 |
| 900000018 | jsq-d | 88.8% | 0.881 | 213256 | 3.000 | 400 | 0 |
| 900000018 | consistent-hash | 96.8% | 0.960 | 211834 | 2.980 | 400 | 0 |
| 900000019 | cache-affinity | 90.0% | 0.893 | 210415 | 2.960 | 400 | 0 |
| 900000019 | vtc-cache-blind | 92.0% | 0.913 | 63 | 0.001 | 400 | 0 |
| 900000019 | jsq-d | 89.2% | 0.886 | 210415 | 2.960 | 400 | 0 |
| 900000019 | consistent-hash | 96.2% | 0.955 | 208993 | 2.940 | 400 | 0 |

