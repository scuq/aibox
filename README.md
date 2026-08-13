# aibox

Controlled, disposable AI development environments for Claude Code and Codex —
a single static Go binary that owns the container lifecycle, the egress
allowlist, and a repository policy with **no git write path inside the
container**. Successor to `bclaude`; [docs/PLAN.md](docs/PLAN.md) is the
design and migration plan.

## Quick start — VS Code Dev Container (the simplest setup)

Everything you need to open a repository in VS Code, wired to the allowlisting
proxy and the read-only-git policy:

```bash
make install                      # build and install → ~/.local/bin/aibox
aibox doctor                      # check rootless podman is present
aibox image build                 # build the local image once

aibox net up                      # create the networks + start squid and the
                                  # relay, and leave them running
cd ~/git/my-project
aibox devcontainer here           # write .devcontainer/devcontainer.json for
                                  # this repo (scoped to the repo root)
```

Then in VS Code: **Dev Containers: Reopen in Container** (set
`dev.containers.dockerPath` to `podman`). A devcontainer is **always**
network-isolated — it joins only `aibox-internal` (no route out) with the squid
proxy, regardless of `egress.mode`, and its `initializeCommand` runs `aibox net
up` on every start, so the networks and squid come up automatically (aibox just
needs to be on your PATH). The `aibox net up` above is therefore optional but
harmless. Tear the shared sidecars down with `aibox net down`.

> `aibox net up` starts **squid** (the egress allowlist proxy) and, when your
> `.aibox.yaml` declares `services:`, the **relay**. Run it once; `aibox run`
> and the devcontainer both reuse whatever it left running.

## Quick start — one-off CLI session

No VS Code, no persistence — an ephemeral `--rm` session on the current
directory:

```bash
aibox run claude                  # interactive session on $PWD
aibox run claude -p "explain"     # assistant flags pass straight through
aibox run --egress proxy claude   # outbound traffic allowlisted and logged
aibox run shell                   # a shell in the same environment
```

## What the environment enforces

- **No git write path.** Inside the container `.git` is mounted read-only and a
  loud shim explains it to the agent: commits, pushes and checkouts are made by
  you, on the host, always. Read-only git (`status`, `diff`, `log`, `show`,
  `blame`) works normally. `aibox handoff` turns the agent's handoff notes into
  the exact git commands to run — printed, never executed.
- **Allowlisted egress.** In proxy mode the container has no route out except a
  squid sidecar that permits only allowlisted domains, plus a relay for named
  TCP services. A blocked connection is an ACL denial, not an outage.
- **Rootless + hardened.** `--userns=keep-id`, `cap-drop=ALL`,
  `no-new-privileges`, pids limit, a fresh nosuid `/tmp`. Claude and Codex never
  share auth or config volumes.
- **Environment notes.** The agent gets `ainotes` (also `/run/aibox/ainotes.md`)
  describing the tools, the network policy, and the write constraints, so it
  plans around them instead of discovering them by failure. aibox also creates
  a `CLAUDE.md` / `AGENTS.md` referencing the notes if the repo has none.

## Command surface

```
aibox doctor | status | config show | version

aibox image build [--ca-certs PATH] [--no-cache] [--dry-run]

aibox run claude|codex|shell [args…]      ephemeral session (--rm)
    -w DIR         workspace (default: $PWD)     --ro          read-only /work
    --egress MODE  open | proxy                  --allow-pkg   sudo apt inside
    --seed-creds   copy the host login in        --seed-config host Claude cfg
    --memory / --cpus / --no-limits             --dry-run     print podman argv

aibox net up | down | status              stand up/tear down the whole
                                          topology: aibox-internal (no route
                                          out) + aibox-egress networks, squid
                                          and relay started on both and left
                                          running

aibox egress start|stop|reload|status     squid, the domain allowlist
aibox egress allow <domain> | deny <domain>
aibox egress denied [--json]              what the allowlist blocked
aibox egress list                         the composed allowlist
aibox egress logs [-f] [--layer http|relay]

aibox relay start|stop|restart|status     the named-service relay (§8)
aibox relay list [--json]                 configured services and their ports
aibox relay test <service>                TCP probe from inside the network
aibox relay logs [-f]

aibox devcontainer here                   generate for the current git repo root
aibox devcontainer create [--assistant claude|codex|claude,codex|none]
                          [--fresh] [--force] [--dry-run]
aibox devcontainer list|status|stop|remove|recreate   label-scoped lifecycle

aibox ephemeral [shell | clear] [--json]  the /ephemeral scratch mount shared
                                          with the host (outside the git repo);
                                          bare form prints the host path for
                                          cd "$(aibox ephemeral)"

aibox notes [--size | --claude-md]        the environment notes
aibox notes project init                  scaffold .aibox/ainotes.md
aibox handoff [--diff | --clear]          print HANDOFF.md + the git commands
aibox init                                write a .aibox.yaml skeleton
```

Every inspection command supports `--json`; every run-shaped command supports
`--dry-run`, printing the exact `podman` invocation with no secret values.

## Configuration

`~/.config/aibox/config.yaml`, then `./.aibox.yaml`, then environment
(`AIBOX_*` only), then flags. `aibox config show` prints the resolved result.
A `.aibox.yaml` with named relay services:

```yaml
version: 1
egress:
  mode: proxy
  allowlist:
    - api.internal.example.com
services:
  - name: sw-nw0102-o71
    backend: 10.20.4.11:22
    aliases: [switch1]
  - name: netbox
    backend: netbox.example:443
```

Inside the container the agent reaches these by name (`ssh switch1`); the
listener port is the destination, so it cannot be redirected. `aibox relay
list` shows the mapping.

Install from source needs Go ≥ 1.25 and rootless [podman](https://podman.io);
`make` builds `./aibox`, `make install` copies it to `~/.local/bin`
(`PREFIX=/usr/local` for system-wide).
