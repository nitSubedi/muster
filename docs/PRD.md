# Muster — Product Requirements (v0.1)

**Owner:** Founder · **Status:** Building v0.1 · **Last updated:** 2026-10-22

## 1. Problem

People who run local AI almost always own more than one capable machine — a
MacBook plus a gaming PC with an NVIDIA GPU, a Mac mini in the closet, an old
workstation. Today each machine is an island:

- The laptop can only run models that fit in *its* memory, even when a 24 GB
  GPU sits idle across the room.
- Every app (Open WebUI, Continue, Cursor, scripts) has to be pointed at one
  specific machine by hand, and breaks when that machine sleeps.
- Existing multi-node tools (exo, GPUStack, llama.cpp RPC) are either focused
  on splitting one model across devices — slow over home networks — or are
  cluster-management products aimed at ops teams.

**The gap:** nobody makes "all my machines act like one AI endpoint" a
one-command experience.

## 2. Product

Muster is a single binary you run on every device you own. The devices find
each other, share what models and hardware they have, and expose **one
OpenAI- and Ollama-compatible endpoint** on every machine. Each request is
routed to the best device that can serve it.

> Run `muster agent` on your laptop and your desktop. Point your apps at
> `localhost:7878`. Your laptop now runs your desktop's 32B model.

## 3. Target user (v0.1)

**"Two-machine developer."** Software developer or AI tinkerer with a laptop
for daily work and at least one stronger machine (GPU desktop / Mac Studio /
Mac mini) on the same network or tailnet. Already uses Ollama. Lives on
r/LocalLLaMA, Hacker News, and in Open WebUI / Continue configs.

## 4. Jobs to be done

1. "Use my strongest machine's models from whichever machine I'm sitting at."
2. "Don't make me reconfigure every app when a machine goes to sleep."
3. "Spread requests across machines when I'm running batch jobs or agents."
4. "Don't expose my machines to the rest of the network to do it."

## 5. v0.1 scope (must ship)

| # | Requirement | Acceptance |
|---|---|---|
| R1 | Zero-config discovery on a LAN | Two agents on one subnet see each other within 5 s, no flags |
| R2 | Manual peers for Tailscale / VLANs | `--peer host:port` joins without broadcast |
| R3 | Drop-in API | OpenAI `/v1/*` and Ollama `/api/*` clients work unchanged |
| R4 | Model-aware routing | Request goes to a node that has the model; prefers already-loaded; then least busy; then local |
| R5 | Failover | If the chosen node is unreachable, the request transparently goes to the next candidate |
| R6 | Unified model list | `/v1/models` and `/api/tags` list models from every healthy node |
| R7 | Secure by default | Cluster token auto-generated; non-loopback requests require it; Ollama never exposed to the LAN |
| R8 | Observability | `muster status` shows nodes, hardware, models, load; responses carry `X-Muster-Node` |
| R9 | Single static binary | macOS (arm64/amd64), Linux (arm64/amd64), Windows (amd64) |

## 6. Non-goals (v0.1)

- Splitting a single model's layers across devices (exo's job; poor fit for
  Wi-Fi). Revisit once routing has users.
- Non-AI workloads (builds, encodes). The scheduler is designed to be
  workload-agnostic so these become plugins later — see ROADMAP.
- Automatic model download / placement.
- Public-internet or multi-tenant operation.

## 7. Success metrics

Measured from the launch post onward. No telemetry in the product — metrics
come from GitHub, release downloads, and an opt-in feedback form.

| Metric | 30 days | 90 days |
|---|---|---|
| GitHub stars | 500 | 2,000 |
| Release downloads | 1,000 | 5,000 |
| Users reporting ≥2 nodes in a cluster (issues/Discord/survey) | 50 | 300 |
| Inbound "can my team use this?" conversations | 3 | 15 |

The last row is the commercial signal: team usage (shared GPU box in an
office) is the paid tier hypothesis.

## 8. Commercial hypothesis

Open-source core (agent, routing, discovery) to win adoption. Paid
**Muster Teams**: hosted coordinator for machines across sites, SSO, usage
accounting per user, and policy ("interns can't use the 70B box"). Validate
with the inbound metric above before building any of it.

## 9. Risks

| Risk | Mitigation |
|---|---|
| Ollama adds native multi-host | Stay backend-agnostic (llama.cpp, vLLM, LM Studio next); routing + policy is the product |
| exo/GPUStack move into this niche | Win on install experience and "works with what you already run" |
| Security incident (open LAN endpoint) | Token-by-default, loopback exemption only, Ollama stays on localhost |
