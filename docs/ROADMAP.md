# Roadmap

Ordered by what moves the PRD metrics. Each milestone ships when it's done,
not on a date.

## v0.1 — "It works on my two machines" (current)
Discovery, routing, failover, OpenAI + Ollama APIs, token auth, `status`.

## v0.2 — "I'd leave it running" (adoption)
- [ ] **Install in one line**: Homebrew tap, `curl | sh` script, Scoop for Windows, GitHub Releases via CI.
- [x] **Run at login**: `muster service install` (launchd / systemd user units). Windows Task Scheduler: still to do.
- [ ] **TLS between agents**: per-node self-signed certificates, pinned on first contact by cluster token (closes the README's known limitation).
- [ ] **Live load reporting**: push in-flight changes to peers on change, not just on 2 s polls, so request bursts spread immediately.
- [x] **`muster doctor`**: checks firewall, Ollama binding, discovery reachability, and token mismatch, and prints the fix. Top priority after field test F1/F2: firewalld blocks Muster on every RHEL-family distro.
- [x] **Reachability reports**: done as doctor's peer probe (field test F3).

## v0.3 — "Works with what I already run" (reach)
- [ ] Backend interface; adapters for **LM Studio**, **llama.cpp server**, **vLLM** (all OpenAI-compatible).
- [ ] **Model placement hints**: "llama3.3:70b is only on desktop (asleep). Wake it or pull it on mac-mini (fits: 64 GB)."
- [ ] **Wake-on-LAN**: wake a sleeping node when it's the only one with the requested model.
- [ ] Web dashboard at `localhost:7878/` (status, live requests, per-node throughput).

## v0.4 — Beyond chat (the general scheduler, per ADR-0002)
- [ ] Workload plugin interface: a job type declares its requirements (GPU, RAM, binaries) and the router places it.
- [ ] First non-LLM workloads: Whisper transcription, image generation (ComfyUI), embeddings batch jobs.
- [ ] Optional **split mode** for models too big for any one node, using llama.cpp RPC as a backend.

## Muster Teams (commercial — validate before building)
Trigger: 15 inbound "can my team use this?" conversations (PRD §7).
- Hosted coordinator so machines across offices and homes form one cluster.
- SSO, per-user tokens, usage accounting, and policy (who can use which node or model).
- Audit log for teams with compliance needs.
