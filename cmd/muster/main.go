// Command muster turns every machine you own into one AI endpoint.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/nitSubedi/muster/internal/bench"
	"github.com/nitSubedi/muster/internal/config"
	"github.com/nitSubedi/muster/internal/discovery"
	"github.com/nitSubedi/muster/internal/doctor"
	"github.com/nitSubedi/muster/internal/gateway"
	"github.com/nitSubedi/muster/internal/muxlisten"
	"github.com/nitSubedi/muster/internal/node"
	"github.com/nitSubedi/muster/internal/ollama"
	"github.com/nitSubedi/muster/internal/peerauth"
	"github.com/nitSubedi/muster/internal/registry"
	"github.com/nitSubedi/muster/internal/service"
	"github.com/nitSubedi/muster/internal/tlsid"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `muster — every machine you own, one AI endpoint.

Usage:
  muster agent   [flags]   run on every device
  muster status  [flags]   show the cluster from this device
  muster token   [flags]   print this cluster's token
  muster join    [flags]   save a cluster token (paste it when prompted)
  muster doctor  [flags]   find out why a device isn't working, with fixes
  muster bench   [flags]   measure devices and links; see when pooling helps
  muster service install [agent flags] | uninstall | status
                           run the agent at login (no sudo)
  muster version

Run "muster <command> -h" for flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "agent":
		err = runAgent(args)
	case "status":
		err = runStatus(args)
	case "token":
		err = runToken(args)
	case "join":
		err = runJoin(args)
	case "doctor":
		err = runDoctor(args)
	case "bench":
		err = runBench(args)
	case "service":
		err = runService(args)
	case "version", "--version", "-v":
		fmt.Println("muster", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "muster:", err)
		os.Exit(1)
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func configFlag(fs *flag.FlagSet) *string {
	def, _ := config.DefaultPath()
	return fs.String("config", def, "config file path")
}

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	cfgPath := configFlag(fs)
	listen := fs.String("listen", ":7878", "address for the API and peer traffic")
	backend := fs.String("ollama", "", "local Ollama URL (default: $OLLAMA_HOST unless it points at Muster, else 127.0.0.1:11434)")
	name := fs.String("name", defaultName(), "node name shown in status and X-Muster-Node")
	token := fs.String("token", os.Getenv("MUSTER_TOKEN"), "join an existing cluster with this token (prefer `muster join`)")
	noDiscovery := fs.Bool("no-discovery", false, "disable LAN broadcast discovery (use --peer only)")
	interval := fs.Duration("interval", 2*time.Second, "peer poll and beacon interval")
	verbose := fs.Bool("v", false, "debug logging")
	var peers multiFlag
	fs.Var(&peers, "peer", "static peer host:port, e.g. a Tailscale address (repeatable)")
	fs.Parse(args)

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	cfg, created, err := config.LoadOrCreate(*cfgPath)
	if err != nil {
		return err
	}
	if *token != "" && *token != cfg.Token {
		cfg.Token = *token
		if err := config.Save(*cfgPath, cfg); err != nil {
			return err
		}
		slog.Info("joined cluster with provided token", "config", *cfgPath)
	} else if created {
		slog.Info("created new cluster", "config", *cfgPath)
	}

	port, err := listenPort(*listen)
	if err != nil {
		return err
	}
	backendURL, err := resolveBackend(*backend, port)
	if err != nil {
		return err
	}
	cluster := config.ClusterID(cfg.Token)

	id, err := tlsid.LoadOrCreate(filepath.Dir(*cfgPath))
	if err != nil {
		return fmt.Errorf("TLS identity: %w", err)
	}
	auth := peerauth.New(cfg.Token)
	reg := registry.New(auth)
	gw := gateway.New(reg, auth, id.FP, cfg.Token, backendURL)
	for _, p := range peers {
		reg.AddStatic(p)
	}

	hw := node.Hardware()
	oc := ollama.New(backendURL)
	refreshSelf := func(ctx context.Context) node.Info {
		info := hw
		info.ID, info.Name, info.Version, info.Backend = cfg.NodeID, *name, version, "ollama"
		info.InFlight, info.Port = gw.InFlight(), port
		models, err := oc.Models(ctx)
		info.BackendUp = err == nil
		info.Models = models
		if err != nil {
			slog.Debug("ollama unavailable", "url", backendURL, "err", err)
		}
		return info
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	reg.SetSelf(refreshSelf(ctx))
	if !reg.Self().BackendUp {
		slog.Warn("Ollama is not reachable; this node will route but not serve", "url", backendURL)
	}
	go reg.Run(ctx, *interval, refreshSelf)

	if !*noDiscovery {
		l, err := discovery.Listen(fmt.Sprintf(":%d", discovery.Port))
		if err != nil {
			slog.Warn("LAN discovery disabled; use --peer", "err", err)
		} else {
			go l.Serve(ctx, cluster, cfg.NodeID, func(id string, addr netip.AddrPort) {
				reg.Observe(addr.String())
			})
			go discovery.Announce(ctx, discovery.Beacon{Cluster: cluster, ID: cfg.NodeID, Port: port}, *interval, nil)
		}
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Cancel in-flight requests (and their upstream calls) on Ctrl-C.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(muxlisten.New(ln, id.ServerConfig())) }()

	slog.Info("muster agent running",
		"node", *name, "listen", *listen, "cluster", cluster, "models", len(reg.Self().Models))
	fmt.Fprintf(os.Stderr, "\n  Point your apps at http://localhost:%d  (OpenAI: /v1, Ollama: OLLAMA_HOST=http://localhost:%d)\n", port, port)
	fmt.Fprintf(os.Stderr, "  Add another device: run `muster token` here, then `muster join` there.\n\n")

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("forced shutdown with requests still open", "err", err)
	}
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	addr := fs.String("addr", "http://127.0.0.1:7878", "local agent URL")
	asJSON := fs.Bool("json", false, "print raw JSON")
	fs.Parse(args)

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimRight(*addr, "/") + gateway.ClusterPath)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return fmt.Errorf("no agent at %s — start one with `muster agent`", *addr)
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent returned %s", resp.Status)
	}
	var rows []registry.PeerStatus
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tADDRESS\tSTATE\tHARDWARE\tBUSY\tMODELS (* = loaded)")
	for _, r := range rows {
		addr := r.Addr
		if r.Local {
			addr = "(this device)"
		}
		name := r.Info.Name
		if name == "" {
			name = "?"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			name, addr, state(r), hardware(r.Info), r.Info.InFlight, models(r.Info))
	}
	return tw.Flush()
}

func state(r registry.PeerStatus) string {
	switch {
	case r.Healthy && r.Info.BackendUp:
		return "ready"
	case r.Healthy || (r.Local && !r.Info.BackendUp):
		return "no-ollama"
	case r.Error != "":
		return "down: " + doctor.Diagnose(r.Addr, r.Error)
	default:
		return "connecting"
	}
}

func hardware(i node.Info) string {
	if i.OS == "" {
		return "-"
	}
	s := fmt.Sprintf("%s/%s %dcpu %s", i.OS, i.Arch, i.CPUs, gib(i.MemoryBytes))
	for _, g := range i.GPUs {
		s += fmt.Sprintf(" +%s %s", strings.TrimPrefix(g.Name, "NVIDIA "), gib(g.MemoryBytes))
	}
	return s
}

func models(i node.Info) string {
	if len(i.Models) == 0 {
		return "-"
	}
	names := make([]string, len(i.Models))
	for j, m := range i.Models {
		names[j] = m.Name
		if m.Loaded {
			names[j] += "*"
		}
	}
	return strings.Join(names, ", ")
}

func gib(b uint64) string { return strconv.FormatUint((b+(1<<29))>>30, 10) + "GB" }

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func runToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ExitOnError)
	cfgPath := configFlag(fs)
	fs.Parse(args)
	cfg, _, err := config.LoadOrCreate(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Cluster token (keep it private). On the new device run `muster join` and paste it:")
	fmt.Println(cfg.Token)
	return nil
}

// runJoin reads the token from stdin rather than argv, so it doesn't land
// in shell history or the process list.
func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	cfgPath := configFlag(fs)
	fs.Parse(args)
	cfg, _, err := config.LoadOrCreate(*cfgPath)
	if err != nil {
		return err
	}
	fmt.Fprint(os.Stderr, "Paste the cluster token from `muster token` on another device: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("no token entered")
	}
	tok := strings.TrimSpace(line)
	if len(tok) < 20 || strings.ContainsAny(tok, " \t") {
		return fmt.Errorf("that doesn't look like a muster token")
	}
	cfg.Token = tok
	if err := config.Save(*cfgPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Joined cluster %s. Start (or restart) `muster agent`.\n", config.ClusterID(tok))
	return nil
}

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	cfgPath := configFlag(fs)
	addr := fs.String("addr", "http://127.0.0.1:7878", "local agent URL")
	noLLM := fs.Bool("no-llm", false, "skip LLM measurements (memory and network only)")
	asJSON := fs.Bool("json", false, "print raw JSON")
	pings := fs.Int("pings", 30, "round trips per link")
	mb := fs.Int("mb", 32, "MB to transfer per link for throughput")
	fs.Parse(args)

	cfg, _, err := config.LoadOrCreate(*cfgPath)
	if err != nil {
		return err
	}
	var rows []registry.PeerStatus
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(strings.TrimRight(*addr, "/") + gateway.ClusterPath)
	if err != nil {
		return fmt.Errorf("no agent at %s: start one with `muster agent`", *addr)
	}
	json.NewDecoder(resp.Body).Decode(&rows)
	resp.Body.Close()
	if len(rows) == 0 {
		return fmt.Errorf("agent at %s returned no status", *addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	run := bench.Runner{
		Client: registry.NewPeerClient(15 * time.Minute), Auth: peerauth.New(cfg.Token), Token: cfg.Token,
		Pings: *pings, DownloadBytes: min(*mb<<20, bench.MaxDownload),
	}
	say := func(f string, a ...any) {
		if !*asJSON {
			fmt.Fprintf(os.Stderr, f+"\n", a...)
		}
	}
	var report bench.Report
	self := rows[0].Info.Name

	say("Benchmarking %s (this device)…", self)
	d, err := run.Local(ctx, strings.TrimRight(*addr, "/"), !*noLLM)
	if err != nil {
		return fmt.Errorf("local benchmark: %w", err)
	}
	report.Devices = append(report.Devices, d)

	for _, p := range rows[1:] {
		if !p.Healthy {
			say("Skipping %s: %s", p.Addr, doctor.Diagnose(p.Addr, p.Error))
			continue
		}
		peer := bench.Peer{Name: p.Info.Name, Addr: p.Addr}
		say("Measuring the link to %s…", peer.Name)
		rtt, err := run.RTT(ctx, peer)
		if err != nil {
			say("  round trips failed: %v", err)
			continue
		}
		through, err := run.Throughput(ctx, peer)
		if err != nil {
			say("  throughput failed: %v", err)
		}
		report.Links = append(report.Links, bench.Link{From: self, To: peer.Name, RTT: rtt, ThroughMB: through})
		say("Benchmarking %s…", peer.Name)
		d, err := run.Remote(ctx, peer, !*noLLM)
		if err != nil {
			say("  device benchmark failed: %v", err)
			continue
		}
		report.Devices = append(report.Devices, d)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	fmt.Fprintln(os.Stderr)
	report.Render(os.Stdout)
	return nil
}

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	cfgPath := configFlag(fs)
	addr := fs.String("addr", "http://127.0.0.1:7878", "local agent URL")
	backend := fs.String("ollama", "", "local Ollama URL (default: $OLLAMA_HOST unless it points at Muster, else 127.0.0.1:11434)")
	fs.Parse(args)
	agentPort := 7878
	if u, err := url.Parse(*addr); err == nil {
		if p, err := strconv.Atoi(u.Port()); err == nil {
			agentPort = p
		}
	}
	backendURL, err := resolveBackend(*backend, agentPort)
	if err != nil {
		return err
	}

	var auth *peerauth.Auth
	if b, err := os.ReadFile(*cfgPath); err == nil {
		var cfg config.Config
		if json.Unmarshal(b, &cfg) == nil && cfg.Token != "" {
			auth = peerauth.New(cfg.Token)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results := doctor.Run(ctx, doctor.DefaultEnv(*addr, backendURL, *cfgPath, auth))

	icons := map[doctor.Status]string{doctor.OK: "✔", doctor.Warn: "⚠", doctor.Fail: "✘", doctor.Skip: "–"}
	failed := 0
	for _, r := range results {
		fmt.Printf("%s %-10s %s\n", icons[r.Status], r.Check, r.Detail)
		if r.Fix != "" && (r.Status == doctor.Warn || r.Status == doctor.Fail) {
			fmt.Printf("  %-10s fix: %s\n", "", r.Fix)
		}
		if r.Status == doctor.Fail {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d problem(s) found", failed)
	}
	fmt.Println("\nAll good.")
	return nil
}

func runService(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: muster service install [agent flags] | uninstall | status")
	}
	m, err := service.NewManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		exe, err := serviceExe()
		if err != nil {
			return err
		}
		agentArgs := args[1:]
		// Services don't inherit the shell environment, so capture it now.
		if v := os.Getenv("OLLAMA_HOST"); v != "" && !hasFlag(agentArgs, "ollama") && !pointsAtMuster(normalizeBackend(v), 7878) {
			agentArgs = append(agentArgs, "--ollama", normalizeBackend(v))
		}
		if err := m.Install(service.Spec{Exe: exe, Args: agentArgs}); err != nil {
			return err
		}
		path, _ := m.Path()
		fmt.Fprintf(os.Stderr, "Installed %s\nMuster now starts at login. Logs: %s\n", path, m.LogHint())
		if m.OS == "linux" {
			fmt.Fprintf(os.Stderr, "To keep it running while you're logged out (servers): loginctl enable-linger %s\n", os.Getenv("USER"))
		}
		return nil
	case "uninstall":
		if err := m.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "Removed. Muster will no longer start at login.")
		return nil
	case "status":
		out, err := m.Status()
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	return fmt.Errorf("unknown service command %q", args[0])
}

// serviceExe is the path the service should run. It must survive upgrades:
// a Homebrew Cellar path is versioned and deleted by `brew cleanup`, so we
// use the stable <prefix>/bin symlink instead.
func serviceExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	tmp, _ := filepath.EvalSymlinks(os.TempDir())
	if strings.HasPrefix(resolved, tmp) || strings.HasPrefix(exe, os.TempDir()) || strings.Contains(resolved, "/go-build") {
		return "", fmt.Errorf("%s is a temporary binary; install muster somewhere permanent first", exe)
	}
	return stablePath(exe, resolved), nil
}

func stablePath(exe, resolved string) string {
	for _, p := range []string{exe, resolved} {
		if prefix, _, ok := strings.Cut(p, "/Cellar/"); ok {
			return prefix + "/bin/muster"
		}
	}
	return exe
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		a = strings.TrimLeft(a, "-")
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func listenPort(listen string) (int, error) {
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("--listen %q: %w", listen, err)
	}
	return strconv.Atoi(p)
}

const defaultOllama = "http://127.0.0.1:11434"

// resolveBackend picks the Ollama URL. Users are told to set
// OLLAMA_HOST=http://localhost:7878 so their apps use Muster; the agent must
// not then treat itself as its own backend.
func resolveBackend(flagValue string, musterPort int) (string, error) {
	if flagValue != "" {
		u := normalizeBackend(flagValue)
		if pointsAtMuster(u, musterPort) {
			return "", fmt.Errorf("--ollama %s points at Muster itself; use your Ollama address, usually %s", flagValue, defaultOllama)
		}
		return u, nil
	}
	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		u := normalizeBackend(v)
		if !pointsAtMuster(u, musterPort) {
			return u, nil
		}
		slog.Debug("ignoring OLLAMA_HOST because it points at Muster", "OLLAMA_HOST", v)
	}
	return defaultOllama, nil
}

// pointsAtMuster reports whether u is a loopback address on Muster's port.
func pointsAtMuster(u string, musterPort int) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Port() != strconv.Itoa(musterPort) {
		return false
	}
	h := parsed.Hostname()
	if h == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && (a.IsLoopback() || a.IsUnspecified())
}

// normalizeBackend accepts OLLAMA_HOST forms like "0.0.0.0:11434" or
// "localhost" and returns a URL. A wildcard bind address is rewritten to
// loopback, since that is where we should connect.
func normalizeBackend(s string) string {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return strings.TrimRight(s, "/")
	}
	host, port := u.Hostname(), u.Port()
	switch host {
	case "0.0.0.0", "":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	if port == "" {
		port = "11434"
	}
	u.Host = net.JoinHostPort(host, port)
	return strings.TrimRight(u.String(), "/")
}

func defaultName() string {
	h, err := os.Hostname()
	if err != nil {
		return "node"
	}
	return strings.TrimSuffix(h, ".local")
}
