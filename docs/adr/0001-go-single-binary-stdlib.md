# ADR-0001: Go, single static binary, standard library only

**Status:** Accepted · 2026-10-22

## Context
The product promise is "one command on every device". Users run macOS,
Linux and Windows on both ARM and x86. Installing a Python runtime or Node on
a gaming PC to join a cluster kills adoption.

## Decision
Write Muster in Go. Ship one static binary per platform. Use only the Go
standard library in v0.1 — discovery is a small UDP beacon rather than an
mDNS library.

## Consequences
- Cross-compiling every target is a `GOOS/GOARCH` loop; no CGO.
- No third-party supply chain to audit, which matters for a tool that sits
  on people's home networks.
- We own a little more code (beacon protocol, memory probes). Acceptable:
  each is under 150 lines and tested.
