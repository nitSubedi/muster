# Muster

**Every machine you own, one AI endpoint.**

You have a laptop you work on and a desktop with a big GPU (or a Mac mini,
or an old workstation). Muster makes them act like one Ollama server. Run it
on each device, point your apps at `localhost:7878`, and every request goes to
the best machine that has the model, whichever device you're sitting at.

```console
$ muster status
NODE     ADDRESS            STATE  HARDWARE                                       BUSY  MODELS (* = loaded)
laptop   (this device)      ready  darwin/arm64 10cpu 16GB                        0     qwen2.5:7b, llama3.2:3b
desktop  192.168.1.21:7878  ready  linux/amd64 16cpu 64GB +GeForce RTX 4090 24GB  1     qwen2.5:32b*, llama3.3:70b, qwen2.5:7b
```

- **Drop-in.** OpenAI (`/v1/*`) and Ollama (`/api/*`) APIs, so Open WebUI,
  Continue, Cursor, LangChain and `curl` work unchanged, streaming included.
- **Model-aware routing.** Each request goes to a machine that has the model.
  It prefers one where the model is already loaded (no cold start), then the
  least busy, then the strongest hardware.
- **Survives sleep.** If a machine drops off, requests fail over to the next
  one that can serve them.
- **Zero config on a LAN.** Devices find each other automatically.
- **Secure by default.** Ollama stays on localhost. Traffic between machines is
  TLS 1.3, and other devices need the cluster token.
- **One binary, no dependencies.** macOS, Linux and Windows, ARM and x86.

---

## 1. Install

Install [Ollama](https://ollama.com) on every machine that should **serve**
models. A machine without Ollama can still **route** requests to the others.

Then install Muster on every machine:

```console
$ curl -fsSL https://raw.githubusercontent.com/nitSubedi/muster/main/install.sh | sh
```

**Success looks like:**

```console
Downloading muster-darwin-arm64 (latest)...
Installed muster v0.2.0 to /Users/you/.local/bin/muster

Add /Users/you/.local/bin to your PATH:  export PATH="/Users/you/.local/bin:$PATH"

Next:
  First device:   muster service install     (or just: muster agent)
  Other devices:  muster join && muster service install
  Problems?       muster doctor
```

The installer picks the right binary for your OS and CPU and checks its
SHA-256 against the release's checksums. If the checksum doesn't match, it
stops without installing anything:

```console
muster install: checksum mismatch for muster-darwin-arm64 (want 5466…, got a5b4…); not installing
```

It installs to `/usr/local/bin` if that's writable, otherwise to
`~/.local/bin` without sudo. If it prints an `export PATH=…` line, add that
line to your shell profile. Then confirm:

```console
$ muster version
muster v0.2.0
```

**Windows:** download `muster-windows-amd64.exe` from
[Releases](https://github.com/nitSubedi/muster/releases) and run it from a
terminal.

---

## 2. Start the first device

```console
$ muster agent
```

**Success looks like:**

```console
time=2026-10-22T09:14:03.112-05:00 level=INFO msg="created new cluster" config="/Users/you/Library/Application Support/muster/config.json"
time=2026-10-22T09:14:03.130-05:00 level=INFO msg="muster agent running" node=laptop listen=:7878 cluster=3f9c2a71d04be6e1 models=2

  Point your apps at http://localhost:7878  (OpenAI: /v1, Ollama: OLLAMA_HOST=http://localhost:7878)
  Add another device: run `muster token` here, then `muster join` there.
```

The first run creates a new cluster: a random token, a node ID and a TLS
certificate. They're saved readable only by you in
`~/Library/Application Support/muster/` (macOS), `~/.config/muster/` (Linux) or
`%AppData%\muster\` (Windows).

`muster agent` runs in the foreground (Ctrl-C stops it). To run it in the
background and start it at login instead, see [Run at login](#6-run-at-login).

---

## 3. Add your other devices

**On the first device**, print the cluster token:

```console
$ muster token
Cluster token (keep it private). On the new device run `muster join` and paste it:
Q2x1c3Rlci10b2tlbi1leGFtcGxlLW5vdC1yZWFsLTAw
```

**On the new device**, join the cluster, then start the agent:

```console
$ muster join
Paste the cluster token from `muster token` on another device: Q2x1c3Rlci10b2tlbi1leGFtcGxlLW5vdC1yZWFsLTAw
Joined cluster 3f9c2a71d04be6e1. Start (or restart) `muster agent`.

$ muster agent
```

The cluster ID (`3f9c2a71d04be6e1`) must match the one the first device
printed. If it's different, the token was pasted wrong. If the agent was
already running on this device, restart it after joining.

Adding a headless server over SSH takes one line, run from a device that's
already in the cluster:

```console
$ muster token | ssh you@server 'muster join && muster agent'
```

Within a few seconds, every device lists all the others (see the next step).

### Linux firewalls

Fedora, RHEL, Rocky and Alma ship with **firewalld** on, and it blocks Muster
by default. Open the ports once on that machine:

```console
$ sudo firewall-cmd --permanent --add-port=7878/tcp --add-port=7879/udp && sudo firewall-cmd --reload
success
success
```

On Ubuntu with **ufw** enabled:

```console
$ sudo ufw allow 7878/tcp && sudo ufw allow 7879/udp
Rule added
Rule added
```

### Tailscale, VPNs and separate subnets

Automatic discovery only reaches your local network. Elsewhere, list the other
device explicitly, on both sides. Use a hostname rather than an IP, so a
changed DHCP address doesn't break it:

```console
$ muster agent --peer desktop.your-tailnet.ts.net:7878
```

---

## 4. Check that it's working

```console
$ muster status
```

**Success looks like** (every device `ready`, and `*` marks models already loaded in memory):

```console
NODE     ADDRESS            STATE  HARDWARE                                       BUSY  MODELS (* = loaded)
laptop   (this device)      ready  darwin/arm64 10cpu 16GB                        0     qwen2.5:7b, llama3.2:3b
desktop  192.168.1.21:7878  ready  linux/amd64 16cpu 64GB +GeForce RTX 4090 24GB  0     qwen2.5:32b*, llama3.3:70b, qwen2.5:7b
```

Then run the full check:

```console
$ muster doctor
```

**Success looks like:**

```console
✔ Config     /Users/you/Library/Application Support/muster/config.json
✔ Ollama     0.40.8 at http://127.0.0.1:11434
✔ Exposure   Ollama is only reachable through Muster
✔ Agent      laptop on port 7878
✔ Peers      reachable: desktop
✔ Inbound    this device is reachable from desktop
✔ Discovery  broadcasts received from every peer

All good.
```

**When something's wrong**, doctor says what and prints the fix. Here a
firewall on this device blocks the others from connecting in, which the device
can't detect on its own, so doctor asks its peers to connect back:

```console
✔ Agent      desktop on port 7878
✔ Peers      reachable: laptop
✘ Inbound    laptop cannot connect to this device on tcp 7878
             fix: sudo firewall-cmd --permanent --add-port=7878/tcp --add-port=7879/udp && sudo firewall-cmd --reload
✔ Discovery  broadcasts received from every peer
muster: 1 problem(s) found
```

`muster status` explains unreachable devices too:

```console
NODE    ADDRESS            STATE                                                                                    HARDWARE                 BUSY  MODELS (* = loaded)
laptop  (this device)      ready                                                                                    darwin/arm64 10cpu 16GB  0     qwen2.5:7b, llama3.2:3b
?       192.168.1.21:7878  down: unreachable (agent stopped, or firewall: open tcp 7878 + udp 7879 on that device)  -                        0     -
```

---

## 5. Use it

Point your apps at **`localhost:7878`** on whichever device you're using:

| App | Setting |
|---|---|
| OpenAI-compatible (Continue, Cursor, LangChain, OpenAI SDKs) | Base URL `http://localhost:7878/v1`, any API key |
| Ollama-compatible (Ollama CLI, Open WebUI, scripts) | `OLLAMA_HOST=http://localhost:7878` |
| An app running on a **different** device | Base URL `http://<device-ip>:7878/v1`, API key = cluster token |

Try it:

```console
$ curl -i localhost:7878/v1/chat/completions -H 'Content-Type: application/json' \
    -d '{"model":"qwen2.5:32b","messages":[{"role":"user","content":"Say hello in five words."}]}'
```

**Success looks like** (`X-Muster-Node` names the machine that answered):

```console
HTTP/1.1 200 OK
Content-Type: application/json
X-Muster-Node: desktop

{"id":"chatcmpl-345","object":"chat.completion","created":1792709007,"model":"qwen2.5:32b","system_fingerprint":"fp_ollama","choices":[{"index":0,"message":{"role":"assistant","content":"Hello there, nice to meet you."},"finish_reason":"stop"}],"usage":{"prompt_tokens":35,"completion_tokens":8,"total_tokens":43}}
```

The laptop doesn't have `qwen2.5:32b`, so Muster sent the request to the
desktop. Your app didn't need to know.

A model that no device has:

```console
$ curl localhost:7878/v1/chat/completions -d '{"model":"llama3.3:405b","messages":[]}'
{"error":{"message":"muster: model \"llama3.3:405b\" is not available on any node (available: llama3.2:3b, llama3.3:70b, qwen2.5:32b, qwen2.5:7b)","type":"muster_error"}}
```

A request from another device, without and with the token:

```console
$ curl -i http://192.168.1.20:7878/v1/models
HTTP/1.1 401 Unauthorized
Content-Type: application/json

{"error":{"message":"muster: cluster token required (Authorization: Bearer <token>); run `muster token` on any node","type":"muster_error"}}

$ curl -H "Authorization: Bearer $MUSTER_TOKEN" http://192.168.1.20:7878/v1/models
{"data":[{"id":"llama3.2:3b","object":"model","created":0,"owned_by":"muster"},{"id":"llama3.3:70b","object":"model","created":0,"owned_by":"muster"},{"id":"qwen2.5:32b","object":"model","created":0,"owned_by":"muster"},{"id":"qwen2.5:7b","object":"model","created":0,"owned_by":"muster"}],"object":"list"}
```

### How a request is routed

1. Only devices that have the requested model are considered (`llama3` = `llama3:latest`).
2. A device with the model **already loaded** goes first, avoiding a 5–60 s load.
3. Then the device with the fewest requests in flight.
4. Then the device with the most GPU memory (or RAM on Apple Silicon).
5. Then this device, to avoid a network hop.

If the chosen device is asleep or unreachable, the next one serves the request
and your app never sees an error. `pull`, `push`, `create`, `copy` and `delete`
always run on the device you send them to.

---

## 6. Run at login

```console
$ muster service install
```

**Success looks like (macOS):**

```console
Installed /Users/you/Library/LaunchAgents/dev.muster.agent.plist
Muster now starts at login. Logs: /Users/you/Library/Logs/muster.log
```

**Success looks like (Linux):**

```console
Installed /home/you/.config/systemd/user/muster.service
Muster now starts at login. Logs: journalctl --user -u muster -f
To keep it running while you're logged out (servers): loginctl enable-linger you
```

This needs no sudo. It restarts automatically if it crashes, and remembers
`OLLAMA_HOST` from when you installed it. Agent flags pass straight through,
for example `muster service install --peer desktop.your-tailnet.ts.net:7878`.
On a headless Linux server, also run the `loginctl enable-linger` line it
prints.

```console
$ muster service status
$ muster service uninstall
Removed. Muster will no longer start at login.
```

On a new device, run `muster join` **before** `muster service install`, so the
service starts with the right cluster. Windows doesn't have run-at-login yet;
add `muster agent` to Task Scheduler (At log on).

---

## Security

- **Ollama is never exposed to the network.** Only Muster listens, on TCP 7878
  (API) and UDP 7879 (discovery). `muster doctor` warns you if Ollama is set
  to `OLLAMA_HOST=0.0.0.0`.
- **Apps on this device** need no token. Websites can't reach Muster through
  your browser: cross-site and DNS-rebinding requests are rejected.
- **Apps on other devices** must send the cluster token as
  `Authorization: Bearer <token>`. Over plain `http://` the token travels
  unencrypted, so prefer Tailscale for those.
- **Between devices**, traffic is **TLS 1.3** and the token is never sent.
  Devices prove they hold it with signed requests, and each device's
  certificate is pinned through the cluster key, so a device on your Wi-Fi
  can't impersonate a Muster node or intercept prompts. Details:
  [ADR-0004](docs/adr/0004-peer-auth-hmac-never-send-token.md),
  [ADR-0005](docs/adr/0005-tls-between-agents-cluster-bound-pins.md).
- Device clocks must agree within 60 seconds (automatic time sync handles this).

## Good to know

- **Whole requests, not split models.** Each request runs on one device, so
  the largest model you can use is the largest one that fits on a single
  device. ([Why](docs/adr/0002-route-requests-not-split-models.md))
- **Routing doesn't yet account for speed.** A slow CPU-only device gets the
  same share of work as a fast GPU when both are idle. This is next on the
  [roadmap](docs/ROADMAP.md).

## Commands

```console
$ muster
muster — every machine you own, one AI endpoint.

Usage:
  muster agent   [flags]   run on every device
  muster status  [flags]   show the cluster from this device
  muster token   [flags]   print this cluster's token
  muster join    [flags]   save a cluster token (paste it when prompted)
  muster doctor  [flags]   find out why a device isn't working, with fixes
  muster service install [agent flags] | uninstall | status
                           run the agent at login (no sudo)
  muster version

Run "muster <command> -h" for flags.
```

Common `agent` flags:

```text
--listen :7878           API and peer port
--ollama URL             local Ollama (default $OLLAMA_HOST or 127.0.0.1:11434)
--name NAME              name shown in status and X-Muster-Node (default: hostname)
--peer HOST:PORT         add a device explicitly; repeatable
--no-discovery           turn off LAN broadcast discovery
```

## Uninstall

```console
$ muster service uninstall
$ rm "$(command -v muster)"
$ rm -rf ~/.config/muster                           # Linux
$ rm -rf ~/Library/Application\ Support/muster      # macOS
```

## Build from source

```console
$ git clone https://github.com/nitSubedi/muster && cd muster
$ make test     # race-enabled tests
$ make build    # ./bin/muster
$ make dist     # binaries for every platform in ./dist
```

## License

[Apache-2.0](LICENSE)
