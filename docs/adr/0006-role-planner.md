# ADR-0006: Muster decides each device's role (`muster plan`)

**Status:** Accepted (v1 is a dry run) · 2026-10-22

## Context
People own several machines of very different strength. One request can't
be made faster by splitting it across a home network (ADR-0002, and the
`muster bench` measurements in docs/FIELD_TESTS.md). The cluster as a whole
*can* do more if each device does the work it's best at. Today users have to
work that out themselves, and nobody does.

Field data shaped the design. On the reference network, the Linux box's
hardware was adequate but it was running other work (load 31.7 on 4 threads,
5.5 GB in swap). A plan based on hardware specs alone would have been wrong.
Roles must come from **measured** speed, adjusted for **current** load.

## Decision
`muster plan` assigns roles from three inputs:

1. **Measurements**, the last `muster bench` report (saved next to the config).
   Each device's effective model bandwidth (bytes of model streamed per second
   while generating) is its capacity for LLM work.
2. **Live state**, from cluster status: which devices are healthy, have Ollama
   up, which models are installed, total memory, and current load.
3. **Load adjustment**: capacity now = measured capacity × (free CPU now ÷ free
   CPU when measured), where free CPU = clamp(1 − load/CPUs, 0.05, 1).

### Roles in v1, all things Muster can already carry out through routing

| Role | Goal | Rule |
|---|---|---|
| **chat** | Best answers at a readable speed | For each device, the largest installed model predicted to stay at ≥ 10 tok/s. Pick the device with the largest such model; ties go to the faster. |
| **embeddings** | Volume without slowing chat | Prefer a device other than the chat device, with an embedding model and ≥ 30 % free CPU. If none qualifies, use the chat device and say so; if a free device lacks the model, suggest pulling it. |
| **batch** | Total throughput | Every device whose share of capacity is ≥ 5 %, with work split in proportion to capacity. A device below 5 % isn't worth the coordination and is listed with its share. |
| **standby** | Honesty | Any device with no role, and why: busy, no Ollama, too slow for anything, unreachable. |

The planner is a pure function, `plan(measurements, live state) → assignments
+ suggestions`, so every rule is unit-tested without machines.

### Not in v1
- `--apply` (pull models, pin roles, make routing follow roles) and automatic
  re-planning. These ship after the dry-run recommendations prove right on real
  clusters.
- Free memory and battery state as inputs (total memory is used for the fit
  check, at most 75 % of it). Planned next.
- Roles needing per-task benchmarks (speech-to-text, OCR, image generation)
  and the model-library role.

## Consequences
- The output is advice the user can check against `muster bench`, which keeps
  trust high while the rules are tuned.
- Device names identify devices in plans. Two devices with the same name are
  ambiguous; `--name` fixes that.
