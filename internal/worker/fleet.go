package worker

import (
	"fmt"
	"time"
)

// FleetSpec describes a fleet of identical llama-server workers sharing one GGUF
// via mmap. Threads are pinned so N*Threads stays within the physical core
// budget, which the spike showed keeps per-instance TTFT stable under
// concurrency.
type FleetSpec struct {
	Bin        string // llama-server binary
	Model      string // shared GGUF path
	BasePort   int    // first port; workers occupy BasePort..BasePort+N-1
	N          int    // number of workers
	Slots      int    // -np per worker
	CtxTotal   int    // -c per worker
	Threads    int    // -t/-tb per worker
	CacheReuse int    // --cache-reuse per worker
	LogDir     string // per-port log directory
	Cores      int    // physical core budget; 0 skips the budget check
}

// DefaultFleetSpec is the working configuration the spike validated: N=4 real
// backends, 3 threads each (12 <= 14 cores), ctx 16384 (8192 per slot with -np
// 2), the shared tiny GGUF. Callers override Bin, Model, LogDir and, for bounded
// tests, N.
func DefaultFleetSpec() FleetSpec {
	return FleetSpec{
		BasePort:   8100,
		N:          4,
		Slots:      2,
		CtxTotal:   16384,
		Threads:    3,
		CacheReuse: 256,
		Cores:      14,
	}
}

// Fleet is a set of running workers reaped together.
type Fleet struct {
	Workers []*Worker
	spec    FleetSpec
}

// StartFleet launches N workers on consecutive ports and blocks until all are
// healthy. If any worker fails to spawn or become healthy, every worker already
// started is reaped before returning, so a partial launch never leaves orphans.
// The thread budget is enforced up front when Cores is set.
func StartFleet(spec FleetSpec, healthTimeout time.Duration) (*Fleet, error) {
	if spec.N < 1 {
		return nil, fmt.Errorf("fleet: N must be >= 1, got %d", spec.N)
	}
	if spec.Cores > 0 && spec.N*spec.Threads > spec.Cores {
		return nil, fmt.Errorf("fleet: thread budget exceeded: %d workers * %d threads > %d cores",
			spec.N, spec.Threads, spec.Cores)
	}

	f := &Fleet{spec: spec}
	for i := 0; i < spec.N; i++ {
		w, err := Start(Config{
			Bin:        spec.Bin,
			Model:      spec.Model,
			Port:       spec.BasePort + i,
			Slots:      spec.Slots,
			CtxTotal:   spec.CtxTotal,
			Threads:    spec.Threads,
			CacheReuse: spec.CacheReuse,
			LogDir:     spec.LogDir,
		})
		if err != nil {
			f.Stop()
			return nil, fmt.Errorf("fleet: start worker %d: %w", i, err)
		}
		f.Workers = append(f.Workers, w)
	}

	for i, w := range f.Workers {
		if err := w.WaitHealthy(healthTimeout); err != nil {
			f.Stop()
			return nil, fmt.Errorf("fleet: worker %d (port %d): %w", i, w.Port(), err)
		}
	}
	return f, nil
}

// Stop reaps every worker. It is idempotent and safe to defer even after a
// partial or failed launch.
func (f *Fleet) Stop() {
	if f == nil {
		return
	}
	for _, w := range f.Workers {
		w.Stop()
	}
}

// Len is the number of workers in the fleet.
func (f *Fleet) Len() int {
	if f == nil {
		return 0
	}
	return len(f.Workers)
}
