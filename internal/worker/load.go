package worker

import (
	"context"
	"sync"
	"time"
)

// Job is one request bound to a chosen worker. The routing decision (which worker
// index) is made by the caller; this package only measures what the backend does
// with it.
type Job struct {
	WorkerIndex int
	Request     Request
}

// Observation is the measured outcome of one job. TTFT is the time to first token
// observed under whatever concurrency was live when the job ran, which is the
// honest quantity: the spike showed solo TTFT understates by ~3.4x at N=4, so the
// frontier must be read from TTFT observed under the load the policy induces, not
// from solo timings. Err is surfaced, never swallowed: a failed job carries its
// error and is not counted as a success.
type Observation struct {
	WorkerIndex int
	Cache       CacheOutcome
	Result      Result
	Err         error
	// Enqueued is when the job was released to the fleet; Started is when its
	// request was actually dispatched. Started-Enqueued is queueing delay under
	// load, distinct from the server-side TTFT in Result.
	Enqueued time.Time
	Started  time.Time
}

// TTFT reports the server-side time to first token for a successful observation.
func (o Observation) TTFT() time.Duration { return o.Result.TTFT }

// OK reports whether the job produced a real datapoint.
func (o Observation) OK() bool { return o.Err == nil }

// Drive dispatches jobs across the fleet under real concurrency and returns one
// Observation per job in input order. Jobs are released at a target arrival rate
// (ratePerSec; <= 0 releases them as fast as maxInFlight allows) and at most
// maxInFlight run at once, which keeps the fleet at a sustainable load rather
// than an unbounded stampede. Every job yields exactly one Observation, so a
// failure is surfaced in place and never silently dropped.
//
// hitFraction is the reuse bar passed to ClassifyCache; pass DefaultHitFraction
// for the standard threshold.
func Drive(ctx context.Context, f *Fleet, jobs []Job, ratePerSec float64, maxInFlight int, hitFraction float64) []Observation {
	obs := make([]Observation, len(jobs))
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	sem := make(chan struct{}, maxInFlight)
	var wg sync.WaitGroup

	var interval time.Duration
	if ratePerSec > 0 {
		interval = time.Duration(float64(time.Second) / ratePerSec)
	}
	next := time.Now()

	for i := range jobs {
		// Pace releases to the target arrival rate. This is an open-loop schedule:
		// queueing that builds up under load is real and shows up in the observed
		// TTFT, which is the point.
		if interval > 0 {
			if d := time.Until(next); d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					obs[i].Err = ctx.Err()
					obs[i].WorkerIndex = jobs[i].WorkerIndex
					continue
				}
			}
			next = next.Add(interval)
		}

		enq := time.Now()
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			obs[i].Err = ctx.Err()
			obs[i].WorkerIndex = jobs[i].WorkerIndex
			continue
		}

		wg.Add(1)
		go func(idx int, job Job, enqueued time.Time) {
			defer wg.Done()
			defer func() { <-sem }()

			o := Observation{WorkerIndex: job.WorkerIndex, Enqueued: enqueued, Started: time.Now()}
			if job.WorkerIndex < 0 || job.WorkerIndex >= len(f.Workers) {
				o.Err = errBadWorkerIndex(job.WorkerIndex, len(f.Workers))
				obs[idx] = o
				return
			}
			res, err := f.Workers[job.WorkerIndex].Complete(ctx, job.Request)
			if err != nil {
				o.Err = err
				obs[idx] = o
				return
			}
			o.Result = res
			o.Cache = ClassifyCache(res.Timings, hitFraction)
			obs[idx] = o
		}(i, jobs[i], enq)
	}

	wg.Wait()
	return obs
}
