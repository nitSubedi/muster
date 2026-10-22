# Muster Architecture (v0.1)

## Topology

Every device runs the same `muster agent`. There is no leader. Each agent is
simultaneously a **gateway** (accepts client requests), a **peer** (serves
requests forwarded by other agents), and a **reporter** (publishes its own
hardware and model inventory).

```
  app ──► muster agent (laptop) ──────────────► muster agent (desktop)
          │ gateway  :7878                       │ /muster/v1/forward/*
          │ router picks best node               ▼
          │                                     Ollama 127.0.0.1:11434
          ▼
          Ollama 127.0.0.1:11434   (used when the laptop is the best node)
```

Ollama always stays bound to loopback. Only the agent listens on the network,
and only token-authenticated callers get past it.

## Packages

| Package | Responsibility |
|---|---|
| `cmd/muster` | CLI: `agent`, `status`, `token`, `version` |
| `internal/config` | Config file, token generation, cluster ID |
| `internal/node` | `Info` type; collect local hardware (CPU, RAM, NVIDIA GPU) |
| `internal/ollama` | Minimal client for `/api/tags` and `/api/ps` |
| `internal/discovery` | UDP broadcast beacons (announce + listen) |
| `internal/peerauth` | HMAC request signing and response proofs between agents |
| `internal/registry` | Live view of all nodes: self + peers, health, expiry, polling |
| `internal/router` | Pure function: (model, nodes) → ordered candidates |
| `internal/gateway` | HTTP server: API compatibility, auth, proxy with failover, aggregation |

Dependency direction is strictly downward: `gateway → router, registry →
node, ollama, discovery → config`. `router` has no I/O and is the most
heavily tested package.

## Discovery

- Each agent broadcasts a JSON beacon to UDP `255.255.255.255:7879` every 2 s:
  `{v, cluster, id, name, port}`.
- `cluster` is the first 8 bytes (hex) of `SHA-256(token)`. Agents ignore
  beacons from other clusters, so two households on one café Wi-Fi never mix.
  The token itself is never broadcast.
- The beacon only says "I exist at this IP:port", and anyone can forge it.
  The receiver polls `GET /muster/v1/node` with a signed request, and the peer
  becomes a candidate only if its response carries a valid proof (ADR-0004).
  At most 64 beacon-discovered peers are tracked; unproven ones are evicted first.
- Static peers (`--peer`) skip beacons and are polled directly. This is the
  Tailscale / routed-network path.

## Node lifecycle (registry)

- Poll every healthy or static peer every 2 s.
- A peer is **healthy** after a successful poll, **unhealthy** after 3
  consecutive failures, and **forgotten** if it is discovered (not static) and
  no beacon has been seen for 30 s.
- The local node is refreshed from Ollama directly on the same interval.

## Routing (router)

Input: requested model name and a snapshot of healthy nodes. Output: ordered
candidate list. Order by, in priority:

1. Node has the model (names normalised: `llama3` ≡ `llama3:latest`).
2. Model already loaded in memory (avoids a 5–60 s cold load).
3. Fewer in-flight requests (reported by the node, combined with requests
   this gateway has dispatched but the node hasn't reported yet).
4. More accelerator memory (GPU VRAM, else total RAM) — stronger box wins ties.
5. Local node (no network hop).
6. Node ID, for determinism.

The gateway tries candidates in order, moving on only when a candidate fails
**before any response bytes are sent**: connection error, missing peer proof,
502, 503, or 404 (inventory can be one poll stale). Dead peers mid-request
are detected by TCP keepalive (~25 s); there is deliberately no response
timeout, because long generations are legitimate.

## Request path

1. Client → gateway. Auth: genuine loopback callers (loopback address *and*
   loopback Host/Origin) are trusted; other clients need
   `Authorization: Bearer <token>`; agents use peer signatures.
2. Gateway reads the body (≤ 64 MiB) and extracts `"model"`.
3. Router returns candidates. No candidates → `404` listing known models.
4. Local candidate: proxy to Ollama. Remote: proxy a signed request to
   `http://peer/muster/v1/forward/<path>` and require a valid proof on the
   response. The forward endpoint only ever serves locally, which makes routing
   loops impossible.
5. Response streams back with a flush after every read, so SSE (OpenAI) and
   NDJSON (Ollama) streaming work unchanged. `X-Muster-Node` names the server.

Requests without a model (`/api/version`, unknown paths) go to local Ollama.
`/v1/models` and `/api/tags` are answered by the gateway from the registry.

## Security model

- Threat: other devices on the same LAN. Not in scope: a compromised device
  inside the cluster (it has the token by definition).
- Token: 32 random bytes, base64url, saved to `<user config dir>/muster/config.json`
  with mode 0600 on first run. Compared in constant time.
- Agents never send the token to each other; they sign requests and prove
  responses with an HMAC key derived from it (ADR-0004).
- Traffic between agents is plain HTTP in v0.1, so prompts are readable by a
  LAN sniffer. Over Tailscale it is already encrypted; LAN TLS is v0.2.
