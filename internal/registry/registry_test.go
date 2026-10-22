package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nitSubedi/muster/internal/node"
	"github.com/nitSubedi/muster/internal/peerauth"
	"github.com/nitSubedi/muster/internal/tlsid"
)

// fakePeer serves node.Info and can be told to fail.
type fakePeer struct {
	srv  *httptest.Server
	info node.Info
	fail atomic.Bool
}

func newFakePeer(t *testing.T, token string, info node.Info) *fakePeer {
	f := &fakePeer{info: info}
	auth := peerauth.New(token)
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce, ok := auth.Verify(r)
		if !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		auth.Prove(w.Header(), nonce, tlsid.Fingerprint(f.srv.Certificate().Raw))
		if f.fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(f.info)
	}))
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePeer) addr() string { return strings.TrimPrefix(f.srv.URL, "https://") }

func newReg(token string) *Registry { return New(peerauth.New(token)) }

func peerInfo(id string) node.Info {
	return node.Info{ID: id, BackendUp: true, Models: []node.Model{{Name: "llama3:latest"}}}
}

func TestStaticPeerBecomesCandidate(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := newReg("tok")
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
	r := newReg("wrong")
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
	r := newReg("tok")
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
	r := newReg("tok")
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
	r := newReg("tok")
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
	r := newReg("tok")
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
	r := newReg("tok")
	r.SetSelf(node.Info{ID: "lap", BackendUp: false})
	if len(r.Candidates()) != 0 {
		t.Fatal("node with backend down must not be a candidate")
	}
}

func TestBeginTracksPending(t *testing.T) {
	r := newReg("tok")
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

// An attacker who copies the broadcast cluster ID and beacons its own
// address must never receive the token and never become a candidate, even
// if it answers with an attractive node.Info.
func TestImpostorPeerNeverTrusted(t *testing.T) {
	var sawSecret atomic.Bool
	evil := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, vs := range r.Header {
			for _, v := range vs {
				if strings.Contains(v, "SECRET") {
					sawSecret.Store(true)
				}
			}
		}
		json.NewEncoder(w).Encode(node.Info{ID: "evil", BackendUp: true, Models: []node.Model{{Name: "llama3", Loaded: true}}})
	}))
	defer evil.Close()

	r := newReg("SECRET-TOKEN")
	r.Observe(strings.TrimPrefix(evil.URL, "https://"))
	r.PollOnce(context.Background())
	if sawSecret.Load() {
		t.Fatal("token sent to unauthenticated peer")
	}
	if len(r.Candidates()) != 0 {
		t.Fatal("impostor became a candidate")
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	var hit atomic.Bool
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Store(true) }))
	defer other.Close()
	redir := httptest.NewTLSServer(http.RedirectHandler(other.URL, http.StatusTemporaryRedirect))
	defer redir.Close()

	r := newReg("tok")
	r.AddStatic(strings.TrimPrefix(redir.URL, "https://"))
	r.PollOnce(context.Background())
	if hit.Load() {
		t.Fatal("peer redirect was followed")
	}
}

func TestDiscoveredPeersBounded(t *testing.T) {
	r := newReg("tok")
	for i := 0; i < MaxDiscovered*3; i++ {
		r.Observe(fmt.Sprintf("10.0.%d.%d:7878", i/256, i%256))
	}
	if n := len(r.Status()) - 1; n != MaxDiscovered {
		t.Fatalf("discovered peers = %d, want %d", n, MaxDiscovered)
	}
}

func TestProvenPeerNotEvictedByFlood(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := newReg("tok")
	r.Observe(p.addr())
	r.PollOnce(context.Background())
	for i := 0; i < MaxDiscovered*3; i++ {
		r.Observe(fmt.Sprintf("10.0.%d.%d:7878", i/256, i%256))
	}
	if len(r.Candidates()) != 1 {
		t.Fatal("proven peer was evicted by a beacon flood")
	}
}

func TestPinSetByProvedPoll(t *testing.T) {
	p := newFakePeer(t, "tok", peerInfo("desk"))
	r := newReg("tok")
	r.AddStatic(p.addr())
	if r.Pin(p.addr()) != "" {
		t.Fatal("pinned before proving")
	}
	r.PollOnce(context.Background())
	if got, want := r.Pin(p.addr()), tlsid.Fingerprint(p.srv.Certificate().Raw); got != want {
		t.Fatalf("pin = %q, want %q", got, want)
	}
}
