# Field Tests

Real hardware, real networks. Each finding becomes a fix or a roadmap item.

## 2026-10-22 — MacBook Pro (M3 Pro, 18 GB) + Rocky Linux 9.6 box (x86, 4 cores, 7 GB, no GPU), home Wi-Fi/LAN

**Setup.** Mac agent with Ollama (8 models). Linux agent with no Ollama,
joined via `muster token | ssh … muster join`.

**Result: works.** Chat run *on the Linux box* was served by the Mac's
`qwen2.5:7b` (`X-Muster-Node: macbook`). With the model warm, the first
streamed token arrived in **0.28 s** across the LAN and a 40-token
generation finished in 1.84 s.

| # | Finding | Severity | Action |
|---|---|---|---|
| F1 | firewalld (default on Fedora/RHEL/Rocky) blocks inbound UDP 7879, so the Linux node never hears beacons and needs `--peer` | High: a whole distro family fails zero-config | Docs now; `muster doctor` + `muster firewall` helper in v0.2 |
| F2 | firewalld blocks inbound TCP 7878. The Mac discovers the Linux node (outbound beacons work) but can't reach it. Status showed a raw Go dial error | High: confusing first-run experience | **Fixed:** status now says "unreachable (… firewall: open tcp 7878 + udp 7879 on that device)" |
| F3 | Discovery is one-way here. The Mac sees Linux but not vice versa, and nothing tells the user why | Medium | v0.2: when node A can reach B but B never contacts A, A's status should say "B can't reach this device". Needs pushed reachability reports |
| F5 | The Linux box changed IP mid-session (DHCP, .98 → .95). `--peer <ip>` setups would silently break | High for static-peer users | Docs: recommend hostnames (`rocky.local`, Tailscale MagicDNS) over IPs for `--peer`. v0.2: re-resolve hostnames on every poll, and learn a static peer's new address from its beacon by node ID |
| F4 | Joining over SSH with `muster token \| ssh host muster join` worked first try and kept the token out of argv | Positive | Put this one-liner in the README as the headless/server path |

### Re-test after opening firewalld (`firewall-cmd --add-port=7878/tcp --add-port=7879/udp`)

All passed, with **no `--peer` flags**:

| Check | Result |
|---|---|
| Mac discovers Rocky automatically | ✅ `rocky 192.168.1.95:7878 no-ollama` |
| Rocky discovers Mac automatically | ✅ `macbook 192.168.1.244:7878 ready`, with live loaded-model marker |
| App on Mac → Rocky's endpoint, no token | ✅ 401 |
| App on Mac → Rocky's endpoint, with token | ✅ Rocky routed it back to the Mac: "round trip ok", `X-Muster-Node: macbook` |
| App on Rocky → Mac's endpoint, no token / token | ✅ 401 / 200 |

This confirms F1/F2 are firewall-only. With the ports open, zero-config
discovery works across macOS and RHEL-family Linux. Not yet tested: a
second node *serving* models (Rocky has no Ollama), and live failover
between two serving nodes.
