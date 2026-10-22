package peerauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signed(t *testing.T, a *Auth, method, target string) (*http.Request, string) {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	return r, a.Sign(r)
}

func TestSignVerifyRoundTrip(t *testing.T) {
	a, b := New("tok"), New("tok")
	r, nonce := signed(t, a, "GET", "/muster/v1/node")
	got, ok := b.Verify(r)
	if !ok || got != nonce {
		t.Fatalf("verify failed: %q %v", got, ok)
	}
}

func TestWrongTokenRejected(t *testing.T) {
	r, _ := signed(t, New("tok"), "GET", "/muster/v1/node")
	if _, ok := New("other").Verify(r); ok {
		t.Fatal("signature from another cluster accepted")
	}
}

func TestTamperedRequestRejected(t *testing.T) {
	a := New("tok")
	r, _ := signed(t, a, "GET", "/muster/v1/node")
	r.URL.Path = "/muster/v1/forward/api/chat"
	if _, ok := a.Verify(r); ok {
		t.Fatal("signature valid for a different path")
	}
	r, _ = signed(t, a, "GET", "/x")
	r.Method = "POST"
	if _, ok := a.Verify(r); ok {
		t.Fatal("signature valid for a different method")
	}
}

func TestReplayRejected(t *testing.T) {
	a, b := New("tok"), New("tok")
	r, _ := signed(t, a, "GET", "/muster/v1/node")
	if _, ok := b.Verify(r); !ok {
		t.Fatal("first use rejected")
	}
	if _, ok := b.Verify(r); ok {
		t.Fatal("replay accepted")
	}
}

func TestStaleTimestampRejected(t *testing.T) {
	a, b := New("tok"), New("tok")
	now := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return now }
	b.now = func() time.Time { return now.Add(MaxSkew + time.Second) }
	r, _ := signed(t, a, "GET", "/x")
	if _, ok := b.Verify(r); ok {
		t.Fatal("stale signature accepted")
	}
}

func TestMalformedHeaderRejected(t *testing.T) {
	for _, h := range []string{"", "v1", "v1 abc def ghi", "v2 1 n s", "v1 notanumber n s"} {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set(Header, h)
		if _, ok := New("tok").Verify(r); ok {
			t.Errorf("accepted %q", h)
		}
	}
}

func TestProof(t *testing.T) {
	a, b := New("tok"), New("tok")
	h := http.Header{}
	b.Prove(h, "nonce-1")
	if !a.CheckProof(h, "nonce-1") {
		t.Fatal("valid proof rejected")
	}
	if a.CheckProof(h, "nonce-2") {
		t.Fatal("proof accepted for another nonce")
	}
	if New("other").CheckProof(h, "nonce-1") {
		t.Fatal("proof accepted by another cluster")
	}
	if a.CheckProof(http.Header{}, "nonce-1") {
		t.Fatal("missing proof accepted")
	}
}

func TestSeenNoncesPruned(t *testing.T) {
	a, b := New("tok"), New("tok")
	now := time.Unix(1_000_000, 0)
	a.now = func() time.Time { return now }
	b.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		r, _ := signed(t, a, "GET", "/x")
		b.Verify(r)
	}
	now = now.Add(3 * MaxSkew)
	r, _ := signed(t, a, "GET", "/x")
	b.Verify(r)
	if n := len(b.seen); n != 1 {
		t.Fatalf("seen has %d entries, want 1 after pruning", n)
	}
}
