// Package gateway is the HTTP face of a Muster agent. It speaks the OpenAI
// and Ollama APIs to clients, routes each request to the best node, and
// serves requests forwarded by other agents.
package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/maauri/muster/internal/config"
	"github.com/maauri/muster/internal/node"
	"github.com/maauri/muster/internal/peerauth"
	"github.com/maauri/muster/internal/registry"
	"github.com/maauri/muster/internal/router"
	"github.com/maauri/muster/internal/tlsid"
)

const (
	// ForwardPrefix is where agents accept requests from other agents. It
	// only ever serves locally, so routing loops are impossible.
	ForwardPrefix = "/muster/v1/forward"
	// ClusterPath returns registry.Status for `muster status`.
	ClusterPath = "/muster/v1/cluster"
	// ProbePath asks this agent to connect back to the caller, so a device
	// can learn whether others can reach it (firewalls are invisible from
	// the inside).
	ProbePath = "/muster/v1/probe"
	// NodeHeader names the node that served a response.
	NodeHeader = "X-Muster-Node"

	maxBody = 64 << 20
)

// Management endpoints act on one machine's model store, so they always run
// on the local node even though their bodies name a model.
var localOnly = map[string]bool{
	"/api/pull": true, "/api/push": true, "/api/create": true,
	"/api/copy": true, "/api/delete": true,
}

// Gateway is safe for concurrent use.
type Gateway struct {
	reg      *registry.Registry
	auth     *peerauth.Auth
	selfFP   string // fingerprint of the certificate this agent serves
	token    string
	backend  string
	client   *http.Client
	inFlight atomic.Int64
}

// New returns a gateway that routes over reg, accepts client requests
// bearing token, signs peer traffic with auth, proves responses with its
// certificate fingerprint selfFP, and serves locally via the Ollama server
// at backendURL.
func New(reg *registry.Registry, auth *peerauth.Auth, selfFP, token, backendURL string) *Gateway {
	// No overall timeout: generations legitimately run for minutes. Dead
	// peers are detected by aggressive TCP keepalives instead (~25 s), which
	// surfaces as a send error and triggers failover.
	dialer := &net.Dialer{
		Timeout:         3 * time.Second,
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 10 * time.Second, Interval: 5 * time.Second, Count: 3},
	}
	client := registry.NewPeerClient(0)
	tr := client.Transport.(*http.Transport)
	tr.DialContext = dialer.DialContext
	tr.MaxIdleConnsPerHost = 16
	// Prompts go only to the certificate the peer proved on its last poll,
	// enforced in the handshake, before any request bytes are written.
	tr.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		pin := reg.Pin(addr)
		if pin == "" {
			return nil, fmt.Errorf("peer %s has no pinned certificate", addr)
		}
		raw, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		c := tls.Client(raw, &tls.Config{
			InsecureSkipVerify: true, // verified against the pin below
			MinVersion:         tls.VersionTLS13,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if tlsid.PeerFP(&cs) != pin {
					return errors.New("peer certificate does not match its pinned identity")
				}
				return nil
			},
		})
		if err := c.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return c, nil
	}
	return &Gateway{
		reg:     reg,
		auth:    auth,
		selfFP:  selfFP,
		token:   token,
		backend: strings.TrimRight(backendURL, "/"),
		client:  client,
	}
}

// InFlight is the number of requests this node's backend is serving now.
func (g *Gateway) InFlight() int { return int(g.inFlight.Load()) }

// Handler returns the agent's HTTP handler.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+registry.NodePath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.reg.Self())
	})
	mux.HandleFunc("GET "+ClusterPath, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.reg.Status())
	})
	mux.HandleFunc("GET "+ProbePath, g.handleProbe)
	mux.HandleFunc(ForwardPrefix+"/", g.handleForward)
	mux.HandleFunc("GET /v1/models", g.handleOpenAIModels)
	mux.HandleFunc("GET /api/tags", g.handleOllamaTags)
	mux.HandleFunc("/", g.handleAPI)
	return g.authenticate(mux)
}

// authenticate admits three kinds of caller: agents with a valid peer signature
// (answered with a proof, see peerauth), clients holding the token, and
// genuine local callers. "Genuine local" means a loopback peer that also addressed us by a
// loopback host and, if it is a browser, from a loopback origin; otherwise
// any website could reach the cluster through the user's browser
// (cross-site requests, DNS rebinding).
func (g *Gateway) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(peerauth.Header) != "" {
			if r.TLS == nil {
				writeError(w, r, http.StatusBadRequest, "muster: agent traffic must use TLS")
				return
			}
			nonce, ok := g.auth.Verify(r)
			if !ok {
				writeError(w, r, http.StatusUnauthorized, "muster: invalid peer signature (different cluster token, or clock skew over 60s)")
				return
			}
			g.auth.Prove(w.Header(), nonce, g.selfFP)
			next.ServeHTTP(w, r)
			return
		}
		if g.hasToken(r) || trustedLocal(r) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, r, http.StatusUnauthorized,
			"muster: cluster token required (Authorization: Bearer <token>); run `muster token` on any node")
	})
}

func (g *Gateway) hasToken(r *http.Request) bool {
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return config.TokenEqual(t, g.token)
	}
	return false
}

func trustedLocal(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !ap.Addr().Unmap().IsLoopback() || !loopbackHost(r.Host) {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || !loopbackHost(u.Host) {
			return false
		}
	}
	return true
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

func (g *Gateway) handleAPI(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	model := ""
	if r.Method == http.MethodPost && !localOnly[r.URL.Path] {
		model = extractModel(body)
	}
	if model == "" {
		g.dispatch(w, r, r.URL.Path, body, false, []router.Candidate{{Info: g.reg.Self(), Local: true}})
		return
	}
	cands := router.Rank(model, g.reg.Candidates())
	if len(cands) == 0 {
		writeError(w, r, http.StatusNotFound, fmt.Sprintf(
			"muster: model %q is not available on any node (available: %s)", model, strings.Join(g.modelNames(), ", ")))
		return
	}
	g.dispatch(w, r, r.URL.Path, body, true, cands)
}

func (g *Gateway) handleForward(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, ForwardPrefix)
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	g.dispatch(w, r, path, body, false, []router.Candidate{{Info: g.reg.Self(), Local: true}})
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err == nil {
		return body, true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "muster: request body over 64 MiB")
	}
	// Any other error means the client went away; there is no one to answer.
	return nil, false
}

// ProbeResult is the answer to a reachability probe.
type ProbeResult struct {
	Addr      string `json:"addr"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// handleProbe dials back to the caller's own IP on ?port= and performs an
// authenticated node fetch. The target host is always the request's source
// address, never caller-supplied, so this cannot be aimed at third parties.
func (g *Gateway) handleProbe(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	ap, perr := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || port < 1 || port > 65535 || perr != nil {
		writeError(w, r, http.StatusBadRequest, "muster: probe needs ?port=1-65535")
		return
	}
	addr := netip.AddrPortFrom(ap.Addr().Unmap(), uint16(port)).String()
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	res := ProbeResult{Addr: addr, Reachable: true}
	if _, _, err := g.reg.Fetch(ctx, addr); err != nil {
		res.Reachable, res.Error = false, err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}

func extractModel(body []byte) string {
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	return v.Model
}

// dispatch tries candidates in order. It moves to the next one only if the
// current one fails before any response bytes reach the client. For routed
// requests a 404 also moves on: inventory is up to one poll stale, so the
// model may have just been removed from that node.
func (g *Gateway) dispatch(w http.ResponseWriter, r *http.Request, path string, body []byte, routed bool, cands []router.Candidate) {
	var lastErr error
	for i, c := range cands {
		last := i == len(cands)-1
		done := g.reg.Begin(c.Info.ID)
		if c.Local {
			g.inFlight.Add(1)
		}
		resp, err := g.send(r, c, path, body)
		if err == nil && !last && retryable(resp.StatusCode, routed) {
			resp.Body.Close()
			err = errors.New(resp.Status)
		}
		if err == nil {
			relay(w, resp, c.Info.Name)
			resp.Body.Close()
		}
		if c.Local {
			g.inFlight.Add(-1)
		}
		done()
		if err == nil {
			return
		}
		if r.Context().Err() != nil {
			return // client went away; nothing to fail over for
		}
		lastErr = err
		slog.Warn("node failed, trying next", "node", c.Info.Name, "addr", c.Addr, "err", err)
	}
	writeError(w, r, http.StatusBadGateway, fmt.Sprintf("muster: no node could serve the request: %v", lastErr))
}

func retryable(code int, routed bool) bool {
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable ||
		(routed && code == http.StatusNotFound)
}

// send issues the request to c. Remote peers must prove they hold the
// cluster key in their response, or the response is discarded.
func (g *Gateway) send(r *http.Request, c router.Candidate, path string, body []byte) (*http.Response, error) {
	target := g.backend + path
	if !c.Local {
		target = "https://" + c.Addr + ForwardPrefix + path
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	if c.Local && g.backend == "" {
		return nil, errors.New("no local backend")
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(req.Header, r.Header)
	req.Header.Del("Authorization")
	req.Header.Del(peerauth.Header)
	if c.Local {
		return g.client.Do(req)
	}
	nonce := g.auth.Sign(req)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	if !g.auth.CheckProof(resp.Header, nonce, tlsid.PeerFP(resp.TLS)) {
		resp.Body.Close()
		return nil, errors.New("peer failed authentication")
	}
	resp.Header.Del(peerauth.ProofHeader)
	return resp, nil
}

// relay streams resp to w, flushing after every read so SSE and NDJSON
// token streams reach the client as they are generated.
func relay(w http.ResponseWriter, resp *http.Response, nodeName string) {
	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(NodeHeader, nodeName)
	w.WriteHeader(resp.StatusCode)
	rc := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopHeaders[k] {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
}

// modelNames is every model on a healthy node, normalised and sorted.
func (g *Gateway) modelNames() []string {
	seen := map[string]bool{}
	var names []string
	for _, c := range g.reg.Candidates() {
		for _, m := range c.Info.Models {
			n := node.NormalizeModel(m.Name)
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (g *Gateway) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	data := []model{}
	for _, n := range g.modelNames() {
		data = append(data, model{ID: n, Object: "model", OwnedBy: "muster"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (g *Gateway) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
	type model struct {
		Name  string `json:"name"`
		Model string `json:"model"`
		Size  int64  `json:"size"`
	}
	sizes := map[string]int64{}
	for _, c := range g.reg.Candidates() {
		for _, m := range c.Info.Models {
			sizes[node.NormalizeModel(m.Name)] = m.Size
		}
	}
	models := []model{}
	for _, n := range g.modelNames() {
		models = append(models, model{Name: n, Model: n, Size: sizes[n]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// writeError uses the error shape each API's clients expect.
func writeError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeJSON(w, code, map[string]any{"error": map[string]string{"message": msg, "type": "muster_error"}})
		return
	}
	writeJSON(w, code, map[string]string{"error": msg})
}
