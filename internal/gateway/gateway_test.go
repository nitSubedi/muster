package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/maauri/muster/internal/node"
	"github.com/maauri/muster/internal/peerauth"
	"github.com/maauri/muster/internal/registry"
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
	gw := New(reg, auth, token, backendURL)
	srv := httptest.NewServer(gw.Handler())
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
		{"lan with forged peer signature", "192.168.1.9:5000", "192.168.1.2:7878", map[string]string{peerauth.Header: "v1 1 n sig"}, 401},
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
	other := peerauth.New(token)
	nonce := other.Sign(r)
	w := httptest.NewRecorder()
	lap.gw.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !other.CheckProof(w.Header(), nonce) {
		t.Fatalf("status=%d proof=%q", w.Code, w.Header().Get(peerauth.ProofHeader))
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

	// desk probes back to the caller (127.0.0.1 here) on lap's port.
	resp, err := http.Get(desk.srv.URL + ProbePath + "?port=" + port)
	if err != nil {
		t.Fatal(err)
	}
	var res ProbeResult
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if !res.Reachable || res.Addr != lap.addr() {
		t.Fatalf("probe = %+v", res)
	}

	lap.srv.Close()
	resp, _ = http.Get(desk.srv.URL + ProbePath + "?port=" + port)
	json.NewDecoder(resp.Body).Decode(&res)
	resp.Body.Close()
	if res.Reachable || res.Error == "" {
		t.Fatalf("probe to closed port = %+v", res)
	}
}

func TestProbeRejectsBadPortAndAnonymousLAN(t *testing.T) {
	desk := newNode(t, "desk", fakeOllama(t, "d").URL)
	resp, _ := http.Get(desk.srv.URL + ProbePath + "?port=0")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	r := httptest.NewRequest(http.MethodGet, ProbePath+"?port=7878", nil)
	r.RemoteAddr, r.Host = "192.168.1.9:5000", "192.168.1.2:7878"
	w := httptest.NewRecorder()
	desk.gw.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous LAN probe status = %d", w.Code)
	}
}
