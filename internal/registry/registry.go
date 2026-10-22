// Package registry keeps the live view of every node in the cluster: the
// local node plus peers found by discovery or configured statically.
package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/maauri/muster/internal/node"
	"github.com/maauri/muster/internal/peerauth"
	"github.com/maauri/muster/internal/router"
	"github.com/maauri/muster/internal/tlsid"
)

const (
	// UnhealthyAfter consecutive poll failures marks a peer unhealthy.
	UnhealthyAfter = 3
	// ForgetAfter without a beacon removes a discovered (non-static) peer.
	ForgetAfter = 30 * time.Second
	// MaxDiscovered bounds peers learned from beacons, which anyone on the
	// LAN can send.
	MaxDiscovered = 64
	// NodePath is the agent endpoint that returns node.Info.
	NodePath = "/muster/v1/node"

	maxInfoBytes = 1 << 20
)

// NewPeerClient returns the HTTP client for agent-to-agent traffic. It never
// uses an environment proxy and never follows redirects, so requests only
// ever reach the peer they were addressed to. TLS certificates are not
// CA-verified: the caller must check the channel-bound proof (ADR-0005).
func NewPeerClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13},
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type peer struct {
	addr       string
	static     bool
	info       node.Info
	fp         string // pinned certificate fingerprint, set by a proved poll
	polled     bool
	failures   int
	lastBeacon time.Time
	lastErr    string
}

// Registry is safe for concurrent use.
type Registry struct {
	auth   *peerauth.Auth
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	self    node.Info
	peers   map[string]*peer // by addr
	pending map[string]int   // by node ID
}

// New returns an empty registry that authenticates peers with auth.
func New(auth *peerauth.Auth) *Registry {
	return &Registry{
		auth:    auth,
		client:  NewPeerClient(2 * time.Second),
		now:     time.Now,
		peers:   map[string]*peer{},
		pending: map[string]int{},
	}
}

// SetSelf replaces the local node's info.
func (r *Registry) SetSelf(info node.Info) {
	r.mu.Lock()
	r.self = info
	r.mu.Unlock()
}

// Self returns the local node's info.
func (r *Registry) Self() node.Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.self
}

// AddStatic registers a peer that is polled forever and never forgotten.
func (r *Registry) AddStatic(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.peers[addr]; ok {
		p.static = true
		return
	}
	r.peers[addr] = &peer{addr: addr, static: true}
}

// Observe records a discovery beacon from addr. Beacons are unauthenticated,
// so a new address is only polled; it becomes a candidate after proving it
// holds the cluster key. When the discovered set is full, a peer that has
// never proved itself is evicted to make room, so spoofed beacons cannot
// lock out real devices for long.
func (r *Registry) Observe(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.peers[addr]; ok {
		p.lastBeacon = r.now()
		return
	}
	if r.discovered() >= MaxDiscovered && !r.evictUnproven() {
		return
	}
	r.peers[addr] = &peer{addr: addr, lastBeacon: r.now()}
}

// discovered and evictUnproven must be called with r.mu held.
func (r *Registry) discovered() int {
	n := 0
	for _, p := range r.peers {
		if !p.static {
			n++
		}
	}
	return n
}

func (r *Registry) evictUnproven() bool {
	for addr, p := range r.peers {
		if !p.static && !p.polled {
			delete(r.peers, addr)
			return true
		}
	}
	return false
}

// PollOnce refreshes every peer concurrently and forgets stale ones.
func (r *Registry) PollOnce(ctx context.Context) {
	r.mu.Lock()
	now := r.now()
	var addrs []string
	for addr, p := range r.peers {
		if !p.static && now.Sub(p.lastBeacon) > ForgetAfter {
			delete(r.peers, addr)
			continue
		}
		addrs = append(addrs, addr)
	}
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, fp, err := r.Fetch(ctx, addr)
			r.record(addr, info, fp, err)
		}()
	}
	wg.Wait()
}

// Run polls every interval until ctx is done. refreshSelf, if non-nil, is
// called first each round to update the local node.
func (r *Registry) Run(ctx context.Context, interval time.Duration, refreshSelf func(context.Context) node.Info) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if refreshSelf != nil {
			r.SetSelf(refreshSelf(ctx))
		}
		r.PollOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Fetch retrieves node.Info from the agent at addr over TLS, requiring a
// proof bound to the certificate it presented. It returns that
// certificate's fingerprint for pinning.
func (r *Registry) Fetch(ctx context.Context, addr string) (node.Info, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+NodePath, nil)
	if err != nil {
		return node.Info{}, "", err
	}
	nonce := r.auth.Sign(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return node.Info{}, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return node.Info{}, "", fmt.Errorf("%s", resp.Status)
	}
	fp := tlsid.PeerFP(resp.TLS)
	if !r.auth.CheckProof(resp.Header, nonce, fp) {
		return node.Info{}, "", errors.New("peer failed authentication (different cluster token?)")
	}
	var info node.Info
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxInfoBytes)).Decode(&info); err != nil {
		return node.Info{}, "", err
	}
	if info.ID == "" {
		return node.Info{}, "", fmt.Errorf("peer returned no node id")
	}
	return info, fp, nil
}

// Pin returns the certificate fingerprint proved by the peer at addr, or ""
// if it has never proved itself. Forwarded prompts are only sent over TLS
// connections presenting this certificate.
func (r *Registry) Pin(addr string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.peers[addr]; ok && p.polled {
		return p.fp
	}
	return ""
}

func (r *Registry) record(addr string, info node.Info, fp string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[addr]
	if !ok {
		return // forgotten while polling
	}
	if err != nil {
		p.failures++
		p.lastErr = err.Error()
		return
	}
	p.info, p.fp, p.polled, p.failures, p.lastErr = info, fp, true, 0, ""
}

func (p *peer) healthy() bool { return p.polled && p.failures < UnhealthyAfter }

// Candidates returns the local node and every healthy peer, deduplicated by
// node ID (a peer may be reachable both by beacon and by --peer), with
// this gateway's pending dispatch counts filled in.
func (r *Registry) Candidates() []router.Candidate {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []router.Candidate
	seen := map[string]bool{}
	if r.self.ID != "" && r.self.BackendUp {
		out = append(out, router.Candidate{Info: r.self, Local: true, Pending: r.pending[r.self.ID]})
		seen[r.self.ID] = true
	}
	for _, p := range r.sortedPeers() {
		if !p.healthy() || !p.info.BackendUp || seen[p.info.ID] || p.info.ID == r.self.ID {
			continue
		}
		seen[p.info.ID] = true
		out = append(out, router.Candidate{Info: p.info, Addr: p.addr, Pending: r.pending[p.info.ID]})
	}
	return out
}

// Begin marks a request dispatched to nodeID; call the returned func when
// the response is complete.
func (r *Registry) Begin(nodeID string) (done func()) {
	r.mu.Lock()
	r.pending[nodeID]++
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if r.pending[nodeID]--; r.pending[nodeID] <= 0 {
				delete(r.pending, nodeID)
			}
			r.mu.Unlock()
		})
	}
}

// PeerStatus is one row of `muster status`.
type PeerStatus struct {
	Addr    string `json:"addr,omitempty"`
	Local   bool   `json:"local"`
	Static  bool   `json:"static"`
	Healthy bool   `json:"healthy"`
	Error   string `json:"error,omitempty"`
	// Beacons reports whether this device has received discovery beacons
	// from the peer. Healthy but false means UDP discovery is blocked
	// somewhere between them.
	Beacons bool      `json:"beacons"`
	Info    node.Info `json:"info"`
}

// Status returns the local node followed by every known peer.
func (r *Registry) Status() []PeerStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []PeerStatus{{Local: true, Healthy: r.self.BackendUp, Info: r.self}}
	for _, p := range r.sortedPeers() {
		out = append(out, PeerStatus{
			Addr: p.addr, Static: p.static, Healthy: p.healthy(), Error: p.lastErr, Beacons: !p.lastBeacon.IsZero(), Info: p.info,
		})
	}
	return out
}

// sortedPeers must be called with r.mu held.
func (r *Registry) sortedPeers() []*peer {
	ps := make([]*peer, 0, len(r.peers))
	for _, p := range r.peers {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].static != ps[j].static {
			return ps[i].static
		}
		return ps[i].addr < ps[j].addr
	})
	return ps
}
