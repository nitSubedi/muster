package doctor

import (
	"context"
	"runtime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nitSubedi/muster/internal/gateway"
	"github.com/nitSubedi/muster/internal/muxlisten"
	"github.com/nitSubedi/muster/internal/node"
	"github.com/nitSubedi/muster/internal/peerauth"
	"github.com/nitSubedi/muster/internal/registry"
	"github.com/nitSubedi/muster/internal/tlsid"
)

const token = "doctor-test-token"

type agent struct {
	srv  *httptest.Server
	reg  *registry.Registry
	port int
}

func newAgent(t *testing.T, name string) *agent {
	auth := peerauth.New(token)
	reg := registry.New(auth)
	id, err := tlsid.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(gateway.New(reg, auth, id.FP, token, "").Handler())
	srv.Listener = muxlisten.New(srv.Listener, id.ServerConfig())
	srv.Start()
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	port, _ := strconv.Atoi(p)
	reg.SetSelf(node.Info{ID: name, Name: name, Port: port})
	return &agent{srv: srv, reg: reg, port: port}
}

func (a *agent) addr() string { return strings.TrimPrefix(a.srv.URL, "http://") }

func fakeOllama(t *testing.T) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"0.40.8"}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func env(t *testing.T, a *agent, ollamaURL string) Env {
	cfg := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(cfg, []byte("{}"), 0o600)
	e := DefaultEnv(a.srv.URL, ollamaURL, cfg, peerauth.New(token))
	e.LocalAddrs = func() []netip.Addr { return nil }
	e.Firewall = func() string { return "firewalld" }
	e.GOOS = runtime.GOOS
	return e
}

func byName(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.Check] = r
	}
	return m
}

func TestHealthyTwoNodeCluster(t *testing.T) {
	lap, desk := newAgent(t, "lap"), newAgent(t, "desk")
	lap.reg.AddStatic(desk.addr())
	desk.reg.AddStatic(lap.addr())
	lap.reg.PollOnce(context.Background())

	got := byName(Run(context.Background(), env(t, lap, fakeOllama(t).URL)))
	for _, c := range []string{"Config", "Ollama", "Exposure", "Agent", "Peers", "Inbound"} {
		if got[c].Status != OK {
			t.Errorf("%s = %+v, want OK", c, got[c])
		}
	}
	if !strings.Contains(got["Inbound"].Detail, "reachable from desk") {
		t.Errorf("Inbound detail = %q", got["Inbound"].Detail)
	}
	// Static peer, no beacons: the field-test F1 situation.
	if d := got["Discovery"]; d.Status != Warn || !strings.Contains(d.Fix, "firewall-cmd") {
		t.Errorf("Discovery = %+v", d)
	}
}

// Field test F2: a peer can't connect back because this device's firewall
// drops inbound traffic. Simulated by advertising a port nothing listens on.
func TestInboundBlockedGivesFirewallFix(t *testing.T) {
	lap, desk := newAgent(t, "lap"), newAgent(t, "desk")
	lap.reg.AddStatic(desk.addr())
	lap.reg.PollOnce(context.Background())
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	lap.reg.SetSelf(node.Info{ID: "lap", Name: "lap", Port: closedPort})

	got := byName(Run(context.Background(), env(t, lap, fakeOllama(t).URL)))
	in := got["Inbound"]
	if in.Status != Fail || !strings.Contains(in.Detail, "desk cannot connect") {
		t.Fatalf("Inbound = %+v", in)
	}
	want := "sudo firewall-cmd --permanent --add-port=" + strconv.Itoa(closedPort) + "/tcp"
	if !strings.Contains(in.Fix, want) {
		t.Fatalf("Fix = %q, want %q", in.Fix, want)
	}
}

func TestAgentDownSkipsClusterChecks(t *testing.T) {
	lap := newAgent(t, "lap")
	e := env(t, lap, fakeOllama(t).URL)
	lap.srv.Close()
	got := byName(Run(context.Background(), e))
	if got["Agent"].Status != Fail || got["Peers"].Status != Skip || got["Inbound"].Status != Skip {
		t.Fatalf("got %+v", got)
	}
}

func TestNoPeersWarns(t *testing.T) {
	lap := newAgent(t, "lap")
	got := byName(Run(context.Background(), env(t, lap, fakeOllama(t).URL)))
	if p := got["Peers"]; p.Status != Warn || !strings.Contains(p.Fix, "muster join") {
		t.Fatalf("Peers = %+v", p)
	}
}

func TestUnhealthyPeerDiagnosed(t *testing.T) {
	lap, desk := newAgent(t, "lap"), newAgent(t, "desk")
	lap.reg.AddStatic(desk.addr())
	desk.srv.Close()
	for i := 0; i < registry.UnhealthyAfter; i++ {
		lap.reg.PollOnce(context.Background())
	}
	got := byName(Run(context.Background(), env(t, lap, fakeOllama(t).URL)))
	if p := got["Peers"]; p.Status != Fail || !strings.Contains(p.Detail, "unreachable") {
		t.Fatalf("Peers = %+v", p)
	}
}

func TestOllamaDownWarns(t *testing.T) {
	lap := newAgent(t, "lap")
	dead := fakeOllama(t)
	dead.Close()
	if o := byName(Run(context.Background(), env(t, lap, dead.URL)))["Ollama"]; o.Status != Warn {
		t.Fatalf("Ollama = %+v", o)
	}
}

func TestExposedOllamaFails(t *testing.T) {
	lap := newAgent(t, "lap")
	ollama := fakeOllama(t)
	e := env(t, lap, ollama.URL)
	// Treat loopback as "a network address" so the test listener counts as exposed.
	e.LocalAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("127.0.0.1")} }
	if x := byName(Run(context.Background(), e))["Exposure"]; x.Status != Fail || !strings.Contains(x.Fix, "OLLAMA_HOST") {
		t.Fatalf("Exposure = %+v", x)
	}
}

func TestConfigPermissions(t *testing.T) {
	lap := newAgent(t, "lap")
	e := env(t, lap, fakeOllama(t).URL)
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permission bits; the token file inherits the profile ACL")
	}
	os.Chmod(e.ConfigPath, 0o644)
	if c := checkConfig(e); c.Status != Fail || !strings.Contains(c.Fix, "chmod 600") {
		t.Fatalf("Config = %+v", c)
	}
	e.ConfigPath = filepath.Join(t.TempDir(), "missing.json")
	if c := checkConfig(e); c.Status != Fail || !strings.Contains(c.Fix, "muster join") {
		t.Fatalf("Config = %+v", c)
	}
}

func TestFirewallFix(t *testing.T) {
	cases := []struct{ goos, fw, want string }{
		{"linux", "firewalld", "firewall-cmd --permanent --add-port=7878/tcp --add-port=7879/udp"},
		{"linux", "ufw", "ufw allow 7878/tcp && sudo ufw allow 7879/udp"},
		{"darwin", "", "System Settings"},
		{"windows", "", "netsh advfirewall"},
		{"linux", "", "allow inbound tcp 7878 and udp 7879"},
	}
	for _, c := range cases {
		if got := FirewallFix(c.goos, c.fw, 7878); !strings.Contains(got, c.want) {
			t.Errorf("FirewallFix(%s,%s) = %q", c.goos, c.fw, got)
		}
	}
}

func TestDiagnose(t *testing.T) {
	cases := []struct{ addr, err, want string }{
		{"192.168.1.98:7890", `dial tcp 192.168.1.98:7890: connect: connection refused`, "open tcp 7890 + udp 7879"},
		{"10.0.0.2:7878", `dial tcp 10.0.0.2:7878: connect: no route to host`, "open tcp 7878"},
		{"10.0.0.2:7878", `dial tcp 10.0.0.2:7878: connectex: No connection could be made because the target machine actively refused it.`, "open tcp 7878"},
		{"10.0.0.2:7878", `dial tcp 10.0.0.2:7878: connectex: A socket operation was attempted to an unreachable host.`, "open tcp 7878"},
		{"x:7878", `dial tcp x: i/o timeout`, "asleep"},
		{"x:7878", `peer failed authentication (different cluster token?)`, "muster join"},
	}
	for _, c := range cases {
		if got := Diagnose(c.addr, c.err); !strings.Contains(got, c.want) {
			t.Errorf("Diagnose(%q, %q) = %q, want %q", c.addr, c.err, got, c.want)
		}
	}
}
