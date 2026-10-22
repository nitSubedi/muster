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
- `muster status` names the peer's actual port in unreachable diagnoses.

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
