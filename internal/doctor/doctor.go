// Package doctor diagnoses why a device isn't working in its cluster and
// prints the exact fix. Most field failures are invisible from the device
// itself (a host firewall dropping inbound traffic), so the key checks ask
// other cluster members to connect back.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/maauri/muster/internal/discovery"
	"github.com/maauri/muster/internal/gateway"
	"github.com/maauri/muster/internal/peerauth"
	"github.com/maauri/muster/internal/registry"
	"github.com/maauri/muster/internal/tlsid"
)

// Status of one check.
type Status int

const (
	OK Status = iota
	Warn
	Fail
	Skip
)

// Result of one check. Fix is a command or instruction the user can act on.
type Result struct {
	Check  string
	Status Status
	Detail string
	Fix    string
}

// Env is everything the checks touch, injectable for tests.
type Env struct {
	AgentURL   string
	OllamaURL  string
	ConfigPath string
	Auth       *peerauth.Auth
	Client     *http.Client
	GOOS       string
	// LocalAddrs returns this device's non-loopback addresses.
	LocalAddrs func() []netip.Addr
	// Firewall returns "firewalld", "ufw" or "" (none detected / unknown).
	Firewall func() string
}

// DefaultEnv returns an Env for this machine.
func DefaultEnv(agentURL, ollamaURL, configPath string, auth *peerauth.Auth) Env {
	return Env{
		AgentURL:   strings.TrimRight(agentURL, "/"),
		OllamaURL:  strings.TrimRight(ollamaURL, "/"),
		ConfigPath: configPath,
		Auth:       auth,
		Client:     registry.NewPeerClient(4 * time.Second),
		GOOS:       goos(),
		LocalAddrs: localAddrs,
		Firewall:   detectFirewall,
	}
}

// Run executes every check in order.
func Run(ctx context.Context, env Env) []Result {
	out := []Result{checkConfig(env), checkOllama(ctx, env), checkExposure(env)}
	rows, agent := checkAgent(ctx, env)
	out = append(out, agent)
	if agent.Status != OK {
		return append(out,
			Result{Check: "Peers", Status: Skip, Detail: "agent not running"},
			Result{Check: "Inbound", Status: Skip, Detail: "agent not running"},
			Result{Check: "Discovery", Status: Skip, Detail: "agent not running"})
	}
	return append(out, checkPeers(rows), checkInbound(ctx, env, rows), checkDiscovery(env, rows))
}

func checkConfig(env Env) Result {
	r := Result{Check: "Config"}
	st, err := os.Stat(env.ConfigPath)
	switch {
	case err != nil:
		r.Status, r.Detail, r.Fix = Fail, "no config at "+env.ConfigPath, "run `muster agent` once to create a cluster, or `muster join` to join one"
	case env.GOOS != "windows" && st.Mode().Perm()&0o077 != 0:
		r.Status, r.Detail, r.Fix = Fail, fmt.Sprintf("%s is readable by other users (%v)", env.ConfigPath, st.Mode().Perm()), "chmod 600 "+shellQuote(env.ConfigPath)
	default:
		r.Detail = env.ConfigPath
	}
	return r
}

func checkOllama(ctx context.Context, env Env) Result {
	r := Result{Check: "Ollama"}
	var v struct{ Version string }
	if err := getJSON(ctx, env.Client, env.OllamaURL+"/api/version", nil, &v); err != nil {
		r.Status, r.Detail = Warn, "not reachable at "+env.OllamaURL+": this device will route requests but not serve them"
		r.Fix = "install Ollama from https://ollama.com and start it (or pass --ollama URL)"
		return r
	}
	r.Detail = fmt.Sprintf("%s at %s", v.Version, env.OllamaURL)
	return r
}

// checkExposure catches the most common unsafe multi-machine setup:
// OLLAMA_HOST=0.0.0.0, which gives everyone on the network unauthenticated
// access to Ollama.
func checkExposure(env Env) Result {
	r := Result{Check: "Exposure", Detail: "Ollama is only reachable through Muster"}
	u, err := url.Parse(env.OllamaURL)
	if err != nil || u.Port() == "" {
		r.Status, r.Detail = Skip, "cannot determine Ollama port"
		return r
	}
	for _, a := range env.LocalAddrs() {
		addr := net.JoinHostPort(a.String(), u.Port())
		c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			continue
		}
		c.Close()
		r.Status = Fail
		r.Detail = "Ollama accepts unauthenticated connections from the network on " + addr
		r.Fix = "bind Ollama to localhost (remove OLLAMA_HOST=0.0.0.0, then restart Ollama); Muster already gives your other devices authenticated access"
		return r
	}
	return r
}

func checkAgent(ctx context.Context, env Env) ([]registry.PeerStatus, Result) {
	r := Result{Check: "Agent"}
	var rows []registry.PeerStatus
	if err := getJSON(ctx, env.Client, env.AgentURL+gateway.ClusterPath, nil, &rows); err != nil || len(rows) == 0 {
		r.Status, r.Detail, r.Fix = Fail, "no agent at "+env.AgentURL, "start it with `muster agent` (or `muster service install` to run at login)"
		return nil, r
	}
	self := rows[0].Info
	r.Detail = fmt.Sprintf("%s on port %d", self.Name, self.Port)
	return rows, r
}

func checkPeers(rows []registry.PeerStatus) Result {
	r := Result{Check: "Peers"}
	peers := rows[1:]
	if len(peers) == 0 {
		r.Status, r.Detail = Warn, "no other devices found"
		r.Fix = "run `muster token` here and `muster join` on another device; across subnets or Tailscale add --peer host:7878"
		return r
	}
	var ok, bad []string
	for _, p := range peers {
		if p.Healthy {
			ok = append(ok, peerName(p))
		} else {
			bad = append(bad, fmt.Sprintf("%s: %s", peerName(p), Diagnose(p.Addr, p.Error)))
		}
	}
	if len(bad) > 0 {
		r.Status = Fail
		r.Detail = strings.Join(bad, "; ")
		r.Fix = "check those devices with `muster doctor` there"
		return r
	}
	r.Detail = "reachable: " + strings.Join(ok, ", ")
	return r
}

// checkInbound asks every healthy peer to connect back to this device.
func checkInbound(ctx context.Context, env Env, rows []registry.PeerStatus) Result {
	r := Result{Check: "Inbound"}
	self := rows[0].Info
	if env.Auth == nil || self.Port == 0 {
		r.Status, r.Detail = Skip, "no cluster key or agent port"
		return r
	}
	var ok, blocked []string
	asked := 0
	for _, p := range rows[1:] {
		if !p.Healthy {
			continue
		}
		asked++
		var res gateway.ProbeResult
		target := "https://" + p.Addr + gateway.ProbePath + "?port=" + strconv.Itoa(self.Port)
		if err := getJSON(ctx, env.Client, target, env.Auth, &res); err != nil {
			blocked = append(blocked, peerName(p)+" (probe failed: "+Diagnose(p.Addr, err.Error())+")")
		} else if res.Reachable {
			ok = append(ok, peerName(p))
		} else {
			blocked = append(blocked, peerName(p))
		}
	}
	switch {
	case asked == 0:
		r.Status, r.Detail = Skip, "no healthy peer to test from"
	case len(blocked) > 0:
		r.Status = Fail
		r.Detail = fmt.Sprintf("%s cannot connect to this device on tcp %d", strings.Join(blocked, ", "), self.Port)
		r.Fix = FirewallFix(env.GOOS, env.Firewall(), self.Port)
	default:
		r.Detail = "this device is reachable from " + strings.Join(ok, ", ")
	}
	return r
}

// checkDiscovery flags peers that are healthy but whose broadcasts never
// arrive: UDP is blocked here, or the peer is on another subnet.
func checkDiscovery(env Env, rows []registry.PeerStatus) Result {
	r := Result{Check: "Discovery"}
	var silent []string
	healthy := 0
	for _, p := range rows[1:] {
		if !p.Healthy {
			continue
		}
		healthy++
		if !p.Beacons {
			silent = append(silent, peerName(p))
		}
	}
	switch {
	case healthy == 0:
		r.Status, r.Detail = Skip, "no healthy peer"
	case len(silent) > 0:
		r.Status = Warn
		r.Detail = fmt.Sprintf("no discovery broadcasts received from %s (udp %d blocked on this device, or a different subnet)", strings.Join(silent, ", "), discovery.Port)
		r.Fix = FirewallFix(env.GOOS, env.Firewall(), rows[0].Info.Port) + "; on a different subnet or Tailscale, keep using --peer"
	default:
		r.Detail = "broadcasts received from every peer"
	}
	return r
}

// FirewallFix returns the command that opens Muster's ports on this OS.
func FirewallFix(goos, firewall string, port int) string {
	p, u := strconv.Itoa(port), strconv.Itoa(discovery.Port)
	switch {
	case firewall == "firewalld":
		return fmt.Sprintf("sudo firewall-cmd --permanent --add-port=%s/tcp --add-port=%s/udp && sudo firewall-cmd --reload", p, u)
	case firewall == "ufw":
		return fmt.Sprintf("sudo ufw allow %s/tcp && sudo ufw allow %s/udp", p, u)
	case goos == "darwin":
		return "System Settings → Network → Firewall → Options: allow incoming connections for muster"
	case goos == "windows":
		return fmt.Sprintf(`netsh advfirewall firewall add rule name=muster dir=in action=allow protocol=TCP localport=%s; netsh advfirewall firewall add rule name=muster-discovery dir=in action=allow protocol=UDP localport=%s`, p, u)
	}
	return fmt.Sprintf("allow inbound tcp %s and udp %s in this device's firewall", p, u)
}

// Diagnose turns an error talking to the peer at addr into the likely
// cause and fix.
func Diagnose(addr, err string) string {
	port := "7878"
	if _, p, splitErr := net.SplitHostPort(addr); splitErr == nil {
		port = p
	}
	switch {
	case strings.Contains(err, "connection refused"), strings.Contains(err, "no route to host"):
		return fmt.Sprintf("unreachable (agent stopped, or firewall: open tcp %s + udp %d on that device)", port, discovery.Port)
	case strings.Contains(err, "i/o timeout"), strings.Contains(err, "deadline exceeded"):
		return fmt.Sprintf("timed out (asleep, or firewall dropping tcp %s)", port)
	case strings.Contains(err, "failed authentication"), strings.Contains(err, "401"):
		return "different cluster (run `muster join` there with this cluster's token)"
	}
	if len(err) > 60 {
		return err[:59] + "…"
	}
	return err
}

func peerName(p registry.PeerStatus) string {
	if p.Info.Name != "" {
		return p.Info.Name
	}
	return p.Addr
}

func getJSON(ctx context.Context, c *http.Client, target string, auth *peerauth.Auth, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	var nonce string
	if auth != nil {
		nonce = auth.Sign(req)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	if auth != nil && !auth.CheckProof(resp.Header, nonce, tlsid.PeerFP(resp.TLS)) {
		return fmt.Errorf("peer failed authentication")
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func localAddrs() []netip.Addr {
	var out []netip.Addr
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && !ip.IsLoopback() && ip.Unmap().Is4() {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

func detectFirewall() string {
	if goos() != "linux" {
		return ""
	}
	for _, fw := range []string{"firewalld", "ufw"} {
		out, _ := exec.Command("systemctl", "is-active", fw).Output()
		if strings.TrimSpace(string(out)) == "active" {
			return fw
		}
	}
	return ""
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"\\$") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
