// Package router holds the four faithfully reimplemented routing policies whose
// cache-hit / cross-tenant service-gap trade-off is the finding. A policy maps an
// incoming request (a tenant and the token prefix it shares with other requests)
// to a backend index, using the live fleet state.
//
// The policies are pure routing logic over fleet state, so they are exercised
// here against a mock fleet without spawning any llama-server. The real-backend
// frontier measurement drives these same policies against live servers, but the
// decision logic under test is identical.
package router

import (
	"hash/fnv"
	"sort"
)

// Request is one routing decision's input. Prefix is the shared token prefix (the
// system prompt a set of requests reuse) and is what cache-aware policies hash on
// for KV reuse. Tenant identifies the client for fairness accounting. InputTokens
// and OutputTokens size the request for the VTC cost function; OutputTokens may be
// an estimate at routing time and reconciled after the real completion.
type Request struct {
	Tenant       string
	Prefix       string
	InputTokens  int
	OutputTokens int
}

// FleetState is the live view a policy routes against: how many backends exist and
// the current outstanding load on each. Load is the count of in-flight requests a
// backend is working, the signal JSQ and the cache-affinity bounded-load overflow
// balance on.
type FleetState interface {
	N() int
	Load(i int) int
}

// Router chooses a backend for a request given the current fleet state. Every
// policy implements this; it is the whole contract the measurement harness needs.
type Router interface {
	Route(req Request, state FleetState) int
	Name() string
}

// Scheduler is an optional capability for a policy that also controls dispatch
// order to enforce fairness, not just placement. VTC implements it: given a batch
// of pending requests it returns their indices in least-served-tenant order. The
// three placement-only policies do not implement it.
type Scheduler interface {
	Order(pending []Request) []int
}

// avgLoad is the mean outstanding load across the fleet, the reference the
// bounded-load cap is built on.
func avgLoad(state FleetState) float64 {
	n := state.N()
	if n <= 0 {
		return 0
	}
	total := 0
	for i := 0; i < n; i++ {
		total += state.Load(i)
	}
	return float64(total) / float64(n)
}

// ring is a consistent-hash ring with virtual nodes. Each backend owns replicas
// virtual points on the hash circle, so a prefix maps to a deterministic backend
// and the mapping stays stable and near-uniform as backends are added or removed.
// The ring is the shared cache-locality primitive: consistent-hash routes to the
// prefix's single home, and cache-affinity walks the ring from that home to build
// a replica set for load spreading.
type ring struct {
	points   []ringPoint // sorted by hash position
	n        int         // number of backends
	replicas int         // virtual nodes per backend
}

type ringPoint struct {
	pos     uint64
	backend int
}

// newRing builds a ring over n backends with the given virtual-node count. More
// virtual nodes give a smoother key distribution; a handful per backend is enough
// for the small fleets here.
func newRing(n, replicas int) *ring {
	if replicas < 1 {
		replicas = 1
	}
	r := &ring{n: n, replicas: replicas}
	for b := 0; b < n; b++ {
		for v := 0; v < replicas; v++ {
			r.points = append(r.points, ringPoint{
				pos:     hashVirtual(b, v),
				backend: b,
			})
		}
	}
	sort.Slice(r.points, func(i, j int) bool { return r.points[i].pos < r.points[j].pos })
	return r
}

// home returns the backend that owns a prefix: the first virtual node clockwise
// from the prefix's hash position. This is the cache home a request reuses KV on.
func (r *ring) home(prefix string) int {
	return r.walk(prefix, 1)[0]
}

// walk returns up to count distinct backends encountered clockwise from the
// prefix's hash position, in ring order. The first is the home; the rest are the
// natural replica set a hot prefix spreads onto. It never returns more distinct
// backends than the fleet has.
func (r *ring) walk(prefix string, count int) []int {
	if r.n == 0 {
		return []int{0}
	}
	if count > r.n {
		count = r.n
	}
	start := sort.Search(len(r.points), func(i int) bool {
		return r.points[i].pos >= hashKey(prefix)
	})
	out := make([]int, 0, count)
	seen := make(map[int]bool, count)
	for i := 0; i < len(r.points) && len(out) < count; i++ {
		p := r.points[(start+i)%len(r.points)]
		if !seen[p.backend] {
			seen[p.backend] = true
			out = append(out, p.backend)
		}
	}
	return out
}

// hashKey maps a prefix string to a position on the hash circle. FNV alone
// avalanches poorly on near-identical inputs (real trace prefixes are shared
// system prompts differing only in a suffix), which would cluster similar keys
// onto a couple of backends; the splitmix64 finalizer decorrelates them so the
// ring stays near-uniform on realistic keys.
func hashKey(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return mix64(h.Sum64())
}

// hashVirtual maps a (backend, virtual-node) pair to a ring position, salted so a
// backend's replicas scatter around the circle instead of clustering.
func hashVirtual(backend, v int) uint64 {
	h := fnv.New64a()
	var buf [16]byte
	putUint64(buf[0:8], uint64(backend))
	putUint64(buf[8:16], uint64(v))
	_, _ = h.Write(buf[:])
	return mix64(h.Sum64())
}

// mix64 is the splitmix64 finalizer, a bijective avalanche mix that turns FNV's
// weak per-byte diffusion into full 64-bit scatter so similar inputs land far
// apart on the ring.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func putUint64(b []byte, v uint64) {
	for i := 0; i < 8; i++ {
		b[i] = byte(v >> (8 * i))
	}
}
