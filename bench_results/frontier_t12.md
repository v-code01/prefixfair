# prefixfair frontier: cache-hit% vs cross-tenant service-gap

Real N=4 llama-server fleet. Every latency and cache-reuse number is measured from a real server slot; nothing in the measured path is simulated. The service gap is the max-min cross-tenant weighted-token service read at the K-th completion, a snapshot taken while every tenant is still backlogged, which is exactly VTC's assumption.

## Configuration

- Tenants: 12, Zipf s=1.10, requests/seed: 2500, prefix sentences: 64 (~1.3k tokens)
- Snapshot at K=400 completions; max in-flight: 8; n_predict: 32
- Fleet: N=4, slots=2, ctx=16384 (8192/slot), threads=3 each
- VTC cost weights: wIn=1.0, wOut=2.0
- Held-out seeds (20): [900000000 900000001 900000002 900000003 900000004 900000005 900000006 900000007 900000008 900000009 900000010 900000011 900000012 900000013 900000014 900000015 900000016 900000017 900000018 900000019]

## Frontier (mean +/- 95% CI over the valid held-out seeds)

Statistics are over the seeds that reached the snapshot; the seeds column shows valid/total. A row marked unreliable (fewer than 75% of seeds valid) is a survivorship-biased sample and is excluded from the frontier shape below.

| policy | seeds | cache-hit% | mean reuse | service gap | norm. gap | TTFT p50 | TTFT p95 | errors |
|---|---|---|---|---|---|---|---|---|
| cache-affinity | 20/20 | 86.9% +/- 0.3 | 0.863 +/- 0.003 | 196055 +/- 5486 | 4.137 +/- 0.116 | 54ms | 685ms | 0 |
| vtc-cache-blind | 20/20 | 88.1% +/- 0.1 | 0.875 +/- 0.001 | 1494 +/- 147 | 0.032 +/- 0.003 | 47ms | 931ms | 1 |
| jsq-d | 20/20 | 85.0% +/- 0.3 | 0.844 +/- 0.003 | 195629 +/- 5480 | 4.128 +/- 0.116 | 67ms | 731ms | 0 |
| consistent-hash | 20/20 | 95.3% +/- 0.2 | 0.946 +/- 0.002 | 194136 +/- 5742 | 4.096 +/- 0.121 | 380ms | 884ms | 1 |

## Frontier shape

The fair corner is **vtc-cache-blind** at 88.1% cache-hit and normalized gap 0.032. The cache corner is **consistent-hash** at 95.3% cache-hit and normalized gap 4.096.

Moving from the fair corner to the cache corner trades +7.2 cache-hit points for a paired normalized-gap increase of 4.065 (95% CI 0.122) across the held-out seeds. The paired gap increase is resolvable beyond zero: on this workload there is a measurable price of fairness (a knee), quantified by the table above.

This is the measured finding, reported as it falls. No policy is claimed to dominate here.

## Regime and caveats

This frontier is measured with 12 tenants against N=4 x 2 slots = 8 aggregate llama-server slots, so the working set (12 distinct tenant prefixes) exceeds the aggregate KV capacity (8 slots): the working set does not fit, so slots thrash and the cache-hit axis is capacity-constrained.

cache-affinity's cache-hit is additionally depressed by the small N=4 fleet: with average load near 2 per backend the bounded-load cap and hotspot replication fire aggressively, spreading a hot prefix off its single warm slot. That is the genuine DualMap load-bounding trade (a faithful baseline, not a strawman), and its cache-hit would recover on a larger fleet.

Cache-hit is measured over each policy's own K completions. Round-robin fair admission serves colder tail-tenant requests than FIFO admission does, so the cache-hit samples are not identical across admission disciplines; this handicaps the fair policy (it serves colder requests) rather than flattering it.

## Per-seed raw results

| seed | policy | hit% | reuse | gap | norm.gap | completed | errors |
|---|---|---|---|---|---|---|---|
| 900000000 | cache-affinity | 87.0% | 0.864 | 213261 | 4.500 | 400 | 0 |
| 900000000 | vtc-cache-blind | 88.0% | 0.873 | 1419 | 0.030 | 400 | 0 |
| 900000000 | jsq-d | 85.0% | 0.844 | 213261 | 4.500 | 400 | 0 |
| 900000000 | consistent-hash | 96.0% | 0.953 | 210417 | 4.440 | 400 | 0 |
| 900000001 | cache-affinity | 86.5% | 0.859 | 203310 | 4.290 | 400 | 0 |
| 900000001 | vtc-cache-blind | 88.0% | 0.874 | 1421 | 0.030 | 400 | 0 |
| 900000001 | jsq-d | 85.5% | 0.849 | 201888 | 4.260 | 400 | 0 |
| 900000001 | consistent-hash | 94.5% | 0.938 | 200466 | 4.230 | 400 | 0 |
| 900000002 | cache-affinity | 87.2% | 0.866 | 217527 | 4.590 | 400 | 0 |
| 900000002 | vtc-cache-blind | 88.8% | 0.881 | 1428 | 0.030 | 400 | 0 |
| 900000002 | jsq-d | 84.8% | 0.842 | 217527 | 4.590 | 400 | 0 |
| 900000002 | consistent-hash | 95.5% | 0.948 | 220371 | 4.650 | 400 | 0 |
| 900000003 | cache-affinity | 86.0% | 0.854 | 199037 | 4.200 | 400 | 0 |
| 900000003 | vtc-cache-blind | 88.2% | 0.876 | 1423 | 0.030 | 400 | 0 |
| 900000003 | jsq-d | 85.8% | 0.852 | 199037 | 4.200 | 400 | 0 |
| 900000003 | consistent-hash | 95.2% | 0.945 | 199037 | 4.200 | 400 | 0 |
| 900000004 | cache-affinity | 86.8% | 0.861 | 191936 | 4.050 | 400 | 0 |
| 900000004 | vtc-cache-blind | 88.0% | 0.874 | 1423 | 0.030 | 400 | 0 |
| 900000004 | jsq-d | 84.8% | 0.842 | 190514 | 4.020 | 400 | 0 |
| 900000004 | consistent-hash | 95.0% | 0.943 | 189092 | 3.990 | 400 | 0 |
| 900000005 | cache-affinity | 87.2% | 0.866 | 193347 | 4.080 | 400 | 0 |
| 900000005 | vtc-cache-blind | 88.0% | 0.874 | 1422 | 0.030 | 400 | 0 |
| 900000005 | jsq-d | 84.8% | 0.842 | 193347 | 4.080 | 400 | 0 |
| 900000005 | consistent-hash | 95.0% | 0.943 | 190503 | 4.020 | 400 | 0 |
| 900000006 | cache-affinity | 87.0% | 0.864 | 189086 | 3.990 | 400 | 0 |
| 900000006 | vtc-cache-blind | 88.2% | 0.876 | 1424 | 0.030 | 400 | 0 |
| 900000006 | jsq-d | 86.2% | 0.856 | 189086 | 3.990 | 400 | 0 |
| 900000006 | consistent-hash | 95.2% | 0.945 | 183398 | 3.870 | 400 | 1 |
| 900000007 | cache-affinity | 87.8% | 0.871 | 196204 | 4.140 | 400 | 0 |
| 900000007 | vtc-cache-blind | 88.0% | 0.874 | 1430 | 0.030 | 400 | 0 |
| 900000007 | jsq-d | 83.2% | 0.827 | 194782 | 4.110 | 400 | 0 |
| 900000007 | consistent-hash | 95.5% | 0.948 | 196204 | 4.140 | 400 | 0 |
| 900000008 | cache-affinity | 87.5% | 0.869 | 208985 | 4.410 | 400 | 0 |
| 900000008 | vtc-cache-blind | 88.0% | 0.874 | 1425 | 0.030 | 400 | 0 |
| 900000008 | jsq-d | 85.2% | 0.847 | 208985 | 4.410 | 400 | 0 |
| 900000008 | consistent-hash | 96.0% | 0.953 | 206141 | 4.350 | 400 | 0 |
| 900000009 | cache-affinity | 87.5% | 0.869 | 181984 | 3.840 | 400 | 0 |
| 900000009 | vtc-cache-blind | 88.0% | 0.874 | 1424 | 0.030 | 400 | 0 |
| 900000009 | jsq-d | 84.2% | 0.837 | 181984 | 3.840 | 400 | 0 |
| 900000009 | consistent-hash | 95.2% | 0.945 | 177718 | 3.750 | 400 | 0 |
| 900000010 | cache-affinity | 86.8% | 0.861 | 183397 | 3.870 | 400 | 0 |
| 900000010 | vtc-cache-blind | 88.5% | 0.879 | 1424 | 0.030 | 400 | 0 |
| 900000010 | jsq-d | 84.8% | 0.842 | 181975 | 3.840 | 400 | 0 |
| 900000010 | consistent-hash | 96.0% | 0.953 | 180553 | 3.810 | 400 | 0 |
| 900000011 | cache-affinity | 86.8% | 0.861 | 179129 | 3.780 | 400 | 0 |
| 900000011 | vtc-cache-blind | 88.0% | 0.874 | 1420 | 0.030 | 400 | 0 |
| 900000011 | jsq-d | 84.5% | 0.839 | 179129 | 3.780 | 400 | 0 |
| 900000011 | consistent-hash | 94.8% | 0.940 | 179129 | 3.780 | 400 | 0 |
| 900000012 | cache-affinity | 88.2% | 0.876 | 190516 | 4.020 | 400 | 0 |
| 900000012 | vtc-cache-blind | 88.0% | 0.874 | 1423 | 0.030 | 400 | 0 |
| 900000012 | jsq-d | 85.0% | 0.844 | 191938 | 4.050 | 400 | 0 |
| 900000012 | consistent-hash | 95.8% | 0.950 | 189094 | 3.990 | 400 | 0 |
| 900000013 | cache-affinity | 85.2% | 0.847 | 184828 | 3.900 | 400 | 0 |
| 900000013 | vtc-cache-blind | 88.0% | 0.874 | 1421 | 0.030 | 400 | 0 |
| 900000013 | jsq-d | 84.5% | 0.839 | 184828 | 3.900 | 400 | 0 |
| 900000013 | consistent-hash | 95.2% | 0.945 | 184828 | 3.900 | 400 | 0 |
| 900000014 | cache-affinity | 86.8% | 0.861 | 211835 | 4.470 | 400 | 0 |
| 900000014 | vtc-cache-blind | 88.2% | 0.876 | 1432 | 0.030 | 400 | 0 |
| 900000014 | jsq-d | 85.2% | 0.847 | 210413 | 4.440 | 400 | 0 |
| 900000014 | consistent-hash | 95.5% | 0.948 | 207569 | 4.380 | 400 | 0 |
| 900000015 | cache-affinity | 86.2% | 0.857 | 179147 | 3.780 | 400 | 0 |
| 900000015 | vtc-cache-blind | 88.0% | 0.874 | 2833 | 0.060 | 400 | 1 |
| 900000015 | jsq-d | 85.5% | 0.849 | 177725 | 3.750 | 400 | 0 |
| 900000015 | consistent-hash | 94.2% | 0.935 | 176303 | 3.720 | 400 | 0 |
| 900000016 | cache-affinity | 87.2% | 0.866 | 190512 | 4.020 | 400 | 0 |
| 900000016 | vtc-cache-blind | 88.2% | 0.876 | 1420 | 0.030 | 400 | 0 |
| 900000016 | jsq-d | 84.2% | 0.837 | 190512 | 4.020 | 400 | 0 |
| 900000016 | consistent-hash | 94.8% | 0.940 | 187668 | 3.960 | 400 | 0 |
| 900000017 | cache-affinity | 86.5% | 0.859 | 207563 | 4.380 | 400 | 0 |
| 900000017 | vtc-cache-blind | 88.2% | 0.876 | 1425 | 0.030 | 400 | 0 |
| 900000017 | jsq-d | 85.5% | 0.849 | 206141 | 4.350 | 400 | 0 |
| 900000017 | consistent-hash | 95.5% | 0.948 | 206141 | 4.350 | 400 | 0 |
| 900000018 | cache-affinity | 87.0% | 0.864 | 203305 | 4.290 | 400 | 0 |
| 900000018 | vtc-cache-blind | 88.2% | 0.876 | 1423 | 0.030 | 400 | 0 |
| 900000018 | jsq-d | 86.0% | 0.854 | 203305 | 4.290 | 400 | 0 |
| 900000018 | consistent-hash | 95.5% | 0.948 | 201883 | 4.260 | 400 | 0 |
| 900000019 | cache-affinity | 87.2% | 0.866 | 196197 | 4.140 | 400 | 0 |
| 900000019 | vtc-cache-blind | 88.0% | 0.874 | 1422 | 0.030 | 400 | 0 |
| 900000019 | jsq-d | 85.5% | 0.849 | 196197 | 4.140 | 400 | 0 |
| 900000019 | consistent-hash | 96.2% | 0.955 | 196197 | 4.140 | 400 | 0 |

