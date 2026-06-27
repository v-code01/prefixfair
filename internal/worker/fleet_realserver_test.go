package worker

import (
	"context"
	"testing"
	"time"
)

// TestFleetConcurrentServe launches a small real fleet, drives load across it
// under real concurrency, and checks every worker serves. It uses N=2 to keep
// runtime bounded; the controller runs the full N=4 sweep later. This exercises
// the concurrency-aware measurement path: TTFT is read as observed under load,
// not solo, per the spike's carry concern.
func TestFleetConcurrentServe(t *testing.T) {
	bin, model := binModel(t)

	spec := FleetSpec{
		Bin:        bin,
		Model:      model,
		BasePort:   8130,
		N:          2,
		Slots:      2,
		CtxTotal:   8192,
		Threads:    3,
		CacheReuse: 256,
		LogDir:     t.TempDir(),
		Cores:      14,
	}
	f, err := StartFleet(spec, 90*time.Second)
	if err != nil {
		t.Fatalf("StartFleet: %v", err)
	}
	// Reap every worker even if an assertion fails: the no-orphans guarantee.
	t.Cleanup(f.Stop)

	if f.Len() != spec.N {
		t.Fatalf("fleet size %d, want %d", f.Len(), spec.N)
	}

	ctx := context.Background()

	// Build a small batch of distinct (miss-style) jobs spread round-robin across
	// the fleet. Distinct prefixes make every worker do real prefill work, so the
	// observed TTFT reflects genuine load, not cache shortcuts.
	const perWorker = 3
	var jobs []Job
	for i := 0; i < spec.N*perWorker; i++ {
		jobs = append(jobs, Job{
			WorkerIndex: i % spec.N,
			Request:     Request{Prompt: MissPrompt(60, 1000+i), NPredict: 4, CachePrompt: true},
		})
	}

	// Drive at a modest arrival rate with bounded in-flight concurrency so the
	// fleet stays at a sustainable load.
	obs := Drive(ctx, f, jobs, 4.0, spec.N, DefaultHitFraction)
	if len(obs) != len(jobs) {
		t.Fatalf("got %d observations, want %d", len(obs), len(jobs))
	}

	served := make([]int, spec.N)
	for i, o := range obs {
		if o.Err != nil {
			t.Fatalf("job %d (worker %d) errored under concurrency: %v", i, o.WorkerIndex, o.Err)
		}
		if o.Result.TTFT <= 0 {
			t.Fatalf("job %d produced non-positive TTFT %v", i, o.Result.TTFT)
		}
		served[o.WorkerIndex]++
	}
	for idx, c := range served {
		if c != perWorker {
			t.Fatalf("worker %d served %d requests, want %d", idx, c, perWorker)
		}
		t.Logf("worker %d served %d requests under concurrency", idx, c)
	}
}

// TestFleetFailedWorkerSurfacesError proves a fleet launch that cannot become
// healthy surfaces an error and, critically, reaps everything it already
// started. A bogus model path makes the second worker exit during load, which
// StartFleet must detect via WaitHealthy and turn into an error, leaving no
// orphaned processes.
func TestFleetFailedWorkerSurfacesError(t *testing.T) {
	bin, model := binModel(t)

	spec := FleetSpec{
		Bin:      bin,
		Model:    model + ".does-not-exist.gguf",
		BasePort: 8140,
		N:        2,
		Slots:    2,
		CtxTotal: 4096,
		Threads:  3,
		LogDir:   t.TempDir(),
		Cores:    14,
	}
	f, err := StartFleet(spec, 20*time.Second)
	if f != nil {
		// Defensive: on the off chance a fleet came back, never leak it.
		t.Cleanup(f.Stop)
	}
	if err == nil {
		t.Fatal("StartFleet with a bogus model must return an error")
	}
	t.Logf("failed fleet correctly surfaced: %v", err)
}

// TestFleetThreadBudgetEnforced rejects an oversubscribed fleet up front rather
// than spawning processes that would poison TTFT. Pure check, no model load.
func TestFleetThreadBudgetEnforced(t *testing.T) {
	_, err := StartFleet(FleetSpec{
		Bin: "x", Model: "y", BasePort: 8150, N: 8, Threads: 3, Cores: 14,
	}, time.Second)
	if err == nil {
		t.Fatal("fleet exceeding the core budget must be rejected")
	}
}
