package router

import (
	"testing"

	"github.com/maauri/muster/internal/node"
)

func cand(id string, local bool, mem uint64, inflight, pending int, models ...node.Model) Candidate {
	return Candidate{
		Local:   local,
		Pending: pending,
		Info:    node.Info{ID: id, MemoryBytes: mem, InFlight: inflight, Models: models},
	}
}

func m(name string, loaded bool) node.Model { return node.Model{Name: name, Loaded: loaded} }

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Info.ID
	}
	return out
}

func assertOrder(t *testing.T, got []Candidate, want ...string) {
	t.Helper()
	g := ids(got)
	if len(g) != len(want) {
		t.Fatalf("got %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("got %v, want %v", g, want)
		}
	}
}

func TestRankExcludesNodesWithoutModel(t *testing.T) {
	got := Rank("llama3", []Candidate{
		cand("a", true, 16, 0, 0, m("qwen:7b", false)),
		cand("b", false, 16, 0, 0, m("llama3:latest", false)),
	})
	assertOrder(t, got, "b")
}

func TestRankNoMatchReturnsEmpty(t *testing.T) {
	if got := Rank("missing", []Candidate{cand("a", true, 16, 0, 0, m("qwen:7b", false))}); len(got) != 0 {
		t.Fatalf("got %v", ids(got))
	}
}

func TestRankPrefersLoadedOverIdle(t *testing.T) {
	got := Rank("llama3", []Candidate{
		cand("cold", true, 64, 0, 0, m("llama3", false)),
		cand("warm", false, 16, 2, 0, m("llama3", true)),
	})
	assertOrder(t, got, "warm", "cold")
}

func TestRankPrefersLessBusy(t *testing.T) {
	got := Rank("llama3", []Candidate{
		cand("busy", false, 64, 3, 0, m("llama3", true)),
		cand("idle", false, 16, 0, 0, m("llama3", true)),
	})
	assertOrder(t, got, "idle", "busy")
}

func TestRankCountsPendingDispatches(t *testing.T) {
	// "a" reports 0 in flight, but this gateway just sent it 2 requests the
	// node hasn't reported yet. Effective load is max(reported, pending).
	got := Rank("llama3", []Candidate{
		cand("a", false, 64, 0, 2, m("llama3", true)),
		cand("b", false, 64, 1, 0, m("llama3", true)),
	})
	assertOrder(t, got, "b", "a")
}

func TestRankPrefersMoreMemoryThenLocal(t *testing.T) {
	got := Rank("llama3", []Candidate{
		cand("small-local", true, 16, 0, 0, m("llama3", false)),
		cand("big-remote", false, 64, 0, 0, m("llama3", false)),
		cand("small-remote", false, 16, 0, 0, m("llama3", false)),
	})
	assertOrder(t, got, "big-remote", "small-local", "small-remote")
}

func TestRankDeterministicByID(t *testing.T) {
	got := Rank("llama3", []Candidate{
		cand("z", false, 16, 0, 0, m("llama3", false)),
		cand("a", false, 16, 0, 0, m("llama3", false)),
	})
	assertOrder(t, got, "a", "z")
}

func TestRankDoesNotMutateInput(t *testing.T) {
	in := []Candidate{
		cand("z", false, 16, 0, 0, m("llama3", false)),
		cand("a", false, 16, 0, 0, m("llama3", false)),
	}
	Rank("llama3", in)
	if in[0].Info.ID != "z" {
		t.Fatal("input slice was reordered")
	}
}
