package gateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nitSubedi/muster/internal/muxlisten"
	"github.com/nitSubedi/muster/internal/node"
	"github.com/nitSubedi/muster/internal/peerauth"
	"github.com/nitSubedi/muster/internal/registry"
	"github.com/nitSubedi/muster/internal/tlsid"
)

const token = "test-token"

// fakeOllama answers model requests with its own name so tests can see
// which node served them.
func fakeOllama(t *testing.T, name string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get(peerauth.Header) != "" {
			t.Errorf("%s: credentials leaked to backend", name)
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, `{"served_by":%q,"path":%q,"query":%q,"body":%s}`, name, r.URL.Path, r.URL.RawQuery, orNull(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func orNull(b []byte) string {
	if len(b) == 0 {
		return "null"
	}
	return string(b)
}

type testNode struct {
	gw  *Gateway
	srv *httptest.Server
	reg *registry.Registry
}

func newNode(t *testing.T, id string, backendURL string, models ...node.Model) *testNode {
	auth := peerauth.New(token)
	reg := registry.New(auth)
	reg.SetSelf(node.Info{ID: id, Name: id, BackendUp: backendURL != "", Models: models})
	ident, err := tlsid.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gw := New(reg, auth, ident.FP, token, backendURL)
	srv := httptest.NewUnstartedServer(gw.Handler())
	srv.Listener = muxlisten.New(srv.Listener, ident.ServerConfig())
	srv.Start()
	t.Cleanup(srv.Close)
	return &testNode{gw: gw, srv: srv, reg: reg}
}

func (n *testNode) addr() string { return strings.TrimPrefix(n.srv.URL, "http://") }

func (n *testNode) peerWith(t *testing.T, others ...*testNode) {
	for _, o := range others {
		n.reg.AddStatic(o.addr())
	}
	n.reg.PollOnce(context.Background())
}

func post(t *testing.T, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func model(name string, loaded bool) node.Model { return node.Model{Name: name, Loaded: loaded} }

func TestRoutesToPeerThatHasModel(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("qwen2.5:32b", false))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("qwen2.5:3b", false))
	lap.peerWith(t, desk)

	resp, out := post(t, lap.srv.URL+"/v1/chat/completions?x=1", `{"model":"qwen2.5:32b","messages":[]}`)
	if resp.StatusCode != 200 || out["served_by"] != "desk-ollama" || out["path"] != "/v1/chat/completions" || out["query"] != "x=1" {
		t.Fatalf("status=%d out=%v", resp.StatusCode, out)
	}
	if got := resp.Header.Get("X-Muster-Node"); got != "desk" {
		t.Fatalf("X-Muster-Node = %q", got)
	}
	if body, _ := out["body"].(map[string]any); body["model"] != "qwen2.5:32b" {
		t.Fatalf("body not forwarded intact: %v", out["body"])
	}
}

func TestRoutesLocallyWhenLocalIsBest(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("llama3", false))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("llama3", true))
	lap.peerWith(t, desk)

	_, out := post(t, lap.srv.URL+"/api/chat", `{"model":"llama3"}`)
	if out["served_by"] != "lap-ollama" {
		t.Fatalf("out=%v", out)
	}
}

func TestFailsOverWhenPeerUnreachable(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("llama3", true))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("llama3", false))
	lap.peerWith(t, desk)
	desk.srv.Close() // desk goes to sleep after the last successful poll

	resp, out := post(t, lap.srv.URL+"/api/generate", `{"model":"llama3"}`)
	if resp.StatusCode != 200 || out["served_by"] != "lap-ollama" {
		t.Fatalf("status=%d out=%v", resp.StatusCode, out)
	}
}

func TestAllCandidatesDownIs502(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	lap := newNode(t, "lap", dead.URL, model("llama3", false))
	resp, out := post(t, lap.srv.URL+"/v1/chat/completions", `{"model":"llama3"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if e, _ := out["error"].(map[string]any); e["message"] == nil {
		t.Fatalf("OpenAI-style error expected: %v", out)
	}
}

func TestUnknownModelIs404WithAvailableList(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "lap").URL, model("llama3", false))
	resp, out := post(t, lap.srv.URL+"/api/chat", `{"model":"nope"}`)
	msg, _ := out["error"].(string)
	if resp.StatusCode != 404 || !strings.Contains(msg, "llama3:latest") {
		t.Fatalf("status=%d out=%v", resp.StatusCode, out)
	}
}

func TestManagementCallsStayLocal(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("newmodel", false))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL)
	lap.reg.SetSelf(node.Info{ID: "lap", Name: "lap", BackendUp: true})
	lap.peerWith(t, desk)

	_, out := post(t, lap.srv.URL+"/api/pull", `{"model":"newmodel"}`)
	if out["served_by"] != "lap-ollama" {
		t.Fatalf("pull must run on the local node, got %v", out)
	}
}

func TestRequestsWithoutModelGoLocal(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL)
	resp, err := http.Get(lap.srv.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out["served_by"] != "lap-ollama" || out["path"] != "/api/version" {
		t.Fatalf("out=%v", out)
	}
}

func TestStreamsIncrementally(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"chunk":1}`)
		w.(http.Flusher).Flush()
		<-release // only send the rest once the client has seen chunk 1
		fmt.Fprintln(w, `{"chunk":2}`)
	}))
	defer backend.Close()
	desk := newNode(t, "desk", backend.URL, model("llama3", true))
	lap := newNode(t, "lap", "")
	lap.peerWith(t, desk)

	resp, err := http.Post(lap.srv.URL+"/api/chat", "application/json", strings.NewReader(`{"model":"llama3"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)

	got := make(chan string)
	go func() {
		lines.Scan()
		got <- lines.Text()
	}()
	select {
	case line := <-got:
		if line != `{"chunk":1}` {
			t.Fatalf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first chunk was buffered instead of streamed")
	}
	close(release)
	if !lines.Scan() || lines.Text() != `{"chunk":2}` {
		t.Fatalf("second line = %q", lines.Text())
	}
}

func TestModelListsAggregateCluster(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "d").URL, model("qwen2.5:32b", false), model("llama3:latest", false))
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("llama3", false))
	lap.peerWith(t, desk)

	resp, _ := http.Get(lap.srv.URL + "/v1/models")
	var openai struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&openai)
	resp.Body.Close()
	if len(openai.Data) != 2 || openai.Data[0].ID != "llama3:latest" || openai.Data[1].ID != "qwen2.5:32b" {
		t.Fatalf("/v1/models = %+v", openai.Data)
	}

	resp, _ = http.Get(lap.srv.URL + "/api/tags")
	var tags struct {
		Models []struct{ Name, Model string } `json:"models"`
	}
	json.NewDecoder(resp.Body).Decode(&tags)
	resp.Body.Close()
	if len(tags.Models) != 2 || tags.Models[1].Model != "qwen2.5:32b" {
		t.Fatalf("/api/tags = %+v", tags.Models)
	}
}

func TestAuth(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("llama3", false))
	h := lap.gw.Handler()

	cases := []struct {
		name   string
		remote string
		host   string
		hdr    map[string]string
		want   int
	}{
		{"loopback trusted", "127.0.0.1:5000", "localhost:7878", nil, 200},
		{"ipv6 loopback trusted", "[::1]:5000", "[::1]:7878", nil, 200},
		{"lan without token", "192.168.1.9:5000", "192.168.1.2:7878", nil, 401},
		{"lan with wrong token", "192.168.1.9:5000", "192.168.1.2:7878", map[string]string{"Authorization": "Bearer nope"}, 401},
		{"lan with bearer token", "192.168.1.9:5000", "192.168.1.2:7878", map[string]string{"Authorization": "Bearer " + token}, 200},
		{"peer signature over plain http", "192.168.1.9:5000", "192.168.1.2:7878", map[string]string{peerauth.Header: "v1 1 n sig"}, 400},
		{"dns rebinding", "127.0.0.1:5000", "evil.example:7878", nil, 401},
		{"cross-site browser", "127.0.0.1:5000", "localhost:7878", map[string]string{"Origin": "https://evil.example"}, 401},
		{"local web app", "127.0.0.1:5000", "localhost:7878", map[string]string{"Origin": "http://localhost:3000"}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			r.RemoteAddr, r.Host = c.remote, c.host
			for k, v := range c.hdr {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
		})
	}
}

func TestNodeEndpointReportsSelf(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("llama3", false))
	resp, _ := http.Get(lap.srv.URL + registry.NodePath)
	var info node.Info
	json.NewDecoder(resp.Body).Decode(&info)
	resp.Body.Close()
	if info.ID != "lap" || len(info.Models) != 1 {
		t.Fatalf("info = %+v", info)
	}
}

func TestInFlightCountsLocalServing(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer backend.Close()
	lap := newNode(t, "lap", backend.URL, model("llama3", false))

	go func() {
		resp, err := http.Post(lap.srv.URL+"/api/chat", "application/json", strings.NewReader(`{"model":"llama3"}`))
		if err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	if got := lap.gw.InFlight(); got != 1 {
		t.Fatalf("in flight = %d", got)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for lap.gw.InFlight() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := lap.gw.InFlight(); got != 0 {
		t.Fatalf("in flight after completion = %d", got)
	}
}

func TestPeerSignatureAcceptedAndProved(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("llama3", false))
	r := httptest.NewRequest(http.MethodGet, registry.NodePath, nil)
	r.RemoteAddr, r.Host = "192.168.1.9:5000", "192.168.1.2:7878"
	r.TLS = &tls.ConnectionState{}
	other := peerauth.New(token)
	nonce := other.Sign(r)
	w := httptest.NewRecorder()
	lap.gw.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !other.CheckProof(w.Header(), nonce, lap.gw.selfFP) {
		t.Fatalf("status=%d proof=%q", w.Code, w.Header().Get(peerauth.ProofHeader))
	}
}

func TestPeerRequestOverPlainHTTPRejected(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL)
	req, _ := http.NewRequest(http.MethodGet, lap.srv.URL+registry.NodePath, nil)
	peerauth.New(token).Sign(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// A man-in-the-middle terminates TLS with its own certificate and relays
// everything to the real node. The real node's proofs are bound to the real
// certificate, so the relay can never be trusted and never sees a prompt.
func TestMITMRelayNeverTrustedOrSentPrompts(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("llama3", true))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("llama3", false))

	var relayedPOST atomic.Bool
	upstream, _ := url.Parse("https://" + desk.addr())
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	proxy.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	mitmID, _ := tlsid.LoadOrCreate(t.TempDir())
	mitm := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			relayedPOST.Store(true)
		}
		proxy.ServeHTTP(w, r)
	}))
	mitm.Listener = muxlisten.New(mitm.Listener, mitmID.ServerConfig())
	mitm.Start()
	defer mitm.Close()

	lap.reg.AddStatic(strings.TrimPrefix(mitm.URL, "http://"))
	lap.reg.PollOnce(context.Background())
	if n := len(lap.reg.Candidates()); n != 1 {
		t.Fatalf("relay became a candidate (%d candidates)", n)
	}
	_, out := post(t, lap.srv.URL+"/api/chat", `{"model":"llama3","messages":[{"content":"secret plans"}]}`)
	if out["served_by"] != "lap-ollama" || relayedPOST.Load() {
		t.Fatalf("out=%v relayedPOST=%v", out, relayedPOST.Load())
	}
}

// A healthy peer address that is taken over by an impostor (e.g. the real
// device left and the attacker grabbed its IP) must not receive prompts.
func TestImpostorAtPeerAddressGetsNoPrompts(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "desk-ollama").URL, model("llama3", true))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("llama3", false))
	lap.peerWith(t, desk)

	impostorAuth := peerauth.New("attacker-guess")
	impostor := newNode(t, "evil", fakeOllama(t, "evil-ollama").URL, model("llama3", true))
	impostor.gw.auth = impostorAuth
	desk.srv.Config.Handler = impostor.gw.Handler()

	resp, out := post(t, lap.srv.URL+"/api/chat", `{"model":"llama3","messages":[{"content":"secret plans"}]}`)
	if resp.StatusCode != 200 || out["served_by"] != "lap-ollama" {
		t.Fatalf("status=%d out=%v", resp.StatusCode, out)
	}
}

func TestStaleInventory404FailsOver(t *testing.T) {
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model 'llama3' not found"}`, http.StatusNotFound)
	}))
	defer gone.Close()
	desk := newNode(t, "desk", gone.URL, model("llama3", true))
	lap := newNode(t, "lap", fakeOllama(t, "lap-ollama").URL, model("llama3", false))
	lap.peerWith(t, desk)

	_, out := post(t, lap.srv.URL+"/api/chat", `{"model":"llama3"}`)
	if out["served_by"] != "lap-ollama" {
		t.Fatalf("out=%v", out)
	}
}

func TestOversizedBodyIs413(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("llama3", false))
	big := strings.NewReader(`{"model":"llama3","x":"` + strings.Repeat("a", maxBody) + `"}`)
	resp, err := http.Post(lap.srv.URL+"/api/chat", "application/json", big)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestProbeDialsBackToCaller(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "d").URL, model("llama3", false))
	lap := newNode(t, "lap", fakeOllama(t, "l").URL)
	_, port, _ := strings.Cut(lap.addr(), ":")

	probe := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, desk.srv.URL+ProbePath+"?port="+port, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	// desk probes back to the caller (127.0.0.1 here) on lap's port.
	resp := probe()
	var res ProbeResult
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if !res.Reachable || res.Addr != lap.addr() {
		t.Fatalf("probe = %+v", res)
	}

	lap.srv.Close()
	resp = probe()
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if res.Reachable || res.Error == "" {
		t.Fatalf("probe to closed port = %+v", res)
	}
}

func TestProbeRejectsBadPortAndAnonymousLAN(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "d").URL)
	req, _ := http.NewRequest(http.MethodGet, desk.srv.URL+ProbePath+"?port=0", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// A web page can make the browser GET localhost without credentials.
	resp, _ = http.Get(desk.srv.URL + ProbePath + "?port=7878")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthenticated loopback probe status = %d, want 403", resp.StatusCode)
	}
	r := httptest.NewRequest(http.MethodGet, ProbePath+"?port=7878", nil)
	r.RemoteAddr, r.Host = "192.168.1.9:5000", "192.168.1.2:7878"
	w := httptest.NewRecorder()
	desk.gw.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous LAN probe status = %d", w.Code)
	}
}

func TestForgedPeerSignatureOverTLSRejected(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL)
	r := httptest.NewRequest(http.MethodGet, registry.NodePath, nil)
	r.RemoteAddr, r.Host, r.TLS = "192.168.1.9:5000", "192.168.1.2:7878", &tls.ConnectionState{}
	r.Header.Set(peerauth.Header, "v1 1 n sig")
	w := httptest.NewRecorder()
	lap.gw.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get(peerauth.ProofHeader) != "" {
		t.Fatalf("status=%d proof=%q", w.Code, w.Header().Get(peerauth.ProofHeader))
	}
}

func TestPeerURLEscapesIPv6Zone(t *testing.T) {
	u := registry.PeerURL("[fe80::1%en0]:7878", ProbePath, "port=7878")
	if _, err := url.Parse(u); err != nil {
		t.Fatalf("PeerURL produced unparseable %q: %v", u, err)
	}
	if !strings.Contains(u, "%25en0") {
		t.Fatalf("zone not escaped: %q", u)
	}
}

func TestErrorMessagesNotHTMLEscaped(t *testing.T) {
	lap := newNode(t, "lap", fakeOllama(t, "l").URL)
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.RemoteAddr, r.Host = "192.168.1.9:5000", "192.168.1.2:7878"
	w := httptest.NewRecorder()
	lap.gw.Handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "Bearer <token>") {
		t.Fatalf("body = %s", w.Body)
	}
}

// `ollama list` panics unless every entry has a digest of 12+ characters,
// so /api/tags must pass the backend's entries through intact.
func TestTagsCompatibleWithOllamaCLI(t *testing.T) {
	tag := json.RawMessage(`{"name":"llama3:latest","model":"llama3:latest","modified_at":"2026-01-01T00:00:00Z","size":4,"digest":"0123456789abcdef0123","details":{"family":"llama"}}`)
	desk := newNode(t, "desk", fakeOllama(t, "d").URL, node.Model{Name: "llama3:latest", Tag: tag})
	lap := newNode(t, "lap", fakeOllama(t, "l").URL, model("qwen2.5:7b", false)) // no Tag: synthesized
	lap.peerWith(t, desk)

	resp, _ := http.Get(lap.srv.URL + "/api/tags")
	var out struct {
		Models []map[string]any `json:"models"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if len(out.Models) != 2 {
		t.Fatalf("models = %v", out.Models)
	}
	for _, m := range out.Models {
		if d, _ := m["digest"].(string); len(d) < 12 {
			t.Errorf("%v: digest %q too short for ollama list", m["name"], d)
		}
		if _, ok := m["modified_at"]; !ok {
			t.Errorf("%v: missing modified_at", m["name"])
		}
	}
	if d, _ := out.Models[0]["details"].(map[string]any); d["family"] != "llama" {
		t.Errorf("backend entry not passed through: %v", out.Models[0])
	}
}

// OLLAMA_HOST=localhost:7878 is how users point apps at Muster. If the agent
// also picks it up as its backend, requests must fail fast, not loop forever.
func TestBackendPointingAtSelfIsDetected(t *testing.T) {
	lap := newNode(t, "lap", "", model("llama3", false))
	lap.gw.backend = lap.srv.URL
	lap.reg.SetSelf(node.Info{ID: "lap", Name: "lap", BackendUp: true, Models: []node.Model{{Name: "llama3"}}})

	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Post(lap.srv.URL+"/api/chat", "application/json", strings.NewReader(`{"model":"llama3"}`))
		if err == nil {
			done <- resp
		}
	}()
	select {
	case resp := <-done:
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusLoopDetected || !strings.Contains(string(b), "--ollama") {
			t.Fatalf("status=%d body=%s", resp.StatusCode, b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request looped instead of failing fast")
	}
}
