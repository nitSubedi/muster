// Package router decides which nodes should serve a request. It is pure:
// no I/O, no clocks, so every routing rule is unit-tested.
package router

import (
	"cmp"
	"slices"

	"github.com/maauri/muster/internal/node"
)

// Candidate is a healthy node as seen by one gateway.
type Candidate struct {
	Info  node.Info
	Local bool
	// Addr is the peer agent's host:port; empty for the local node.
	Addr string
	// Pending is how many requests this gateway has dispatched to the node
	// that are still open. It covers the gap between dispatch and the node's
	// next InFlight report, which stops bursts piling onto one node.
	Pending int
}

func (c Candidate) load() int { return max(c.Info.InFlight, c.Pending) }

// Rank returns the candidates able to serve model, best first. See
// docs/ARCHITECTURE.md "Routing" for the rule order.
func Rank(model string, nodes []Candidate) []Candidate {
	type scored struct {
		Candidate
		loaded bool
	}
	var able []scored
	for _, n := range nodes {
		if m, ok := n.Info.Model(model); ok {
			able = append(able, scored{n, m.Loaded})
		}
	}
	slices.SortFunc(able, func(a, b scored) int {
		if a.loaded != b.loaded {
			return boolFirst(a.loaded)
		}
		if c := cmp.Compare(a.load(), b.load()); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Info.AccelMemory(), a.Info.AccelMemory()); c != 0 {
			return c
		}
		if a.Local != b.Local {
			return boolFirst(a.Local)
		}
		return cmp.Compare(a.Info.ID, b.Info.ID)
	})
	out := make([]Candidate, len(able))
	for i, s := range able {
		out[i] = s.Candidate
	}
	return out
}

func boolFirst(aTrue bool) int {
	if aTrue {
		return -1
	}
	return 1
}
