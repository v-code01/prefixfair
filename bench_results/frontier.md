# prefixfair frontier: cache-hit% vs cross-tenant service-gap

Real N=4 llama-server fleet. Every latency and cache-reuse number is measured from a real server slot; nothing in the measured path is simulated. The service gap is the max-min cross-tenant weighted-token service read at the K-th completion, a snapshot taken while every tenant is still backlogged, which is exactly VTC's assumption.

## Configuration

- Tenants: 8, Zipf s=1.10, requests/seed: 2000, prefix sentences: 64 (~1.3k tokens)
- Snapshot at K=400 completions; max in-flight: 8; n_predict: 32
- Fleet: N=4, slots=2, ctx=16384 (8192/slot), threads=3 each
- VTC cost weights: wIn=1.0, wOut=2.0
- Held-out seeds (20): [900000000 900000001 900000002 900000003 900000004 900000005 900000006 900000007 900000008 900000009 900000010 900000011 900000012 900000013 900000014 900000015 900000016 900000017 900000018 900000019]

## Frontier (mean +/- 95% CI over 20 held-out seeds)

| policy | cache-hit% | mean reuse | service gap | norm. gap | TTFT p50 | TTFT p95 | errors |
|---|---|---|---|---|---|---|---|
| cache-affinity | 97.1% +/- 0.5 | 0.963 +/- 0.005 | 203447 +/- 13876 | 2.966 +/- 0.088 | 839ms | 2.236s | 1854 |
| vtc-cache-blind | 91.0% +/- 0.6 | 0.903 +/- 0.006 | 1427 +/- 660 | 0.020 +/- 0.009 | 597ms | 1.533s | 1 |
| jsq-d | 44.5% +/- 21.4 | 0.442 +/- 0.213 | 94122 +/- 47630 | 1.502 +/- 0.723 | 155ms | 1.181s | 23644 |
| consistent-hash | 79.9% +/- 19.2 | 0.793 +/- 0.190 | 161507 +/- 40509 | 2.343 +/- 0.568 | 582ms | 1.735s | 9792 |

## Frontier shape

The fair corner is **vtc-cache-blind** at 91.0% cache-hit and normalized gap 0.020. The cache corner is **cache-affinity** at 97.1% cache-hit and normalized gap 2.966.

Moving from the fair corner to the cache corner trades +6.0 cache-hit points for a paired normalized-gap increase of 2.946 (95% CI 0.086) across the held-out seeds. The paired gap increase is resolvable beyond zero: on this workload there is a measurable price of fairness (a knee), quantified by the table above.

This is the measured finding, reported as it falls. No policy is claimed to dominate here.

## Per-seed raw results

| seed | policy | hit% | reuse | gap | norm.gap | completed | errors |
|---|---|---|---|---|---|---|---|
| 900000000 | cache-affinity | 95.0% | 0.943 | 228903 | 3.220 | 400 | 0 |
| 900000000 | vtc-cache-blind | 92.8% | 0.920 | 2795 | 0.039 | 400 | 0 |
| 900000000 | jsq-d | 100.0% | 0.992 | 105189 | 3.403 | 174 | 1826 |
| 900000000 | consistent-hash | 100.0% | 0.992 | 228903 | 3.220 | 400 | 0 |
| 900000001 | cache-affinity | 95.5% | 0.948 | 208995 | 2.940 | 400 | 0 |
| 900000001 | vtc-cache-blind | 92.0% | 0.913 | 2791 | 0.039 | 400 | 0 |
| 900000001 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000001 | consistent-hash | 100.0% | 0.992 | 208995 | 2.940 | 400 | 0 |
| 900000002 | cache-affinity | 96.8% | 0.960 | 233165 | 3.280 | 400 | 0 |
| 900000002 | vtc-cache-blind | 91.2% | 0.906 | 2803 | 0.039 | 400 | 0 |
| 900000002 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000002 | consistent-hash | 99.5% | 0.987 | 231743 | 3.260 | 400 | 0 |
| 900000003 | cache-affinity | 95.8% | 0.950 | 208991 | 2.940 | 400 | 0 |
| 900000003 | vtc-cache-blind | 90.2% | 0.896 | 2834 | 0.040 | 400 | 0 |
| 900000003 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000003 | consistent-hash | 99.8% | 0.990 | 208991 | 2.940 | 400 | 0 |
| 900000004 | cache-affinity | 96.0% | 0.953 | 199047 | 2.800 | 400 | 0 |
| 900000004 | vtc-cache-blind | 90.0% | 0.893 | 40 | 0.001 | 400 | 0 |
| 900000004 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000004 | consistent-hash | 100.0% | 0.992 | 199047 | 2.800 | 400 | 0 |
| 900000005 | cache-affinity | 97.0% | 0.962 | 226046 | 3.180 | 400 | 1 |
| 900000005 | vtc-cache-blind | 90.2% | 0.896 | 67 | 0.001 | 400 | 0 |
| 900000005 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000005 | consistent-hash | 100.0% | 0.992 | 226046 | 3.180 | 400 | 0 |
| 900000006 | cache-affinity | 97.0% | 0.962 | 211830 | 2.980 | 400 | 0 |
| 900000006 | vtc-cache-blind | 89.8% | 0.891 | 52 | 0.001 | 400 | 0 |
| 900000006 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000006 | consistent-hash | 100.0% | 0.992 | 210408 | 2.960 | 400 | 0 |
| 900000007 | cache-affinity | 97.5% | 0.967 | 203313 | 2.860 | 400 | 1 |
| 900000007 | vtc-cache-blind | 89.5% | 0.888 | 2807 | 0.039 | 400 | 0 |
| 900000007 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000007 | consistent-hash | 100.0% | 0.992 | 203313 | 2.860 | 400 | 0 |
| 900000008 | cache-affinity | 97.5% | 0.967 | 217517 | 3.060 | 400 | 1 |
| 900000008 | vtc-cache-blind | 90.5% | 0.898 | 71 | 0.001 | 400 | 0 |
| 900000008 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000008 | consistent-hash | 99.8% | 0.990 | 218939 | 3.080 | 400 | 0 |
| 900000009 | cache-affinity | 96.2% | 0.955 | 200468 | 2.820 | 400 | 0 |
| 900000009 | vtc-cache-blind | 90.8% | 0.901 | 37 | 0.001 | 400 | 0 |
| 900000009 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000009 | consistent-hash | 100.0% | 0.992 | 196202 | 2.760 | 400 | 0 |
| 900000010 | cache-affinity | 97.8% | 0.970 | 193348 | 2.720 | 400 | 0 |
| 900000010 | vtc-cache-blind | 89.8% | 0.891 | 2806 | 0.039 | 400 | 1 |
| 900000010 | jsq-d | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000010 | consistent-hash | 99.8% | 0.990 | 193348 | 2.720 | 400 | 0 |
| 900000011 | cache-affinity | 97.2% | 0.965 | 186236 | 2.620 | 400 | 0 |
| 900000011 | vtc-cache-blind | 90.5% | 0.898 | 51 | 0.001 | 400 | 0 |
| 900000011 | jsq-d | 78.0% | 0.775 | 96763 | 2.989 | 182 | 1818 |
| 900000011 | consistent-hash | 100.0% | 0.992 | 183392 | 2.580 | 400 | 0 |
| 900000012 | cache-affinity | 97.2% | 0.965 | 203313 | 2.860 | 400 | 0 |
| 900000012 | vtc-cache-blind | 91.2% | 0.906 | 2821 | 0.040 | 400 | 0 |
| 900000012 | jsq-d | 89.2% | 0.886 | 201891 | 2.840 | 400 | 0 |
| 900000012 | consistent-hash | 100.0% | 0.992 | 200469 | 2.820 | 400 | 0 |
| 900000013 | cache-affinity | 97.0% | 0.962 | 196197 | 2.760 | 400 | 0 |
| 900000013 | vtc-cache-blind | 90.5% | 0.898 | 41 | 0.001 | 400 | 0 |
| 900000013 | jsq-d | 88.0% | 0.874 | 196197 | 2.760 | 400 | 0 |
| 900000013 | consistent-hash | 100.0% | 0.992 | 194775 | 2.740 | 400 | 0 |
| 900000014 | cache-affinity | 97.2% | 0.965 | 213256 | 3.000 | 400 | 0 |
| 900000014 | vtc-cache-blind | 90.2% | 0.896 | 2790 | 0.039 | 400 | 0 |
| 900000014 | jsq-d | 89.8% | 0.891 | 216100 | 3.040 | 400 | 0 |
| 900000014 | consistent-hash | 100.0% | 0.992 | 216100 | 3.040 | 400 | 0 |
| 900000015 | cache-affinity | 98.2% | 0.975 | 203312 | 2.860 | 400 | 0 |
| 900000015 | vtc-cache-blind | 90.8% | 0.901 | 46 | 0.001 | 400 | 0 |
| 900000015 | jsq-d | 89.0% | 0.884 | 203312 | 2.860 | 400 | 0 |
| 900000015 | consistent-hash | 100.0% | 0.992 | 109460 | 2.962 | 208 | 1792 |
| 900000016 | cache-affinity | 97.2% | 0.965 | 216100 | 3.040 | 400 | 0 |
| 900000016 | vtc-cache-blind | 90.8% | 0.901 | 58 | 0.001 | 400 | 0 |
| 900000016 | jsq-d | 88.8% | 0.881 | 218948 | 3.080 | 400 | 0 |
| 900000016 | consistent-hash | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000017 | cache-affinity | 98.8% | 0.980 | 217522 | 3.060 | 400 | 0 |
| 900000017 | vtc-cache-blind | 93.8% | 0.930 | 2798 | 0.039 | 400 | 0 |
| 900000017 | jsq-d | 89.2% | 0.886 | 220366 | 3.100 | 400 | 0 |
| 900000017 | consistent-hash | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000018 | cache-affinity | 98.8% | 0.980 | 213256 | 3.000 | 400 | 0 |
| 900000018 | vtc-cache-blind | 93.2% | 0.925 | 57 | 0.001 | 400 | 0 |
| 900000018 | jsq-d | 88.5% | 0.879 | 213256 | 3.000 | 400 | 0 |
| 900000018 | consistent-hash | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |
| 900000019 | cache-affinity | 97.3% | 0.966 | 88120 | 3.329 | 149 | 1851 |
| 900000019 | vtc-cache-blind | 92.5% | 0.918 | 2782 | 0.039 | 400 | 0 |
| 900000019 | jsq-d | 89.8% | 0.891 | 210415 | 2.960 | 400 | 0 |
| 900000019 | consistent-hash | 0.0% | 0.000 | 0 | 0.000 | 0 | 2000 |

