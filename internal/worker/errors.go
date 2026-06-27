package worker

import "fmt"

// errBadWorkerIndex is returned when a Job names a worker outside the fleet.
func errBadWorkerIndex(idx, n int) error {
	return fmt.Errorf("job: worker index %d out of range [0,%d)", idx, n)
}
