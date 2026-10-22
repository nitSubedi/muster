# ADR-0005: TLS between agents, with certificates pinned via the cluster key

**Status:** Accepted · 2026-10-22 · Closes the plain-HTTP limitation in ADR-0003/0004

## Context
Through v0.1, prompts and responses travel between agents in plain HTTP. Anyone
who can sniff the LAN can read them. Tailscale users are covered by WireGuard;
everyone else isn't. Home devices have no certificate authority, and a
self-signed certificate on its own is easy to man-in-the-middle.

## Decision
1. **Per-node identity.** On first run each agent generates an ECDSA P-256
   self-signed certificate (key stored 0600 next to the config). Its identity
   is the SHA-256 fingerprint of the certificate.
2. **One port, two protocols.** The agent sniffs the first byte of each
   connection on :7878. A TLS ClientHello (`0x16`) is served over TLS;
   anything else gets plain HTTP. Local apps keep using
   `http://localhost:7878` unchanged, and agents always use TLS.
3. **Channel-bound proofs.** The response proof becomes
   `HMAC(K, "resp", nonce, server-cert-fingerprint)`. The caller computes it
   with the fingerprint of the certificate *it actually saw*. A
   man-in-the-middle presents its own certificate, so the proof the real node
   computed can never match, and the attacker can't compute one without K.
4. **Pin before sending prompts.** A successful proved poll pins that
   peer's fingerprint. Forwarded requests use a TLS dialer that refuses any
   certificate except the pinned one, **during the handshake, before the
   request body is sent**. An attacker who intercepts a forward gets a failed
   handshake, never a prompt.
5. Polls themselves carry no secrets (signed GET, response is node
   inventory), so they may use an unpinned TLS connection: the proof
   check decides whether the response is trusted.

## Consequences
- Agent-to-agent traffic is now confidential and authenticated on any LAN.
- Pins update on every proved poll, so a reinstalled node (new certificate)
  rejoins automatically within one poll interval.
- Mixed v0.1/v0.2 clusters don't interoperate. Acceptable: v0.1 never
  shipped.
- Remote *app* clients (not agents) may still use plain HTTP with the Bearer
  token, and the token is readable on the wire in that case. Documented;
  they can use `https://` with certificate pinning or Tailscale.
