# Muster

**Every machine you own, one AI endpoint.**

You have a laptop you work on and a desktop with a big GPU (or a Mac mini,
or an old workstation). Muster makes them act like one Ollama server.
Run it on each device, point your apps at `localhost:7878`, and every request
goes to the best machine that has the model, whichever device you're sitting at.

```
$ muster status
NODE     ADDRESS            STATE  HARDWARE                           BUSY  MODELS (* = loaded)
laptop   (this device)      ready  darwin/arm64 11cpu 18GB            0     qwen2.5:3b
desktop  192.168.1.20:7878  ready  linux/amd64 16cpu 64GB +RTX 4090   1     qwen2.5:32b*, llama3.3:70b
```

- **Drop-in.** OpenAI (`/v1/*`) and Ollama (`/api/*`) APIs, so Open WebUI,
  Continue, Cursor, LangChain and `curl` work unchanged. Streaming included.
- **Model-aware routing.** Goes to a node that has the model, prefers one
  where it's already loaded (no cold start), then the least busy, then the
  strongest hardware.
- **Survives sleep.** If a machine drops off, requests fail over to the next
  one that can serve them.
- **Zero config on a LAN.** Devices find each other automatically. On
  Tailscale or across subnets, add `--peer`.
- **Secure by default.** Ollama stays on localhost. Muster requires a cluster
  token from anything that isn't this device, and blocks browser-based
  cross-site and DNS-rebinding attacks.
- **One binary, no dependencies.** macOS, Linux and Windows on ARM and x86.

## Quick start

You need [Ollama](https://ollama.com) installed on each machine that should
serve models. Machines without Ollama can still route.

**1. First device**

```sh
muster agent
```

**2. Every other device**

On the first device, run `muster token`. It prints a join command:

```sh
muster agent --token <your-cluster-token>
```

Run that on the new device. Within a few seconds, `muster status` on either
machine shows both.

**3. Point your apps at Muster**

| Client | Setting |
|---|---|
| Anything OpenAI-compatible | Base URL `http://localhost:7878/v1`, any API key |
| Anything Ollama-compatible | `OLLAMA_HOST=http://localhost:7878` |
| Open WebUI | Ollama URL `http://localhost:7878` |
| Another device on your network | Base URL `http://<device-ip>:7878/v1`, API key = cluster token |

Every response includes an `X-Muster-Node` header naming the machine that
served it.

## Tailscale, VPNs and separate subnets

Broadcast discovery only reaches your local subnet. For anything else, list
peers explicitly (on both sides):

```sh
muster agent --peer desktop.tailnet-name.ts.net:7878
```

## How it decides where a request goes

1. Nodes that have the requested model (`llama3` = `llama3:latest`).
2. Model already loaded in memory first, so you skip a 5–60 s load.
3. Fewest requests in flight.
4. Most GPU memory (or RAM on Apple Silicon).
5. This device, to avoid a network hop.

Pull, push, create, copy and delete always run on the device you sent them
to. Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Security

- Ollama is never exposed to the network. Only Muster listens, on `:7878`.
- Requests from this device are trusted. Requests from anywhere else need the
  cluster token as `Authorization: Bearer <token>` or `X-Muster-Token`.
- The token is created on first run and stored with owner-only permissions
  in your user config directory (`~/Library/Application Support/muster/` on
  macOS, `~/.config/muster/` on Linux). It is never broadcast.
- **Known limitation (v0.1):** traffic between agents is plain HTTP. Anyone
  who can sniff your LAN can read prompts in transit between your devices.
  Tailscale encrypts this for you. Native TLS is planned for v0.2.

## Flags

```
muster agent  --listen :7878           API + peer port
              --ollama URL             local Ollama (default $OLLAMA_HOST or 127.0.0.1:11434)
              --name NAME              display name (default hostname)
              --peer HOST:PORT         static peer, repeatable
              --token TOKEN            join an existing cluster
              --no-discovery           disable LAN broadcast
muster status --json                   cluster view from this device
muster token                           print the join command
```

## Build from source

```sh
make test     # race-enabled tests
make build    # ./bin/muster
make dist     # binaries for every platform in ./dist
```

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md). Next up: TLS between agents,
`brew install`, run-at-login service, and more backends (LM Studio,
llama.cpp server, vLLM).
