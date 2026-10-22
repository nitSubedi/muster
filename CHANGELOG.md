# Changelog

## Unreleased — v0.2.0

### Added
- `muster doctor`: checks config permissions, Ollama, **Ollama network
  exposure** (`OLLAMA_HOST=0.0.0.0`), the agent, and peers. It also asks peers
  to connect back (**inbound reachability**) and checks whether **discovery
  broadcasts** arrive. Each problem prints an OS-specific fix (firewalld, ufw,
  macOS, Windows). Verified on the Rocky field box (field tests F1–F3).
- `/muster/v1/probe`: an authenticated agent dials back only to the
  caller's own IP, so it can't be pointed at third parties.
- `muster service install|uninstall|status`: run at login via a per-user
  launchd agent (macOS) or `systemd --user` unit (Linux), with no sudo and
  automatic restart on crash. Verified on Rocky 9.6 (install, kill -9 →
  restart in ~5 s, uninstall).
- `install.sh` (`curl | sh`): picks the right binary, verifies SHA-256
  against `SHA256SUMS`, and refuses tampered downloads; no sudo needed.
  Verified on macOS arm64 and Rocky Linux x86_64.
- Release workflow: tagging `v*` runs tests, builds every platform, publishes
  a GitHub Release, and renders the Homebrew formula
  (`packaging/homebrew/render.sh`).
- **TLS 1.3 between agents** (ADR-0005): per-node certificates, response
  proofs bound to the server certificate, forwards pinned in the handshake
  before any prompt bytes are sent, and peer requests over plain HTTP refused.
  Local apps keep plain HTTP on the same port. Tested against an active MITM
  relay (never trusted, never sent a prompt) and verified on the LAN.
- `muster status` names the peer's actual port in unreachable diagnoses.

### Fixed (second pre-release security review)
- **DoS:** one temporary `accept` error (e.g. too many open files) left the
  listener dead while the process looked healthy. It now retries with
  backoff, and silent pre-TLS connections are capped at 256 with a 3 s sniff
  deadline.
- `/muster/v1/probe` now requires a peer signature or the token. Loopback
  trust alone let any web page trigger signed requests to local ports.
- `muster service install` rejects arguments containing control characters
  (newline injection into the systemd unit), retries launchd bootstrap after
  bootout, and records the stable Homebrew `bin/` path instead of the
  versioned Cellar path that `brew cleanup` deletes.
- Node certificate and key now live in one atomically created `node.pem`, so
  concurrent first runs can't produce a mismatched pair.
- Peer URLs escape IPv6 zones (link-local probes no longer misreport a firewall).
- Release workflow: tag passed via env and validated (no script injection);
  only the publish job has write permission.
- `install.sh` runs entirely inside `main()` (safe against truncated
  `curl | sh`), stages on the install filesystem for an atomic replace, and
  restarts a running service after an upgrade.
- Homebrew formula no longer defines a competing `brew services` unit.

## Unreleased — v0.1.0

### Added
- `muster agent`: discovery (UDP beacons scoped by cluster ID), static peers,
  model-aware routing with failover, OpenAI and Ollama compatible gateway
  with streaming, aggregated `/v1/models` and `/api/tags`.
- Token auth for non-local callers; loopback trust hardened against
  cross-site requests and DNS rebinding.
- `muster status`, `muster token`, `muster join`, `muster version`.
- Agent-to-agent authentication with HMAC-signed requests and response proofs;
  the token never crosses the network (ADR-0004).
- Hardware reporting: CPU, RAM, NVIDIA GPUs via `nvidia-smi`.

### Security (fixed before release)
- First design sent the raw token to any address that beaconed with the
  broadcast cluster ID, so a LAN attacker could steal it and capture prompts.
  Found in pre-release review and replaced by ADR-0004.
- Peer clients no longer honour `HTTP(S)_PROXY` or follow redirects.
- Beacon-discovered peers capped at 64; peer responses capped at 1 MiB.

### Fixed (from review)
- Stale-inventory 404s fail over to the next node.
- Dead peers mid-request detected by TCP keepalive and failed over.
- Ctrl-C cancels in-flight requests and exits 0.
- `OLLAMA_HOST` IPv6 forms (`[::1]`, `[::]`) parsed correctly.
- Windows reports total RAM.
