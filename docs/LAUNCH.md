# Launch Plan

The code is the easy part. This is how Muster gets users, and how we find out
quickly whether it deserves more time.

## Positioning

**One line:** Every machine you own, one AI endpoint.

**For** developers who run local models and own more than one capable
machine, **who** are stuck pointing every app at one box by hand, **Muster**
is a single binary that makes all their machines answer as one
OpenAI/Ollama endpoint. **Unlike** exo or llama.cpp RPC, which split one model
across devices (slow over Wi-Fi), Muster routes each request to the best
whole machine, so you get native speed with zero app changes.

## Pre-launch checklist (gate: all done before posting anywhere)

- [ ] Public GitHub repo, license chosen (see Decisions below), CI green.
- [ ] Tagged `v0.1.0` release with binaries + `SHA256SUMS` (CI builds these).
- [ ] `curl -fsSL https://.../install.sh | sh` and a Homebrew tap. **Most users
      will leave if the first step is building from source.**
- [ ] 30-second terminal recording (asciinema or GIF) for the README: laptop
      `status` → chat request → `X-Muster-Node: desktop`.
- [ ] Tested on a real second machine (Linux + NVIDIA) on a real LAN *and*
      over Tailscale.
- [ ] Feedback channel: GitHub Discussions + a 3-question form ("how many
      machines, what hardware, what would make you pay").

## Launch sequence

| Day | Channel | Asset |
|---|---|---|
| 0 | r/LocalLLaMA | "I made my MacBook use my gaming PC's GPU with zero config": demo GIF and benchmark |
| 0 | Show HN | "Show HN: Muster – all your machines as one Ollama/OpenAI endpoint" |
| 1–3 | Reply to every comment and issue within hours | Turn objections into FAQ and roadmap items |
| 7 | Blog post | "Routing beats splitting: benchmarks of local inference across home machines" (laptop-only vs exo split vs Muster routing, tokens/s and time-to-first-token) |
| 14 | Integrations | Short guides PR'd or posted to Open WebUI, Continue and Aider communities |

The benchmark post does double duty: it is the strongest marketing asset
*and* the strongest portfolio piece for infrastructure roles.

## Kill / continue criteria (30 days after launch)

- **Continue and invest** if ≥ 300 stars *or* ≥ 50 users report running 2+
  nodes *or* ≥ 3 team/company inquiries.
- **Pivot the wedge** if stars come but nobody runs 2+ nodes. Interest without
  usage means install friction or the wrong problem. Interview 10 starrers.
- **Park it** if under 100 stars and no usage. Keep the repo and the benchmark
  post as portfolio, and move on.

## Decisions needed from the founder

1. **License.** Apache-2.0 maximises adoption and is standard for open-core.
   AGPL protects against a cloud vendor hosting it, but deters companies.
   *Recommendation: Apache-2.0 for the agent; Teams features in a separate,
   proprietary repo later.*
2. **Name and handle.** Check that `muster` is free on GitHub, Homebrew and a
   `.dev`/`.sh` domain before launch day. Rename now if it isn't; it's cheap
   today and expensive later.
3. **GitHub owner.** The module path is `github.com/maauri/muster`. Change it
   if publishing under a different account or org.
