package frontier

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Stat is a sample mean with a 95% confidence half-width across seeds.
type Stat struct {
	Mean float64
	CI   float64 // 95% half-width; 0 when fewer than two seeds
}

func (s Stat) String() string {
	if s.CI == 0 {
		return fmt.Sprintf("%.3f", s.Mean)
	}
	return fmt.Sprintf("%.3f +/- %.3f", s.Mean, s.CI)
}

// Aggregate is one policy's frontier point summarized over the seeds where it
// actually reached the snapshot, with confidence intervals on the held-out seed set.
type Aggregate struct {
	Policy     string
	TotalSeeds int // seeds swept for this policy
	ValidSeeds int // seeds that reached the snapshot (the ones the stats are over)
	HitRate    Stat
	MeanReuse  Stat
	Gap        Stat
	NormGap    Stat
	TTFTp50    time.Duration // mean across valid seeds
	TTFTp95    time.Duration
	TTFTMean   time.Duration
	Errors     int
	Backlogged bool // true only if every VALID seed stayed backlogged at the snapshot
}

// Reliable reports whether this policy has enough valid seeds to be trusted as a
// frontier point. An unreliable policy crashed too many seeds and its surviving
// average is a survivorship-biased sample, so it is not placed on the frontier.
func (a Aggregate) Reliable() bool { return reliable(a.ValidSeeds, a.TotalSeeds) }

// reliable is the trust rule for a policy's row: at least three valid seeds (below
// which a confidence interval is meaningless) AND at least three quarters of the
// swept seeds valid (below which the surviving sample is too selective to trust,
// since failures tend to correlate with the hardest trace draws).
func reliable(valid, total int) bool {
	return valid >= 3 && valid*4 >= total*3
}

// Summarize reduces per-seed results into one confidence-bounded point per policy,
// preserving the policy reporting order. Statistics are computed ONLY over the seeds
// that reached the snapshot (Valid): a crashed seed produced no frontier point, and
// averaging its spurious (hit=0, gap=0) in would silently deflate the row. The count
// of valid versus total seeds is retained so the row's reliability is visible.
func Summarize(results []SeedResult) []Aggregate {
	if len(results) == 0 {
		return nil
	}
	order := []string{}
	byPolicy := map[string][]PolicyResult{}
	for _, sr := range results {
		for _, pr := range sr.Policies {
			if _, ok := byPolicy[pr.Policy]; !ok {
				order = append(order, pr.Policy)
			}
			byPolicy[pr.Policy] = append(byPolicy[pr.Policy], pr)
		}
	}

	out := make([]Aggregate, 0, len(order))
	for _, name := range order {
		rs := byPolicy[name]
		hit, reuse, gap, ng := []float64{}, []float64{}, []float64{}, []float64{}
		var p50, p95, mean time.Duration
		errs := 0
		valid := 0
		backlogged := true
		for _, r := range rs {
			errs += r.Errors // errors are summed over ALL seeds, valid or not
			if !r.Valid {
				continue // exclude crashed seeds from every measured statistic
			}
			valid++
			hit = append(hit, r.HitRate)
			reuse = append(reuse, r.MeanReuse)
			gap = append(gap, r.ServiceGap)
			ng = append(ng, r.NormalizedGap)
			p50 += r.TTFTp50
			p95 += r.TTFTp95
			mean += r.TTFTMean
			backlogged = backlogged && r.Backlogged
		}
		agg := Aggregate{
			Policy:     name,
			TotalSeeds: len(rs),
			ValidSeeds: valid,
			HitRate:    meanCI(hit),
			MeanReuse:  meanCI(reuse),
			Gap:        meanCI(gap),
			NormGap:    meanCI(ng),
			Errors:     errs,
			Backlogged: valid > 0 && backlogged,
		}
		if valid > 0 {
			nn := time.Duration(valid)
			agg.TTFTp50 = p50 / nn
			agg.TTFTp95 = p95 / nn
			agg.TTFTMean = mean / nn
		}
		out = append(out, agg)
	}
	return out
}

// meanCI computes the sample mean and the 95% confidence half-width using the
// Student-t critical value for the sample's degrees of freedom, which is the honest
// interval for the small seed counts a real-backend sweep affords.
func meanCI(xs []float64) Stat {
	n := len(xs)
	if n == 0 {
		return Stat{}
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	if n < 2 {
		return Stat{Mean: mean}
	}
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	sd := math.Sqrt(ss / float64(n-1))
	sem := sd / math.Sqrt(float64(n))
	return Stat{Mean: mean, CI: tCritical(n-1) * sem}
}

// tCritical returns the two-sided 95% Student-t critical value for df degrees of
// freedom, falling back to the normal 1.96 for df beyond the table.
func tCritical(df int) float64 {
	table := map[int]float64{
		1: 12.706, 2: 4.303, 3: 3.182, 4: 2.776, 5: 2.571,
		6: 2.447, 7: 2.365, 8: 2.306, 9: 2.262, 10: 2.228,
		11: 2.201, 12: 2.179, 13: 2.160, 14: 2.145, 15: 2.131,
		16: 2.120, 17: 2.110, 18: 2.101, 19: 2.093, 20: 2.086,
		21: 2.080, 22: 2.074, 23: 2.069, 24: 2.064, 25: 2.060,
		26: 2.056, 27: 2.052, 28: 2.048, 29: 2.045, 30: 2.042,
	}
	if v, ok := table[df]; ok {
		return v
	}
	return 1.96
}

// WriteReport renders the frontier to a markdown file: the per-policy frontier
// table with held-out-seed CIs, a factual statement of the frontier shape (flat vs
// knee, decided by whether the gap spread across policies exceeds the confidence
// intervals), and the per-seed raw numbers. It claims no winning policy; that is a
// separate deliverable.
func WriteReport(path string, cfg Config, seeds []int64, results []SeedResult) error {
	aggs := Summarize(results)
	var b strings.Builder

	fmt.Fprintf(&b, "# prefixfair frontier: cache-hit%% vs cross-tenant service-gap\n\n")
	fmt.Fprintf(&b, "Real N=%d llama-server fleet. Every latency and cache-reuse number is measured from a real server slot; nothing in the measured path is simulated. ", cfg.Fleet.N)
	fmt.Fprintf(&b, "The service gap is the max-min cross-tenant weighted-token service read at the K-th completion, a snapshot taken while every tenant is still backlogged, which is exactly VTC's assumption.\n\n")

	fmt.Fprintf(&b, "## Configuration\n\n")
	fmt.Fprintf(&b, "- Tenants: %d, Zipf s=%.2f, requests/seed: %d, prefix sentences: %d (~1.3k tokens)\n",
		cfg.Trace.Tenants, cfg.Trace.ZipfS, cfg.Trace.Requests, cfg.Trace.PrefixSentences)
	fmt.Fprintf(&b, "- Snapshot at K=%d completions; max in-flight: %d; n_predict: %d\n",
		cfg.Completions, effectiveMaxInFlight(cfg), cfg.NPredict)
	fmt.Fprintf(&b, "- Fleet: N=%d, slots=%d, ctx=%d (%d/slot), threads=%d each\n",
		cfg.Fleet.N, cfg.Fleet.Slots, cfg.Fleet.CtxTotal, cfg.Fleet.CtxTotal/max2(cfg.Fleet.Slots, 1), cfg.Fleet.Threads)
	fmt.Fprintf(&b, "- VTC cost weights: wIn=%.1f, wOut=%.1f\n", cfg.WIn, cfg.WOut)
	fmt.Fprintf(&b, "- Held-out seeds (%d): %v\n\n", len(seeds), seeds)

	fmt.Fprintf(&b, "## Frontier (mean +/- 95%% CI over the valid held-out seeds)\n\n")
	fmt.Fprintf(&b, "Statistics are over the seeds that reached the snapshot; the seeds column shows valid/total. A row marked unreliable (fewer than 75%% of seeds valid) is a survivorship-biased sample and is excluded from the frontier shape below.\n\n")
	fmt.Fprintf(&b, "| policy | seeds | cache-hit%% | mean reuse | service gap | norm. gap | TTFT p50 | TTFT p95 | errors |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|---|---|---|\n")
	for _, a := range aggs {
		seedCell := fmt.Sprintf("%d/%d", a.ValidSeeds, a.TotalSeeds)
		if !a.Reliable() {
			seedCell += " (unreliable)"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %.0f +/- %.0f | %s | %s | %s | %d |\n",
			a.Policy, seedCell,
			pct(a.HitRate), a.MeanReuse.String(),
			a.Gap.Mean, a.Gap.CI, a.NormGap.String(),
			dur(a.TTFTp50), dur(a.TTFTp95), a.Errors)
	}
	b.WriteString("\n")

	writeShape(&b, aggs, results)
	writeBacklogNote(&b, aggs)
	writeRaw(&b, results)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// writeShape states the frontier shape factually by naming its two Pareto corners:
// the fair corner (lowest normalized service gap) and the cache corner (highest
// cache-hit%). It reports the trade between them and whether it is a knee (a
// measurable price of fairness) or flat (fairness nearly free), tested on the
// PAIRED per-seed gap difference. Pairing is the right test here: the absolute gap
// magnitude swings with each seed's Zipf draw, but that swing is common to all
// policies on a seed, so the per-seed difference cancels it and isolates the real
// separation. No policy is declared a winner.
func writeShape(b *strings.Builder, aggs []Aggregate, results []SeedResult) {
	// The frontier corners are chosen ONLY among reliable policies: a policy that
	// crashed most of its seeds has a survivorship-biased average and cannot anchor a
	// corner. Unreliable policies are named so their exclusion is explicit.
	var trusted []Aggregate
	var dropped []string
	for _, a := range aggs {
		if a.Reliable() {
			trusted = append(trusted, a)
		} else {
			dropped = append(dropped, fmt.Sprintf("%s (%d/%d valid)", a.Policy, a.ValidSeeds, a.TotalSeeds))
		}
	}
	if len(dropped) > 0 {
		fmt.Fprintf(b, "## Frontier shape\n\n")
		fmt.Fprintf(b, "Excluded from the frontier as unreliable (too few valid seeds; their failures correlate with the hardest trace draws, so the surviving average is biased): %s.\n\n", strings.Join(dropped, ", "))
	}
	if len(trusted) < 2 {
		fmt.Fprintf(b, "Fewer than two policies produced a reliable frontier point on this run; the shape is not stated. Re-run on an uncontended machine so every policy reaches the snapshot on every seed.\n\n")
		return
	}
	fair, cache := trusted[0], trusted[0]
	for _, a := range trusted {
		if a.NormGap.Mean < fair.NormGap.Mean {
			fair = a
		}
		if a.HitRate.Mean > cache.HitRate.Mean {
			cache = a
		}
	}

	if len(dropped) == 0 {
		fmt.Fprintf(b, "## Frontier shape\n\n")
	}
	fmt.Fprintf(b, "The fair corner is **%s** at %.1f%% cache-hit and normalized gap %.3f. ",
		fair.Policy, fair.HitRate.Mean*100, fair.NormGap.Mean)
	fmt.Fprintf(b, "The cache corner is **%s** at %.1f%% cache-hit and normalized gap %.3f.\n\n",
		cache.Policy, cache.HitRate.Mean*100, cache.NormGap.Mean)

	if fair.Policy == cache.Policy {
		fmt.Fprintf(b, "One policy holds both corners on this workload. That is reported as measured; adjudicating a dominating policy is a separate deliverable, not claimed here.\n\n")
		return
	}

	paired := pairedNormGapDiff(results, cache.Policy, fair.Policy)
	hitDiff := (cache.HitRate.Mean - fair.HitRate.Mean) * 100
	fmt.Fprintf(b, "Moving from the fair corner to the cache corner trades +%.1f cache-hit points for a paired normalized-gap increase of %.3f (95%% CI %.3f) across the held-out seeds. ",
		hitDiff, paired.Mean, paired.CI)
	if paired.Mean-paired.CI > 0 && paired.CI > 0 {
		fmt.Fprintf(b, "The paired gap increase is resolvable beyond zero: on this workload there is a measurable price of fairness (a knee), quantified by the table above.\n\n")
	} else {
		fmt.Fprintf(b, "The paired gap increase is not resolvable beyond its confidence interval (too few seeds or genuinely small): the knee is not established at this seed count.\n\n")
	}
	fmt.Fprintf(b, "This is the measured finding, reported as it falls. No policy is claimed to dominate here.\n\n")
}

// pairedNormGapDiff returns the mean and 95% CI of the per-seed difference
// (aNormGap - bNormGap), the paired test that cancels the seed-common Zipf-draw
// variance. Only seeds where both policies produced a result contribute.
func pairedNormGapDiff(results []SeedResult, a, b string) Stat {
	diffs := []float64{}
	for _, sr := range results {
		av, aok := normGapOf(sr, a)
		bv, bok := normGapOf(sr, b)
		if aok && bok {
			diffs = append(diffs, av-bv)
		}
	}
	return meanCI(diffs)
}

// normGapOf returns a policy's normalized gap in a seed's result, but only if that
// seed was valid for the policy. A crashed seed has no frontier point, so pairing
// its (0,0) would fabricate a difference; it is treated as absent.
func normGapOf(sr SeedResult, policy string) (float64, bool) {
	for _, pr := range sr.Policies {
		if pr.Policy == policy {
			return pr.NormalizedGap, pr.Valid
		}
	}
	return 0, false
}

// writeBacklogNote surfaces whether the snapshot honored the backlogged assumption
// on every seed, since the gap is only inside VTC's scope while it did.
func writeBacklogNote(b *strings.Builder, aggs []Aggregate) {
	allOK := true
	for _, a := range aggs {
		if !a.Backlogged {
			allOK = false
		}
	}
	if allOK {
		return
	}
	fmt.Fprintf(b, "> Note: at least one policy/seed exhausted a tenant's work before the snapshot, so its gap is partly outside VTC's backlogged assumption. Lower K or raise requests/seed to keep every tenant backlogged.\n\n")
}

// writeRaw dumps the per-seed numbers so the CIs are auditable.
func writeRaw(b *strings.Builder, results []SeedResult) {
	fmt.Fprintf(b, "## Per-seed raw results\n\n")
	fmt.Fprintf(b, "| seed | policy | hit%% | reuse | gap | norm.gap | completed | errors |\n")
	fmt.Fprintf(b, "|---|---|---|---|---|---|---|---|\n")
	sorted := make([]SeedResult, len(results))
	copy(sorted, results)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seed < sorted[j].Seed })
	for _, sr := range sorted {
		for _, pr := range sr.Policies {
			fmt.Fprintf(b, "| %d | %s | %.1f%% | %.3f | %.0f | %.3f | %d | %d |\n",
				sr.Seed, pr.Policy, pr.HitRate*100, pr.MeanReuse, pr.ServiceGap,
				pr.NormalizedGap, pr.Completed, pr.Errors)
		}
	}
	b.WriteString("\n")
}

func pct(s Stat) string {
	if s.CI == 0 {
		return fmt.Sprintf("%.1f%%", s.Mean*100)
	}
	return fmt.Sprintf("%.1f%% +/- %.1f", s.Mean*100, s.CI*100)
}

func dur(d time.Duration) string { return d.Round(time.Millisecond).String() }

func effectiveMaxInFlight(cfg Config) int {
	if cfg.MaxInFlight > 0 {
		return cfg.MaxInFlight
	}
	return cfg.Fleet.N * cfg.Fleet.Slots
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
