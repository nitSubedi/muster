# ADR-0004: Agents prove the token with HMAC and never send it

**Status:** Accepted · 2026-10-22 · Supersedes the agent-to-agent part of ADR-0003

## Context
Pre-release review found a critical flaw in v0.1's first design. Agents sent
the raw cluster token in `X-Muster-Token` to any address that beaconed with
the right cluster ID. The cluster ID is broadcast in clear text, so any device
on the LAN could:
1. read the cluster ID from a beacon,
2. send its own beacon with that ID,
3. receive the token on the next poll, and
4. claim to have every model loaded, so it attracted every prompt.

UDP source addresses can also be spoofed, which turned this into token
exfiltration to arbitrary hosts. Confirmed with a proof of concept, then fixed
before any release.

## Decision
- The token never crosses the network between agents. Each agent derives a
  key `K = SHA-256("muster-peer-auth:" + token)`.
- Every agent-to-agent request carries
  `X-Muster-Auth: v1 <ts> <nonce> HMAC(K, "req", ts, nonce, method, uri)`.
  The receiver rejects timestamps more than 60 s from its clock, and nonces it
  has already seen.
- Every response carries `X-Muster-Proof: HMAC(K, "resp", nonce)`. The caller
  discards responses without a valid proof. Peers become routing candidates
  only after a proved poll, and forwarded prompts are only relayed back with a
  proved response.
- Peer HTTP clients ignore `HTTP(S)_PROXY` and never follow redirects.
- Discovered (beacon-only) peers are capped at 64. Unproven entries are
  evicted first, so a beacon flood cannot push out real devices.

## Consequences
- A LAN attacker can learn that a cluster exists, but cannot join it, steal the
  token, or receive prompts.
- Agents' clocks must agree within 60 s. NTP is the default on every supported
  OS, and the error message names clock skew as a possible cause.
- The request body is not signed. Someone who can actively tamper with LAN
  traffic could alter a forwarded prompt within the 60 s window. That falls
  under the same plain-HTTP limitation that TLS in v0.2 closes.
- App clients on other devices still send the token (`Authorization: Bearer`),
  but only to an address the user typed in themselves.
