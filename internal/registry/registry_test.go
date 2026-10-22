package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maauri/muster/internal/node"
)

// fakePeer serves node.Info and can be told to fail.
type fakePeer struct {
	srv  *httptest.Server
	info node.Info
	fail atomic.Bool
}

func newFakePeer(t *testing.T, token string, info node.Info) *fakePeer {
	f := &fakePeer{info: info}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if f.fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(f.info)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePeer) addr() string { return strings.TrimPrefix(f.srv.URL, "http://") }

func peerInfo(id string) node.Info {
	return node.Info{ID: id, BackendUp: true, Models: []node.Model{{Name: "llama3:latest"}}}
}

func TestStaticPeerBecomesCandidate(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := New("tok")
	r.SetSelf(node.Info{ID: "lap", BackendUp: true})
	r.AddStatic(p.addr())

	if got := r.Candidates(); len(got) != 1 {
		t.Fatalf("unpolled peer must not be a candidate: %+v", got)
	}
	r.PollOnce(context.Background())
	got := r.Candidates()
	if len(got) != 2 || !got[0].Local || got[1].Info.ID != "desk" || got[1].Addr != p.addr() {
		t.Fatalf("got %+v", got)
	}
}

func TestWrongTokenNeverHealthy(t *testing.T) {
	p := newFakePeer(t, "right", peerInfo("desk"))
	r := New("wrong")
	r.AddStatic(p.addr())
	r.PollOnce(context.Background())
	if len(r.Candidates()) != 0 {
		t.Fatal("peer with wrong token became a candidate")
	}
	if st := r.Status(); st[1].Error == "" {
		t.Fatalf("expected error in status: %+v", st[1])
	}
}

func TestUnhealthyAfterRepeatedFailures(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := New("tok")
	r.AddStatic(p.addr())
	r.PollOnce(context.Background())

	p.fail.Store(true)
	for i := 0; i < UnhealthyAfter-1; i++ {
		r.PollOnce(context.Background())
	}
	if len(r.Candidates()) != 1 {
		t.Fatal("peer dropped before threshold")
	}
	r.PollOnce(context.Background())
	if len(r.Candidates()) != 0 {
		t.Fatal("peer still a candidate after threshold")
	}

	p.fail.Store(false)
	r.PollOnce(context.Background())
	if len(r.Candidates()) != 1 {
		t.Fatal("peer did not recover")
	}
}

func TestDiscoveredPeerForgottenWithoutBeacons(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := New("tok")
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }

	r.Observe(p.addr())
	r.PollOnce(context.Background())
	if len(r.Candidates()) != 1 {
		t.Fatal("discovered peer not added")
	}
	now = now.Add(ForgetAfter + time.Second)
	r.PollOnce(context.Background())
	if len(r.Status()) != 1 {
		t.Fatal("stale discovered peer not forgotten")
	}
}

func TestStaticPeerNeverForgotten(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := New("tok")
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	r.AddStatic(p.addr())
	now = now.Add(time.Hour)
	r.PollOnce(context.Background())
	if len(r.Candidates()) != 1 {
		t.Fatal("static peer forgotten")
	}
}

func TestCandidatesDedupeAndSkipSelf(t *testing.T) {
	a := newFakePeer(t, "tok", peerInfo("desk"))
	b := newFakePeer(t, "tok", peerInfo("desk")) // same node via second address
	me := newFakePeer(t, "tok", peerInfo("lap")) // --peer pointing at ourselves
	r := New("tok")
	r.SetSelf(node.Info{ID: "lap", BackendUp: true})
	for _, p := range []*fakePeer{a, b, me} {
		r.AddStatic(p.addr())
	}
	r.PollOnce(context.Background())
	got := r.Candidates()
	if len(got) != 2 || got[0].Info.ID != "lap" || got[1].Info.ID != "desk" {
		t.Fatalf("got %+v", got)
	}
}

func TestBackendDownExcluded(t *testing.T) {
	r := New("tok")
	r.SetSelf(node.Info{ID: "lap", BackendUp: false})
	if len(r.Candidates()) != 0 {
		t.Fatal("node with backend down must not be a candidate")
	}
}

func TestBeginTracksPending(t *testing.T) {
	r := New("tok")
	r.SetSelf(node.Info{ID: "lap", BackendUp: true})
	done1, done2 := r.Begin("lap"), r.Begin("lap")
	if got := r.Candidates()[0].Pending; got != 2 {
		t.Fatalf("pending = %d", got)
	}
	done1()
	done1() // idempotent
	if got := r.Candidates()[0].Pending; got != 1 {
		t.Fatalf("pending = %d", got)
	}
	done2()
	if got := r.Candidates()[0].Pending; got != 0 {
		t.Fatalf("pending = %d", got)
	}
}
