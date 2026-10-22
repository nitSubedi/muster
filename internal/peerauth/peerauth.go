// Package peerauth authenticates agent-to-agent requests without ever
// sending the cluster token over the network (ADR-0004).
//
// The caller signs each request:
//
//	X-Muster-Auth: v1 <unix-ts> <nonce> <base64 HMAC(key, "req" ts nonce method uri)>
//
// and the callee answers with proof that it also holds the key, bound to
// the TLS certificate it served the response with (ADR-0005):
//
//	X-Muster-Proof: <base64 HMAC(key, "resp" nonce cert-fingerprint)>
//
// The caller checks the proof against the fingerprint of the certificate
// it actually saw, so a man-in-the-middle's proof relay never verifies.
package peerauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Header carries the request signature.
	Header = "X-Muster-Auth"
	// ProofHeader carries the response proof.
	ProofHeader = "X-Muster-Proof"
	// MaxSkew is how far a signature's timestamp may be from our clock.
	MaxSkew = 60 * time.Second
)

// Auth signs and verifies for one cluster. Safe for concurrent use.
type Auth struct {
	key []byte
	now func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // nonce → expiry, for replay protection
}

// New derives the signing key from the cluster token.
func New(token string) *Auth {
	k := sha256.Sum256([]byte("muster-peer-auth:" + token))
	return &Auth{key: k[:], now: time.Now, seen: map[string]time.Time{}}
}

// Sign adds a signature header to r and returns its nonce, which the
// caller passes to CheckProof on the response.
func (a *Auth) Sign(r *http.Request) string {
	b := make([]byte, 16)
	rand.Read(b)
	nonce := base64.RawURLEncoding.EncodeToString(b)
	ts := strconv.FormatInt(a.now().Unix(), 10)
	r.Header.Set(Header, strings.Join([]string{"v1", ts, nonce, a.mac("req", ts, nonce, r.Method, r.URL.RequestURI())}, " "))
	return nonce
}

// Verify checks r's signature, freshness and nonce uniqueness. It returns
// the nonce for Prove.
func (a *Auth) Verify(r *http.Request) (nonce string, ok bool) {
	parts := strings.Split(r.Header.Get(Header), " ")
	if len(parts) != 4 || parts[0] != "v1" {
		return "", false
	}
	ts, nonce, sig := parts[1], parts[2], parts[3]
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return "", false
	}
	now := a.now()
	if d := now.Sub(time.Unix(sec, 0)); d > MaxSkew || d < -MaxSkew {
		return "", false
	}
	if !hmac.Equal([]byte(sig), []byte(a.mac("req", ts, nonce, r.Method, r.URL.RequestURI()))) {
		return "", false
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	for n, exp := range a.seen {
		if now.After(exp) {
			delete(a.seen, n)
		}
	}
	if _, dup := a.seen[nonce]; dup {
		return "", false
	}
	a.seen[nonce] = now.Add(2 * MaxSkew)
	return nonce, true
}

// Prove sets the response proof for nonce on h, bound to certFP, the
// fingerprint of the certificate this server presents.
func (a *Auth) Prove(h http.Header, nonce, certFP string) {
	h.Set(ProofHeader, a.mac("resp", nonce, certFP))
}

// CheckProof reports whether h carries a valid proof for nonce, made by a
// server presenting the certificate with fingerprint certFP.
func (a *Auth) CheckProof(h http.Header, nonce, certFP string) bool {
	got := h.Get(ProofHeader)
	return got != "" && certFP != "" && hmac.Equal([]byte(got), []byte(a.mac("resp", nonce, certFP)))
}

func (a *Auth) mac(parts ...string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(strings.Join(parts, "\n")))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
