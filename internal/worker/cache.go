package worker

import "fmt"

// CacheOutcome classifies whether a completion reused a real per-slot KV prefix
// cache. It is derived only from server-reported timings, so it reflects what the
// backend actually did, not what the client hoped for.
type CacheOutcome int

const (
	// CacheMiss means the server prefilled essentially the whole prompt.
	CacheMiss CacheOutcome = iota
	// CacheHit means the server skipped essentially the whole prompt by reusing
	// a resident KV prefix.
	CacheHit
)

func (o CacheOutcome) String() string {
	switch o {
	case CacheHit:
		return "HIT"
	case CacheMiss:
		return "MISS"
	default:
		return fmt.Sprintf("CacheOutcome(%d)", int(o))
	}
}

// ReuseFraction is the share of the prompt the server reused from cache, in
// [0,1]. It is cache_n / (cache_n + prompt_n): reused tokens over total prompt
// tokens the server accounted for. A full prefill is 0; a full prefix skip
// approaches 1.
func (t Timings) ReuseFraction() float64 {
	total := t.CacheN + t.PromptN
	if total <= 0 {
		return 0
	}
	return float64(t.CacheN) / float64(total)
}

// DefaultHitFraction is the reuse share above which a completion counts as a
// real cache hit. A genuine prefix skip reuses nearly the entire prompt, so the
// bar sits well clear of both a full miss (near 0) and the handful of boundary
// tokens the server re-evaluates on a hit.
const DefaultHitFraction = 0.5

// ClassifyCache decides HIT vs MISS from server timings. hitFraction is the
// minimum ReuseFraction required to call it a hit; pass DefaultHitFraction for
// the standard bar. The decision uses only real server counters, so it is the
// realness anchor: a repeated long prefix must actually reuse KV to register as a
// hit, and a distinct prefix must actually prefill to register as a miss.
func ClassifyCache(t Timings, hitFraction float64) CacheOutcome {
	if t.ReuseFraction() >= hitFraction {
		return CacheHit
	}
	return CacheMiss
}
