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

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/nitSubedi/muster/main/install.sh | sh
# or: brew install nitSubedi/tap/muster
```

The installer verifies the binary's SHA-256 against the release checksums,
which catches corrupted downloads (it's not a signature: the checksums ship
with the same release). It installs to `/usr/local/bin`, or to
`~/.local/bin` without sudo, and restarts a running service on upgrade.
Windows: download `muster-windows-amd64.exe` from Releases.

## Quick start

You need [Ollama](https://ollama.com) installed on each machine that should
serve models. Machines without Ollama can still route.

**1. First device**

```sh
muster agent
```

**2. Every other device**

On the first device, print the cluster token:

```sh
muster token
```

On the new device, paste it when prompted, then start the agent:

```sh
muster join
muster agent
```

Within a few seconds, `muster status` on either machine shows both.

Adding a headless server over SSH takes one line:

```sh
muster token | ssh you@server 'muster join && muster agent'
```

**Linux with firewalld (Fedora, RHEL, Rocky, Alma)** blocks Muster by
default. Open the ports on that machine:

```sh
sudo firewall-cmd --permanent --add-port=7878/tcp --add-port=7879/udp && sudo firewall-cmd --reload
```

On Ubuntu with ufw enabled, run `sudo ufw allow 7878/tcp && sudo ufw allow 7879/udp`.

**3. Point your apps at Muster**

| Client | Setting |
|---|---|
| Anything OpenAI-compatible | Base URL `http://localhost:7878/v1`, any API key |
| Anything Ollama-compatible | `OLLAMA_HOST=http://localhost:7878` |
| Open WebUI | Ollama URL `http://localhost:7878` |
| Another device on your network | Base URL `http://<device-ip>:7878/v1`, API key = cluster token |

Every response includes an `X-Muster-Node` header naming the machine that
served it.

## Run at login

```sh
muster service install            # add any agent flags, e.g. --peer desktop.ts.net:7878
muster service status
muster service uninstall
```

This uses a per-user launchd agent on macOS and a `systemd --user` unit on
Linux. No sudo is needed, it restarts automatically if it crashes, and
`OLLAMA_HOST` is captured at install time. On a headless Linux server, also
run `loginctl enable-linger $USER` so it keeps running after you log out.

## When something doesn't work

```sh
muster doctor
```

Doctor checks this device and asks the other devices in your cluster to
connect back to it, so it catches problems that are invisible from the
inside, such as a firewall dropping inbound connections. Every problem comes
with the command that fixes it:

```
✔ Agent      rocky on port 7878
✔ Peers      reachable: macbook
✘ Inbound    macbook cannot connect to this device on tcp 7878
             fix: sudo firewall-cmd --permanent --add-port=7878/tcp --add-port=7879/udp && sudo firewall-cmd --reload
```

It also warns if Ollama is exposed to your network without authentication
(`OLLAMA_HOST=0.0.0.0`).

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
  cluster token as `Authorization: Bearer <token>`.
- Agents never send the token to each other. They sign every request and
  prove every response with a key derived from it, so a device on your Wi-Fi
  that impersonates a Muster node can't steal the token or receive prompts
  ([ADR-0004](docs/adr/0004-peer-auth-hmac-never-send-token.md)). Device clocks
  must agree within 60 s.
- The token is created on first run and stored with owner-only permissions
  in your user config directory (`~/Library/Application Support/muster/` on
  macOS, `~/.config/muster/` on Linux, `%AppData%\muster\` on Windows, where
  it inherits your profile's ACL). It is never broadcast. `muster join` reads
  it from stdin so it stays out of shell history.
- Traffic between agents is **TLS 1.3**. Each device has its own certificate,
  pinned via the cluster key, so there's no certificate authority to set up and
  no man-in-the-middle window ([ADR-0005](docs/adr/0005-tls-between-agents-cluster-bound-pins.md)).
  Local apps keep using plain `http://localhost:7878` on the same port.
- Apps on *other* devices that call Muster over plain `http://` send the
  token unencrypted. Prefer Tailscale for those, or `https://` with
  certificate pinning.

## Flags

```
muster agent  --listen :7878           API + peer port
              --ollama URL             local Ollama (default $OLLAMA_HOST or 127.0.0.1:11434)
              --name NAME              display name (default hostname)
              --peer HOST:PORT         static peer, repeatable
              --token TOKEN            join an existing cluster (prefer `muster join`)
              --no-discovery           disable LAN broadcast
muster status --json                   cluster view from this device
muster token                           print this cluster's token
muster join                            save a token pasted on stdin
muster doctor                          diagnose this device, with fixes
muster service install|uninstall|status   run at login (no sudo)
```

## Build from source

```sh
make test     # race-enabled tests
make build    # ./bin/muster
make dist     # binaries for every platform in ./dist
```

## Roadmap

See [docs/ROADMAP.md](docs/ROADMAP.md). Next up: throughput-aware routing
(field test F7) and more backends (LM Studio, llama.cpp server, vLLM).
