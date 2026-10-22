# Changelog

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
