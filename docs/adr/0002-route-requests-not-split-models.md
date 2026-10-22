# ADR-0002: Route whole requests; don't split models across devices

**Status:** Accepted · 2026-10-22

## Context
Two ways to use several machines for inference:
1. **Split** one model's layers across devices (exo, llama.cpp RPC).
2. **Route** each request to one device that holds the whole model.

Splitting lets you run models larger than any single device, but every token
crosses the network once per device boundary. Over Wi-Fi that is often
5–10× slower than running a smaller model locally, and the slowest device
sets the pace.

## Decision
v0.1 routes whole requests. The target user already has one strong machine;
the value is reaching it from everywhere, reliably, with zero app
reconfiguration.

## Consequences
- Latency is native-speed on the serving node plus one LAN hop.
- Models larger than the biggest single device are not supported in v0.1.
- The router is the extension point. A future "split" backend (llama.cpp
  RPC) can appear as just another node type to the router.
