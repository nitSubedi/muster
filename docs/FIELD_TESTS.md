# Field Tests

Real hardware, real networks. Each finding becomes a fix or a roadmap item.

## 2026-10-22 — Apple Silicon laptop + RHEL-family Linux box (x86, 4 cores, 7 GB, no GPU), home Wi-Fi/LAN

**Setup.** Laptop agent with Ollama (8 models). Linux agent with no Ollama,
joined via `muster token | ssh … muster join`.

**Result: works.** Chat run *on the Linux box* was served by the laptop's
`qwen2.5:7b` (`X-Muster-Node: laptop`). With the model warm, the first
streamed token arrived in **0.28 s** across the LAN and a 40-token
generation finished in 1.84 s.

| # | Finding | Severity | Action |
|---|---|---|---|
| F1 | firewalld (default on Fedora/RHEL/Rocky) blocks inbound UDP 7879, so the Linux node never hears beacons and needs `--peer` | High: a whole distro family fails zero-config | Docs now; `muster doctor` + `muster firewall` helper in v0.2 |
| F2 | firewalld blocks inbound TCP 7878. The laptop discovers the Linux node (outbound beacons work) but can't reach it. Status showed a raw Go dial error | High: confusing first-run experience | **Fixed:** status now says "unreachable (… firewall: open tcp 7878 + udp 7879 on that device)" |
| F3 | Discovery is one-way here. The laptop sees Linux but not vice versa, and nothing tells the user why | Medium | v0.2: when node A can reach B but B never contacts A, A's status should say "B can't reach this device". Needs pushed reachability reports |
| F5 | The Linux box changed IP mid-session (DHCP, .21 → .22). `--peer <ip>` setups would silently break | High for static-peer users | Docs: recommend hostnames (`linuxbox.local`, Tailscale MagicDNS) over IPs for `--peer`. v0.2: re-resolve hostnames on every poll, and learn a static peer's new address from its beacon by node ID |
| F4 | Joining over SSH with `muster token \| ssh host muster join` worked first try and kept the token out of argv | Positive | Put this one-liner in the README as the headless/server path |

### Re-test after opening firewalld (`firewall-cmd --add-port=7878/tcp --add-port=7879/udp`)

All passed, with **no `--peer` flags**:

| Check | Result |
|---|---|
| Laptop discovers the Linux box automatically | ✅ `linuxbox 192.168.1.21:7878 no-ollama` |
| The Linux box discovers the laptop automatically | ✅ `laptop 192.168.1.20:7878 ready`, with live loaded-model marker |
| App on the laptop → the Linux box's endpoint, no token | ✅ 401 |
| App on the laptop → the Linux box's endpoint, with token | ✅ The Linux box routed it back to the laptop: "round trip ok", `X-Muster-Node: laptop` |
| App on the Linux box → the laptop's endpoint, no token / token | ✅ 401 / 200 |

This confirms F1/F2 are firewall-only. With the ports open, zero-config
discovery works across macOS and RHEL-family Linux. Not yet tested: a
second node *serving* models (the Linux box has no Ollama), and live failover
between two serving nodes.

### Round 3: two serving nodes (Ollama 0.40.8 installed on the Linux box, CPU-only, user-local, no sudo)

| Test | Expectation | Result |
|---|---|---|
| T1 Model only on the Linux box (`qwen2.5:0.5b`), requested on the laptop | Routed to the Linux box | ✅ `X-Muster-Node: linuxbox` |
| T2 Shared model (`qwen2.5:3b`), loaded on the laptop only | Prefer the warm node | ✅ laptop |
| T3 6 parallel requests, model warm on both | Spread by load | ✅ 3 laptop / 3 linuxbox |
| T4 Linux box warm and ranked first; its agent killed just before the request | Transparent failover | ✅ "connection refused" → laptop answered in 1.9 s including a cold load; client saw no error. Status shows linuxbox "unreachable" within ~6 s |

| # | Finding | Action |
|---|---|---|
| F6 | Ollama ≥ 0.40 ships Linux builds as `.tar.zst` (1.5 GB with CUDA). RHEL-family boxes lack `zstd` by default, and CPU-only nodes need only ~61 MB of it | Installer (v0.2) should detect "no GPU" and tell users which Ollama parts they need, or link to Ollama's installer |
| F7 | Generation on a 4-core CPU node is far slower than on Apple Silicon, yet the router ranks only on load/memory, so T3 sent half the work to the slow box | v0.3: measure per-node tokens/s from served requests and weight routing by throughput. This is the next most valuable routing improvement |

### Round 4: `muster doctor` against the real firewall (2026-10-22)

The Linux box's agent on tcp 7890, which firewalld still blocked. Doctor on the Linux box:

```
✔ Peers      reachable: laptop
✘ Inbound    laptop cannot connect to this device on tcp 7890
             fix: sudo firewall-cmd --permanent --add-port=7890/tcp --add-port=7879/udp && sudo firewall-cmd --reload
```

The device couldn't see its own firewall problem (the Linux box reached the laptop fine).
Doctor found it via the peer probe and printed the exact fix with the right
port. This closes F1–F3 from the user's point of view. The run also caught a
hard-coded "7878" in the peer diagnosis on the laptop side; it now uses the
peer's real port.

### Round 5: TLS between agents, live (2026-10-22)

| Check | Result |
|---|---|
| Laptop → model only the Linux box has, over TLS forward | ✅ `X-Muster-Node: linuxbox` |
| Linux box → laptop's model, over TLS forward | ✅ `X-Muster-Node: laptop` |
| `openssl s_client` to linuxbox:7878 | ✅ TLSv1.3, TLS_AES_128_GCM_SHA256; cert on the wire = the Linux box's `node.crt` (SHA-256 match) |
| Signed peer request over plain HTTP | ✅ refused: "agent traffic must use TLS" |
