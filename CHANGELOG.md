# Changelog

## Unreleased — v0.1.0

### Added
- `muster agent`: discovery (UDP beacons scoped by cluster ID), static peers,
  model-aware routing with failover, OpenAI and Ollama compatible gateway
  with streaming, aggregated `/v1/models` and `/api/tags`.
- Token auth for non-local callers; loopback trust hardened against
  cross-site requests and DNS rebinding.
- `muster status`, `muster token`, `muster version`.
- Hardware reporting: CPU, RAM, NVIDIA GPUs via `nvidia-smi`.
