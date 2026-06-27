package router

// ConsistentHash is the sticky prefix-hash baseline: a prefix always routes to its
// consistent-hash home backend, so repeated requests on the same prefix land on
// the same slot and reuse its KV cache. It is cache-friendly by construction and
// deliberately load- and fairness-blind: it never consults backend load, so a hot
// prefix pins its whole tenant to one backend. That blindness is the point of the
// baseline, not a crippling of a stronger method; it marks the cache-maximal,
// fairness-minimal corner of the frontier.
type ConsistentHash struct {
	ring *ring
}

// NewConsistentHash builds the policy over n backends. virtualNodes controls the
// smoothness of the key distribution; pass 0 for a sensible default.
func NewConsistentHash(n, virtualNodes int) *ConsistentHash {
	if virtualNodes < 1 {
		virtualNodes = defaultVirtualNodes
	}
	return &ConsistentHash{ring: newRing(n, virtualNodes)}
}

// Route returns the prefix's home backend, ignoring load entirely.
func (c *ConsistentHash) Route(req Request, _ FleetState) int {
	return c.ring.home(req.Prefix)
}

// Name identifies the policy in reports.
func (c *ConsistentHash) Name() string { return "consistent-hash" }

// defaultVirtualNodes is the per-backend virtual-node count used when a caller
// does not specify one. A modest count keeps small fleets near-uniform.
const defaultVirtualNodes = 64
