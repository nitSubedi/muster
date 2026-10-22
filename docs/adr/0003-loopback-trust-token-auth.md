# ADR-0003: Trust loopback, token for everything else, Ollama stays local

**Status:** Accepted · 2026-10-22

## Context
The common "multi-machine Ollama" advice is `OLLAMA_HOST=0.0.0.0`, which
exposes an unauthenticated API to everyone on the network. We must not repeat
that.

## Decision
- Ollama stays on 127.0.0.1. Only Muster listens on the network.
- Requests from loopback are trusted, so existing local apps need no changes.
- All other requests need the cluster token. OpenAI clients can send it as
  their API key (`Authorization: Bearer`).
- The token is generated on first run and never broadcast. Beacons carry only
  a hash-derived cluster ID.
- Agent-to-agent traffic is HTTP in v0.1. Tailscale users get encryption from
  WireGuard. Native TLS with trust-on-first-use certificate pinning is
  planned for v0.2.

## Consequences
- Joining a device means copying one token (`muster token` prints it).
- A LAN attacker can see that a Muster cluster exists and read traffic
  between agents on plain LANs until v0.2. Documented in the README.
