# aibox — design and migration plan

> Successor to `bclaude`. A single static Go binary that owns the complete
> lifecycle of controlled, disposable AI development environments for Claude
> Code and Codex — standalone, persistent, or as VS Code Dev Containers.

Status: draft · Target first release: `v0.1.0`

---

## 1. Why rewrite

Not because `bclaude` is 1,856 lines of Bash. It is a working, hardened,
well-commented tool and the line count alone would not justify a rewrite.

The rewrite is justified by three things aibox adds that Bash makes miserable:

1. **Label-scoped lifecycle queries.** Finding "every container this project
   owns, in devcontainer mode, running or stopped" and acting on the result
   requires structured data, not `podman ps --format` plus `grep`.
2. **Persistent project state** across invocations, with schema versioning.
3. **Machine-readable output** on every inspection command, designed in rather
   than retrofitted.

Everything else — mount isolation, rootless Podman, volume separation, Squid
enforcement, Dev Container generation — is already proven in `bclaude` and must
be ported as *behaviour*, not rewritten from first principles.

### The primary risk

Roughly 40% of `bclaude`'s value is in its comments: the recorded reasons for
`updateRemoteUserUID: false`, the PEP 668 marker removal, the
`/etc/profile.d` PATH rewrite, `GOPATH` under `.cache`, setuid stripping, the
`mount_opts` helper. A clean Go rewrite loses all of that silently, and six
months later someone "simplifies" a flag back out.

**Mitigation: every non-obvious flag, mount, and env var in the Go port carries
the reasoning from the Bash original in a comment on the line that sets it.**
Code review rejects a hardening flag with no recorded rationale.

---

## 2. Scope of the rename

`bclaude` is "Claude in a container". `aibox` is a **reproducible AI development
workstation** with:

- selectable assistants (Claude Code, Codex, none)
- versioned, pinnable image profiles
- controlled and audited egress
- a repository *read* toolbox optimised for agent use
- **no repository write path** (§4)
- complete lifecycle ownership, including containers VS Code created

Explicit non-goals for v1: Docker support, remote workspaces, plugin systems,
automated VS Code launching, context-bundle generation.

---

## 3. Lifecycle model

The central conceptual fix. Three distinct classes of container, never conflated:

| Class | Command | Ownership | Persistence |
|---|---|---|---|
| Ephemeral | `aibox run …` | aibox | `--rm`, gone on exit |
| Persistent workspace | `aibox workspace …` | aibox | explicit start/stop/remove |
| Dev Container | `aibox devcontainer …` | VS Code creates, aibox manages | explicit, survives VS Code stop |

A normal CLI session is never silently persistent. A Dev Container is never
silently orphaned.

### Ownership is labels, not names

Every resource aibox creates carries:

```
io.aibox.managed=true
io.aibox.schema=1
io.aibox.project.id=<12-hex>
io.aibox.project.path=<canonical host path>
io.aibox.role=workspace|proxy|network|volume
io.aibox.mode=standalone|workspace|devcontainer
io.aibox.assistant=claude|codex|none
io.aibox.recipe=<16-hex>
```

`bclaude` labels volumes but not containers — that gap is exactly why VS Code
ends up owning half the lifecycle. Cleanup queries labels, never names, so
`aibox devcontainer remove` can never reach an unrelated container called
`chalk-dev`.

**Project ID:** `sha256(realpath(workspace))[0:12]`, e.g. `a81f728bc9ef`.
Container names are derived and deterministic:
`aibox-dc-<project-name>-<project-id>`.

State cache at `~/.local/state/aibox/projects/<id>/state.json` is for discovery
and speed only. **Runtime labels are the source of truth**; a missing or stale
state file must never change behaviour.

---

## 4. Git policy: readable history, no write path

**Requirement: no git write command works inside the container. Commits, adds,
and pushes are performed by the user on the host, manually, always.**

Read-only git is fully supported and expected: `status`, `diff`, `log`, `show`,
`blame`, `grep`, `rev-parse`, `ls-files`, `describe`, `shortlog`.

### Layer 1 — kernel enforcement (the actual mechanism)

```
--volume $WORKSPACE:/work
--volume $GIT_COMMON_DIR:/work/.git:ro
```

The workspace is read-write so the agent can edit files. `.git` is mounted
read-only *over* it. `git add` cannot write the index. `commit` cannot write
objects or refs. `push` cannot update remote-tracking refs. This is `EROFS`,
not advice.

### Layer 2 — the `git` shim (agent UX, not security)

`/usr/local/bin/git` precedes `/usr/bin/git` on PATH. Write verbs stop
**loudly**: non-zero exit, message written for an agent rather than a human.

```
aibox: `git commit` is not available in this container.

Repository history is mounted read-only. Commits are made by the user on
the host. Record what you changed and why in .aibox/HANDOFF.md, including a
suggested commit message. Read-only git (status, diff, log, show, blame)
works normally. See `ainotes`.
```

Loud, never silent-success. **Never fake success to an agent** — it will build
a multi-step plan on the lie and the failure surfaces somewhere far away.
The cost is noise in the transcript; the benefit is one clear stop instead of
three turns of `--force` escalation.

Denied verbs:

```
add commit push rm mv merge rebase reset revert checkout switch restore
stash cherry-pick am apply tag branch (-d/-D/-m/-M) remote (add/set-url/remove)
config (--global/--system) worktree gc prune fetch pull clone submodule
filter-repo notes update-ref symbolic-ref hook update-index
```

Everything not on the list passes through unmodified. The shim must handle
`git -C DIR <verb>` and `git --git-dir=… <verb>` — the verb is not always
`$1`. Table-tested.

### Layer 3 — no identity, no credentials

`bclaude` mounts `~/.gitconfig` read-only so `git commit` inside knows who you
are. **That requirement is now gone; the mount is removed.** aibox generates a
minimal config instead:

```ini
[safe]
    directory = /work
[gc]
    auto = 0
[core]
    fsmonitor = false
[credential]
    helper =
```

No user identity, no credential helper, no SSH agent forwarding, no
`~/.git-credentials`. In `--egress proxy` mode, SSH cannot leave the internal
network at all.

**Honest residual gap:** in `--egress open` mode, an agent that obtains a token
from the environment could still reach `github.com` over HTTPS. Layer 1 stops it
writing local refs, but not the network call itself. If that matters, run proxy
mode and drop `github.com` from the allowlist. This is documented, not hidden.

### Layer 4 — tell the agent first

The git constraint is the opening section of `.ainotes` (§5), so the assistant
plans around it rather than discovering it by failure.

### Scar tissue — the parts that will bite

- **`GIT_OPTIONAL_LOCKS=0` must be in the image ENV.** Without it `git status`
  tries to refresh and write the index, hits the read-only mount, and produces
  errors that send the agent down a rabbit hole. This is the single most
  important line in the feature.
- **`.git` may be a file, not a directory.** Submodules and linked worktrees use
  a gitfile pointing elsewhere. Bind-mounting that file read-only appears to
  work while the real gitdir sits outside the workspace, unmounted, and git
  fails confusingly. Resolve `git rev-parse --git-common-dir` on the host and
  mount *that*; if it lies outside the workspace, mount it read-only at its own
  path as well, or refuse with a clear message.
- **The workspace may not be the repo root.** No `.git` to mount; git inside
  walks up to nothing. Detect, warn once, continue — history is simply
  unavailable.
- **Ownership.** `keep-id:uid=1000` normally lines ownership up, but
  `safe.directory = /work` costs nothing and prevents a dubious-ownership
  refusal on unusual host configurations.
- **`stash`, `restore`, `worktree` are collateral damage.** Losing
  `git restore -- file` is a mild inconvenience. Stated explicitly in the notes
  so the agent does not reach for them.
- **Hooks.** `core.hooksPath` is not settable from inside (read-only config
  path); nothing runs hooks anyway without a write verb.

### The handoff convention

Since the container cannot commit, it hands you something you can act on.
`/work/.aibox/` is writable and belongs in `.gitignore`
(`aibox repo gitignore suggest` adds it):

```
.aibox/HANDOFF.md      what changed, why, suggested commit message(s)
.aibox/changed.txt     paths, from `git status --porcelain`
.aibox/review.patch    `git diff` output for a quick skim
```

Host side:

```
aibox handoff            print HANDOFF.md, then the exact git commands to run
aibox handoff --diff     show review.patch through delta
aibox handoff --clear    remove .aibox/ after committing
```

`aibox handoff` **prints** commands. It never runs them. There is no
`--yes`, and there will not be one.

### Configuration

```yaml
git:
  history: read-only     # read-only | none
  identity: false        # never mount host gitconfig
  shim: loud             # loud | off  (off is for debugging the shim itself)
  handoff: .aibox
```

`history: none` omits the `.git` mount entirely — for review sessions where the
agent should not read branch names or commit messages at all.

---

## 5. `.ainotes` — the environment briefing

A short, generated document telling the assistant what this environment is,
which tools to prefer, and what it must not do. Referenceable from `CLAUDE.md`,
`AGENTS.md`, or a system prompt.

### Rules

1. **Generated, never hand-maintained.** It derives from the same source of
   truth as `/usr/share/aibox/image-manifest.json`, so the versions in the notes
   cannot drift from the versions in the image. Hand-written notes rot in about
   two releases.
2. **2 KB hard cap.** This lands in the agent's context every single session; it
   is a recurring token cost, not free documentation. Enforced in the generator
   — exceeding it fails the image build. `aibox notes --size` reports current
   usage.
3. **Not in `/work`.** It is environment metadata, not project content, and
   should not need gitignoring.

### Location

```
/run/aibox/ainotes.md        canonical, generated at container start
~/.ainotes                   symlink to it
ainotes                      command on PATH that prints it
```

### Layers, concatenated at start

| Layer | Source | Content |
|---|---|---|
| Image | image manifest | profile, toolchains, tool inventory with invocations |
| Policy | run-time config | git constraint, egress mode, writable paths, caps |
| Project | `.aibox/ainotes.md` in repo | your own additions, committed with the repo |

Project layer last, so a repo can add conventions but not delete the git policy.

### Content sketch

```markdown
# aibox environment notes

## Repository writes — read this first
Do NOT use git write commands (add, commit, push, checkout, reset, stash…).
`.git` is mounted read-only; the user commits on the host. Record intended
changes in .aibox/HANDOFF.md with a suggested commit message. Read-only git
(status, diff, log, show, blame, grep) works normally.

## Prefer these over the defaults
rg PATTERN                    instead of grep -r   (respects .gitignore)
fd NAME                       instead of find
ast-grep -p 'PATTERN' --lang go
                              structural search — use for functions and types
                              rather than regex over source
tokei                         language and line breakdown
bat -p FILE                   file contents
aibox repo symbol NAME        definitions and references, LSP-backed

## Network
Egress is allowlisted through a proxy. A connection failure is an ACL denial,
not an outage — report the domain instead of retrying. Currently reachable:
api.anthropic.com, proxy.golang.org, pypi.org, registry.npmjs.org,
github.com (read only).

## Toolchain
go 1.23.12 in /usr/local/go · gopls, staticcheck, dlv, goimports installed
Caches under ~/.cache persist across runs; re-downloading is unnecessary.

## Writable paths
/work (except /work/.git) · /work/.aibox · /tmp · ~/.cache · ~/.claude
```

### Commands

```
aibox notes                    render and print (host)
aibox notes --json
aibox notes --size             report byte usage against the 2 KB cap
aibox notes --claude-md        emit the CLAUDE.md include snippet
aibox notes --project init     scaffold .aibox/ainotes.md
```

### CLAUDE.md wiring

A committed `CLAUDE.md` referencing an aibox-only path is a dangling reference
when the repo is opened elsewhere. The emitted snippet is therefore conditional
in wording and degrades to a no-op:

```markdown
## Environment
If `/run/aibox/ainotes.md` exists, read it before starting work — it describes
the available tools, the network policy, and the repository write constraints
for this container. Outside that container, ignore this section.
```

---

## 6. Assistants

`aibox devcontainer create` does **not** implicitly mean Claude, Codex, or both.
The Dev Container is the environment; assistants are selectable components.

```
aibox devcontainer create --assistant claude          (default: one)
aibox devcontainer create --assistant codex
aibox devcontainer create --assistant claude,codex    (explicit opt-in, warns)
aibox devcontainer create --assistant none
```

`kodex` is accepted as an input alias and normalised to `codex` everywhere in
configuration, labels, and image metadata.

**Why "both" warns:** enabling both unions the egress allowlists, so one
container can reach `api.anthropic.com` *and* `api.openai.com` while holding
both credential sets. Two exfiltration channels, one blast radius. Same
treatment as `--allow-pkg`: available, explicit, and it tells you what it just
relaxed.

Storage is never shared between assistants:

```
aibox-auth-claude                    login, shared across projects
aibox-auth-codex
aibox-config-claude-<project-id>     per-project config
aibox-config-codex-<project-id>
aibox-cache-shared                   toolchain caches, nothing precious
```

### Tool interface

```go
type Assistant interface {
    Name() string
    Executable() string

    ConfigMounts(cfg Config) []Mount
    Environment(cfg Config) []EnvVar
    Arguments(args []string) []string

    RequiredDomains() []string
    AuthProtocol() AuthProtocol   // see §7
    Version(ctx context.Context, r Runtime) (string, error)
}
```

Implementations: `internal/assistant/claude`, `.../codex`, `.../shell`.
Adding `aider` or `opencode` later must not require touching lifecycle code.

---

## 7. Behaviour that must be ported, not reinvented

Five pieces of `bclaude` are load-bearing and subtle. Each gets unit tests
**transcribed from the existing Bash test suite before any Go is written.**

### 7.1 The credential hand-off protocol

The most subtle logic in the codebase. `bclaude`'s entrypoint captures
`AUTH_CREDS_IN` — the auth volume's fingerprint *before* anything is copied in —
and every decision on the way out compares against it:

| Condition | Action |
|---|---|
| config dir ends equal to `AUTH_CREDS_IN` | nothing of ours to add; leave the volume alone |
| volume no longer equals `AUTH_CREDS_IN` | a concurrent session refreshed and exited first; its token is newer, it stands |
| otherwise | publish |

Rule 1 stops an idle session writing a spent token over a live one. The case it
must not swallow: a config dir holding the *only* copy of a login (a volume from
before the auth/config split, or a fresh `--auth-volume` alongside an existing
config volume) has to reach an empty auth volume. Fingerprinting the *volume*
rather than the config dir is what separates those two cases.

**Specification:** the `login hand-off between overlapping sessions` and
`shared auth volume` sections of `tests/run-tests.sh`. Port as a table test
first. Two assistants means two independent instances of this protocol.

Fingerprinting must return empty for a missing file and never non-zero — it runs
under `set -e` inside an assignment in the shell version, and the Go port must
preserve the same total-function behaviour.

### 7.2 Recipe-hash staleness

`bclaude` stamps `org.bclaude.recipe` = `sha256(containerfile + entrypoint +
claude version)[0:16]` on the image and rebuilds when it no longer matches.

`.aibox.lock` and published digests solve *reproducibility*. The recipe hash
solves a **different** problem: "the binary on your PATH changed, your local
image did not." Keep both. `io.aibox.recipe` must be checked at run time, not
merely recorded — a label nobody reads is decoration.

### 7.3 The Dev Container five

Generated `devcontainer.json` must always contain all five, together:

```jsonc
"overrideCommand": true,
"containerUser": "aibox",
"remoteUser": "aibox",
"updateRemoteUserUID": false,
"runArgs": ["--userns=keep-id:uid=1000,gid=1000", …]
```

Without them, Dev Containers derives its own `localhost/vsc-<project>-<hash>-uid`
image purely to rewrite the container user's uid — which is redundant against
the keep-id mapping, fights it (volumes are owned by 1000), and inserts a second
cached image between you and `aibox image build`.

Enforced by golden-file tests, which also assert the read-only `.git` mount and
the `/vscode` mountpoint ownership.

### 7.4 Mount rendering

`bclaude`'s `mount_opts` exists because naively appending `,z` to an option-less
mount yields `/work,z`, which Podman reads as the *destination path*: the
mountpoint becomes `/work,z` and `/work` stays empty, silently.

In Go: a typed `Mount` struct with exactly one renderer, and that renderer is
**the only path to a `--volume` argument anywhere in the codebase**. Templates
in `assets/` must not contain literal mount strings; that is how this bug comes
back. Lint rule or review check.

### 7.5 Proxy address derivation

The proxy's address is `.2` in `EGRESS_SUBNET`, derived arithmetically — *not*
resolved by container name — because container-name resolution on internal
networks varies across Podman versions. Preserve exactly.

Also preserve: the internal network is the enforcement; the proxy environment
variables merely tell tools where the one door is. Both upper- and lower-case
forms (`HTTPS_PROXY` and `https_proxy`, etc.) are set, because `curl` reads one
and most CLIs read the other.

---

## 8. Egress — squid, plus a service relay

Two sidecars on the internal network, both application proxies. Neither needs
the workload to have a route out, which is what keeps the topology simple:
`--internal` network, `cap-drop=ALL`, no `NET_ADMIN`, no shared netns, no
routing. The workload can only reach what it can address, and on an internal
network that is the two sidecars.

```
                     internal network (no route out)
   ┌──────────────┐        10.199.0.0/24
   │   workload   │──── .2  squid   ── HTTP/HTTPS, domain allowlist ──┐
   │ cap-drop=ALL │                                                    ├─→ out
   │              │──── .3  relay   ── named TCP services ────────────┘
   └──────────────┘
```

`bclaude`'s squid design is kept as-is. The relay is the new part.

### 8.0 ACL composition (squid, unchanged from the original plan)

```
~/.config/aibox/egress/
├── base.acl                 always applied
├── claude.acl               per assistant
├── codex.acl
├── project-<id>.acl         per project
└── generated.acl            NEVER edited by hand
```

Reload: `base + enabled assistants + project + CLI additions → normalise →
validate → deduplicate → generated.acl → squid -k parse (gate) → squid -k
reconfigure`. The active configuration is replaced only after parse succeeds.
User-facing domain syntax is abstracted (`example.com` vs `.example.com`);
nobody should need to know Squid's `dstdomain` semantics.

### 8.1 Why a relay and not a jump host

An SSH jump host takes the destination as a parameter: `ssh -J relay
user@switch1`. That has two costs.

- The relay must police a client-supplied destination against a list. Policy
  enforcement, with the list as the only thing between the agent and the rest
  of the network.
- The container must authenticate to the relay, which means SSH key material
  inside the container — precisely what §4 layer 3 spent its effort removing.

A static-port relay takes no destination parameter at all:

```
ssh -p 2204 relay      →  relay forwards to 10.20.4.11:22
```

The listener port is the destination. There is no field the agent can put a
different host into. That is structural enforcement rather than policy, and it
needs no authentication to the relay.

Three further properties fall out of it:

- **End-to-end SSH survives.** The relay is a byte pipe, so the switch's real
  host key reaches the client and host-key verification still works. A
  terminating jump host would break that.
- **Not SSH-specific.** The same mechanism serves NETCONF/830, a device HTTPS
  API, Postgres, a syslog collector — anything TCP.
- **The mental model changes.** The container is granted named services, not
  network access. It can reach `sw-nw0102-o71`, not `10.20.0.0/16`.

### 8.2 Service definitions

```yaml
services:
  - name: sw-nw0102-o71
    backend: 10.20.4.11:22
    listen: 2204                 # optional; auto-allocated from 2200 if omitted
    aliases: [switch1]

  - name: netbox
    backend: netbox.kagesintern.at:443

  - name: ise-ers
    backend: ise01.kagesintern.at:9060
    maxConns: 2
    idleTimeout: 300s
```

Each entry becomes one listener on the relay. Nothing else is reachable
through it — no wildcard backend, no dynamic destination, no CONNECT verb.
Ports are allocated in declaration order from a base, with an explicit
`listen:` override. Reordering the list therefore reshuffles ports, which
matters less than it looks because of §8.4.

### 8.3 Generated client wiring

The agent should not have to know port numbers. aibox generates, into the
container at start, `~/.ssh/config` (read-only):

```
Host sw-nw0102-o71 switch1
    HostName        aibox-relay
    Port            2204
    HostKeyAlias    sw-nw0102-o71
    User            %r
```

and `/run/aibox/services.json` — the machine-readable inventory, and the
source for the services section of `.ainotes` (§5), so the agent is told what
exists rather than probing for it:

```json
{"name":"sw-nw0102-o71","proto":"ssh","address":"aibox-relay:2204",
 "aliases":["switch1"],"backend_disclosed":false}
```

`backend_disclosed` controls whether the real address appears. Default false:
the agent gets a name and a way to reach it, not a map of your network.

### 8.4 HostKeyAlias is not optional

Without it, SSH keys `known_hosts` entries by `[aibox-relay]:2204`. Two
consequences, both bad: reorder the service list, ports shift, and every entry
mismatches — which looks exactly like a MITM warning and trains the agent (and
you) to ignore them; and two services on the same relay are indistinguishable
in `known_hosts`. `HostKeyAlias sw-nw0102-o71` keys the entry to device
identity instead, so host keys survive port churn and stay meaningful. This is
the single detail most likely to be omitted and most annoying to debug
afterwards.

### 8.5 Implementation: haproxy

haproxy in TCP mode, one `listen` section per service. Not socat (no useful
logging), and not a bespoke Go relay — a relay needs connection limits, idle
and connect timeouts, backend health checks, and structured logging, all of
which haproxy already has and a hand-written one would have to reimplement.

```
listen svc_sw-nw0102-o71
    bind 0.0.0.0:2204
    mode tcp
    option tcplog
    timeout connect 5s
    timeout client  1h
    timeout server  1h
    maxconn 4
    server backend 10.20.4.11:22 check
```

Log to stdout → `podman logs` → `aibox relay logs`, matching squid's existing
treatment.

### 8.6 UDP is second-class, and says so

haproxy does not do general UDP relaying, and UDP has no connections to relay.
A `socat UDP4-RECVFROM:…,fork UDP4-SENDTO:…` listener works for simple
request/response traffic — SNMP polling, syslog shipping — with real caveats:
no sessions (per-flow logging is approximate, byte accounting crude), naive
source-port tracking (concurrent clients interleave), and **TFTP does not
work** (it negotiates an ephemeral server port on the first reply, which a
fixed-port relay cannot follow — named explicitly because firmware and config
pushes are the obvious thing to try). UDP services are therefore opt-in per
service (`proto: udp`), documented best-effort, and left out of the default.

### 8.7 Credentials — what the relay does not solve

The relay gets the agent to the device. It does nothing about logging in, and
should not pretend otherwise. Three positions, in increasing order of
preference:

1. **No credentials.** For services that authenticate some other way, or for
   reachability testing. Honest, limited.
2. **Explicit injection**, opt-in per service, sourced from OpenBao at
   container start into an env var or a 0400 file. The agent can read it — and
   therefore, in principle, exfiltrate it. The narrow egress allowlist is what
   bounds that, not the injection mechanism.
3. **Point the backend at a PAM** instead of at the device (`backend:
   fudo.example:22`). The device credential never leaves the PAM, session
   recording and command-level audit come from the system built to do it, and
   aibox's relay becomes purely a reachability control.

Option 3 is the right long-term shape and costs aibox nothing to support — it
is just a backend address. v1 ships options 1 and 2; the docs recommend 3.

### 8.8 Logging

Connection-level, and honest about its limit: the relay logs that a session
happened, not what was done inside it. SSH is encrypted end-to-end by design —
command-level audit is a PAM's job (§8.7).

```json
{"ts":"…","layer":"relay","event":"open","service":"sw-nw0102-o71","conn":"c7f2"}
{"ts":"…","layer":"relay","event":"close","conn":"c7f2","duration":"6m42s","bytes_up":8421,"bytes_down":193744}
{"ts":"…","layer":"relay","event":"refused","port":2209,"reason":"no-service"}
```

`aibox egress logs` merges squid's access log and the relay's stream in
timestamp order, so one command answers "what did this session touch."

### 8.9 The git-policy interaction

A real regression risk against §4. `bclaude` has no outbound SSH at all. With
the relay, SSH exists — to configured backends only. If someone configures a
service whose backend is a git remote, `git push` becomes possible at the
network layer, and layer 1 (the read-only `.git` mount) is then the only thing
left standing. So: at `aibox run` / `devcontainer create`, compare configured
service backends against the workspace's git remotes (`git remote -v`,
read-only, works fine) and warn loudly on a match, naming both. Not a refusal —
there are legitimate reasons to reach a host that also serves git — but never
silent.

### 8.10 Commands

```
aibox net up | down | status          the whole topology as a unit:
                                      aibox-internal (--internal, no route out)
                                      + aibox-egress networks, squid and relay
                                      started on both and left running so every
                                      run/devcontainer reuses them

aibox relay start | stop | restart | status
aibox relay list [--json]              configured services and listener ports
aibox relay test <service>             TCP connect probe from inside the network
aibox relay logs [-f]

aibox egress start | stop | status | reload      squid, unchanged
aibox egress allow <domain> | deny <domain>
aibox egress denied [--json]
aibox egress logs [-f] [--layer http|relay]
```

`aibox relay test` answers "policy or upstream?" from the workload's own
vantage point, which is where every reachability incident starts.

### 8.11 Security properties, stated plainly

**Holds:** the destination is not client-controllable (the port is the
destination); no credentials are needed to reach the relay; SSH and TLS remain
end-to-end (host keys and certificates verify normally); blast radius is
exactly N named endpoints, enumerable with `aibox relay list`; the workload is
on an internal network with no route out.

**Does not hold:** full protocol access to a configured backend is complete
device access if credentials exist — reachability control is not
authorization, the device's own AAA still has to be right; no content
inspection (encrypted end-to-end, by design); `maxConns` is not rate limiting
(an agent can open, close, and reopen).

### 8.12 Preserved from bclaude

Sidecar addresses derived arithmetically from the subnet (squid `.2`, relay
`.3`), never by container name — name resolution on internal networks varies
across Podman versions. Both cases of the proxy env vars. The internal network
is the enforcement; the proxy variables only tell tools where the door is.
`squid -k parse` gates every reload; the relay gets the equivalent, `haproxy
-c -f`, before any restart. haproxy has no live reconfigure as clean as
squid's, so a service change is a restart — which drops open sessions, and the
command says so before doing it.

---

## 9. Images

Two deliverables: the static `aibox` host binary, and published container
images. The binary still embeds the recipe (`//go:embed assets/*`) so it can
build locally — that is the escape hatch, and it carries real weight (§9.3).

### 9.1 Profiles

Layered, one recipe with build args:

```
minimal → base → { go, python, rust } → polyglot (local build only)
```

- **minimal** — git, bash, curl, ca-certificates, jq, yq, ripgrep, fd, fzf, bat,
  eza, tree, delta, tokei, ast-grep, less, openssh-client, unzip. No compilers.
- **base** — plus make, cmake, pkg-config, build-essential, strace, lsof,
  procps, iproute2, dnsutils, socat, shellcheck, shfmt, hyperfine, hexyl.
- **go** — pinned upstream toolchain (checksummed, per-arch, fails closed on
  unpinned arch), gopls, staticcheck, golangci-lint, goimports, gofumpt,
  gotestsum, govulncheck, dlv.
- **python** — python + headers, uv, ruff, mypy, pyright, pytest, coverage,
  debugpy, ipython. Tooling in `/opt/pytools` venv with only entry points
  symlinked — never `python`/`pip` — so it cannot collide with a project's
  requirements. PEP 668 marker removed (the container is disposable and `/usr`
  is unwritable without `--allow-pkg`).
- **rust** — rustup, rustfmt, clippy, rust-analyzer, cargo-nextest, cargo-audit,
  cargo-deny, bacon.

`polyglot` is a convenience build, never the default, never published.

Note: `xsv` is unmaintained. Use `qsv` or `xan` if a CSV tool is wanted.

### 9.2 Tags and the immutability caveat

```
aibox:go1.23.12-2026.07.1     immutable exact — never moved
aibox:go1.23                  series alias — may advance patch
aibox:go                      convenience alias — may change minor
aibox:latest                  convenience — never for reproducible projects
```

**The caveat, stated plainly:** layered inheritance and per-patch immutable tags
are in tension. Rebuild `base` for a glibc CVE and either every child tag is
re-cut (breaking the immutability promise for the lower layers) or children keep
a vulnerable base. Therefore:

- publish at **release granularity** (`2026.07.1`), one set of profile tags
- do **not** attempt a tag per toolchain patch across every profile
- treat a base rebuild as a new release number, not a silent re-push
- record digests in `.aibox.lock`, and prefer digests over tags for anything
  long-lived

Two arches: `linux/amd64`, `linux/arm64`, multi-arch manifest.

### 9.3 The long tail

Historic and unpublished combinations are served by local builds, not by an
ever-growing published matrix:

```
aibox image build --profile go --toolchain go=1.22.12 \
                  --tag local/aibox:go1.22.12
```

This is the primitive that keeps the published matrix small. Lean on it.

### 9.4 Image manifest

`/usr/share/aibox/image-manifest.json`, the single source of truth for both
`aibox image tools` and the generated `.ainotes`:

```json
{
  "schemaVersion": 1,
  "image":     { "profile": "go", "release": "2026.07.1" },
  "toolchains": { "go": "1.23.12" },
  "assistants": { "claude": "1.0.72", "codex": "0.28.0" },
  "tools":      { "ripgrep": "14.1.1", "fd": "10.2.0", "ast-grep": "0.38.5" }
}
```

```
aibox image tools [--json]
aibox image compare IMAGE_A IMAGE_B
```

---

## 10. Configuration

Merge order, with `aibox config show` printing the resolved result:

```
compiled defaults → ~/.config/aibox/config.yaml → ./.aibox.yaml → env → flags
```

```yaml
version: 1

project:
  name: chalk

image:
  profile: go
  release: "2026.07.1"
  toolchains:
    go: "1.23.12"

assistants:
  claude: { enabled: true }
  codex:  { enabled: false }

runtime:
  engine: podman
  workspace: .
  workspaceMode: read-write
  cpus: 4
  memory: 6GiB

git:
  history: read-only
  identity: false
  shim: loud
  handoff: .aibox

security:
  allowPackageInstall: false
  noNewPrivileges: true
  dropCapabilities: true
  pidsLimit: 2048

egress:
  mode: proxy
  subnet: 10.199.0.0/24
  allowlist:
    - api.anthropic.com
    - proxy.golang.org
    - sum.golang.org
    - github.com

devcontainer:
  name: chalk-aibox
  removeOnStop: false
  vscode:
    extensions: [golang.go]

notes:
  project: .aibox/ainotes.md
  maxBytes: 2048

repo:
  tree: { maxDepth: 5, respectGitignore: true }
```

`.aibox.lock` records what `.aibox.yaml` resolved to:

```yaml
version: 1
image:
  reference: ghcr.io/scuq/aibox:go1.23.12-2026.07.1
  digest: sha256:8c2a…
components:
  claude: "1.0.72"
  go: "1.23.12"
  ripgrep: "14.1.1"
generated:
  aibox: "0.1.0"
```

Intent lives in `.aibox.yaml`; resolved reality lives in `.aibox.lock`.
`aibox lock verify` fails loudly on drift.

---

## 11. Command surface (v1)

```
aibox init | doctor | status | version | config show | completion

aibox run claude|codex|shell [args…]           ephemeral, --rm
aibox workspace start|exec|stop|remove         persistent, aibox-owned

aibox devcontainer create|start|stop|remove|recreate|list|status

aibox image build|pull|list|inspect|tools|compare|prune
aibox lock | lock update | lock verify

aibox egress start|stop|reload|allow|deny|list|denied|logs

aibox notes | handoff
aibox repo overview|tree|file|symbol|gitignore
aibox volume list|prune|remove
```

Every inspection command supports `--json`. Every run-shaped command supports
`--dry-run`, printing the exact `podman` invocation with no secret values.

**Cut from v1** (revisit only after the lifecycle is proven): `repo context`
— a token-aware context bundler is a product of its own and competes with what
Claude Code and Codex already do natively via agentic search; `repo health`,
`repo secrets`, `repo duplicates` — ship gitleaks, jscpd, and tokei in the image
rather than wrapping them; Docker runtime; automated VS Code launching.

---

## 12. Package layout

```
aibox/
├── cmd/aibox/main.go
├── internal/
│   ├── app/          command wiring
│   ├── config/       load, merge, validate, schema version
│   ├── runtime/      Runtime interface + podman CLI impl + fake recorder
│   ├── container/    spec, mounts (single renderer), labels, lifecycle
│   ├── git/          gitdir resolution, policy, shim generation, handoff
│   ├── notes/        ainotes generation + size budget
│   ├── devcontainer/ model, generate, lifecycle
│   ├── egress/       manager, network, squid, acl, logs
│   ├── image/        builder, embedded assets, manifest, compare
│   ├── project/      identity, metadata, discovery
│   ├── assistant/    claude, codex, shell
│   ├── repo/         overview, tree, file, symbol, gitignore
│   ├── doctor/
│   └── output/       human, json
├── assets/
│   ├── Containerfile
│   ├── entrypoint.sh
│   ├── git-shim.sh
│   ├── squid.conf.tmpl
│   ├── ainotes/
│   └── allowlists/{base,claude,codex}.txt
├── tests/{integration,golden,fixtures}
├── .goreleaser.yaml
└── Makefile
```

### Runtime abstraction

Podman **CLI**, not the REST API, for v1: it matches proven behaviour, rootless
support is straightforward, there is no daemon or socket discovery, and
`--dry-run` can print exactly what will run. The REST API can come later behind
the same interface.

```go
type Runtime interface {
    Run(context.Context, ContainerSpec) error
    Create(context.Context, ContainerSpec) (Container, error)
    Start(context.Context, string) error
    Stop(context.Context, string, StopOptions) error
    Remove(context.Context, string, RemoveOptions) error
    List(context.Context, Filter) ([]Container, error)
    Exec(context.Context, string, []string) (ExecResult, error)
}
```

---

## 13. Testing

The existing 834-line Bash suite is the **compatibility specification**. Its
section names map directly to Go test files:

| Bash section | Go |
|---|---|
| `run invocation (--dry-run)` | `container/spec_test.go` golden args |
| `egress filtering (--dry-run)` | `egress/*_test.go` |
| `devcontainer` | `devcontainer/generate_test.go` golden files |
| `SELinux relabelling` | `container/mounts_test.go` |
| `userns mapping` | `container/spec_test.go` |
| `secret handling` | `container/spec_test.go` (argv never carries values) |
| `generic env vars are not inherited` | `config/loader_test.go` |
| `shared auth volume` | `assistant/auth_test.go` |
| `login hand-off between overlapping sessions` | `assistant/auth_test.go` — **write first** |
| `shared cache volume` | `container/mounts_test.go` |
| `credential seeding` | `assistant/auth_test.go` |
| `per-project config volumes` | `project/identity_test.go` |
| `rootless requirement` | `doctor/*_test.go` |
| `build arguments (--dry-run)` | `image/builder_test.go` |

Three layers:

1. **Unit** — no Podman. Config merge, project ID, labels, ACL normalisation,
   mount rendering, devcontainer JSON, git shim verb parsing, notes size budget,
   auth protocol.
2. **Fake runtime** — a recording `Runtime` asserting call *sequences*: e.g.
   `devcontainer remove` must list by labels, stop matching, remove matching,
   remove anonymous volumes, and **preserve named auth/config/cache volumes**.
3. **Integration** — rootless Podman in CI. Build, run, write a file and verify
   host ownership, `git status` works, `git commit` fails loudly, `.git` is
   read-only, proxy allows an allowlisted host and denies another, reload
   validates, generate a devcontainer config, simulate a VS Code-created
   labelled container and remove it through aibox.

New non-negotiable checks: **`git add`/`commit`/`push` fail**; `git status`
succeeds with no index-write errors (the `GIT_OPTIONAL_LOCKS` regression);
`.ainotes` is under budget and contains the git policy; gitfile/submodule
workspaces resolve correctly.

CI keeps `shellcheck` for the remaining embedded shell (entrypoint, git shim)
and adds `go vet`, `staticcheck`, `gofmt -l`, warnings as errors.

---

## 14. Phases

Each phase ends green: builds clean, tests pass, `doctor` honest.

### 01 — Foundation (no containers)
Config load and merge, `config show`, project identity, label constants, typed
`Mount` + the single renderer, `Runtime` interface + fake recorder, human/JSON
output, `doctor`, `version`. Tests only.

### 02 — Standalone run, with egress and git policy from day one
`image build`, `run claude|codex|shell`, `--dry-run`. Internal network and Squid
land **here, not later** — the port must never be less safe than the script it
replaces. Git read-only mount, `git-common-dir` resolution, `GIT_OPTIONAL_LOCKS=0`,
loud git shim, generated gitconfig, no host gitconfig mount. Credential
hand-off protocol, tests transcribed first. At the end of this phase aibox
replaces `bclaude`'s main execution path.

### 03 — Notes and handoff
Image manifest, notes generator with enforced budget, `ainotes` in-container,
`aibox notes`, `--claude-md`, `aibox handoff`.

### 04 — Egress hardening
ACL layering, `squid -k parse` gate, `allow`/`deny`/`denied`/`logs`,
per-assistant fragments, optional UDP/DNS blocking.

### 05 — Devcontainer generation
`devcontainer create` with `--assistant`, golden-file tests for the Dev
Container five plus the read-only `.git` mount, `/vscode` ownership,
`initializeCommand` freshness carried over from `--fresh`.

### 06 — Devcontainer lifecycle
Label-scoped `stop`/`remove`/`recreate`/`list`/`status`. This is the feature
missing from plain VS Code and the reason the rewrite exists.

### 07 — Repo commands
`overview`, `tree`, `file`, `symbol` (LSP → ast-grep → ctags → ripgrep
fallback), `gitignore suggest/apply` — which must never silently ignore an
already-tracked file, and which adds `.aibox/`.

### 08 — Lock and images
`.aibox.lock`, digest pinning, `image tools`/`compare`, published multi-arch
images, retention policy.

### 09 — Release
GoReleaser (`CGO_ENABLED=0`, `-trimpath`, `-s -w`), linux/darwin × amd64/arm64,
checksums, SBOM, signatures, install script, and a `bclaude` compatibility
wrapper that prints migration guidance rather than translating silently.

---

## 15. Decisions taken

| # | Decision |
|---|---|
| 1 | Go binary drives the Podman **CLI**, not the REST API |
| 2 | Podman only for v1; `Runtime` interface exists, Docker does not |
| 3 | Labels are the authoritative ownership mechanism; state files are a cache |
| 4 | `run` is disposable; `workspace` and `devcontainer` are explicit objects |
| 5 | Claude and Codex never share auth or config storage |
| 6 | **No git write path inside the container, ever — enforced by a read-only `.git` mount, surfaced by a loud shim** |
| 7 | Host `~/.gitconfig` is no longer mounted; no identity, no credential helper |
| 8 | `aibox handoff` prints git commands and never executes them |
| 9 | `.ainotes` is generated from the image manifest and capped at 2 KB |
| 10 | Generated ACLs are never hand-edited; `squid -k parse` gates every reload |
| 11 | Recipe hash **and** lock file both exist — different problems |
| 12 | No secrets in argv, state files, logs, or generated devcontainer JSON |
| 13 | JSON output on every inspection command from the first release |
| 14 | Backward compatibility is behavioural; the Bash test suite is the spec |
| 15 | Published image matrix stays small; local builds serve the long tail |
| 16 | Never fake success to an agent — failures are loud and explained |

---

## 16. Architecture

```
                     ┌────────────────────┐
                     │    aibox binary    │
                     └─────────┬──────────┘
                               │
        ┌──────────┬───────────┼───────────┬──────────┐
        │          │           │           │          │
   assistants   git policy  lifecycle    egress     notes
        │          │           │           │          │
   claude/codex  ro .git    podman      squid+ACL  manifest
                 + shim     runtime     internal      │
        │          │           │          network     │
        └──────────┴───────────┼───────────┴──────────┘
                               │
              ┌────────────────┼────────────────┐
              │                │                │
        aibox run        aibox workspace   devcontainer
        (ephemeral)       (persistent)     (VS Code, managed)
```

Data flow for a run:

```
.aibox.yaml + lock + flags
    → resolved Config
    → ContainerSpec (mounts, labels, env, hardening)
    → single mount renderer
    → podman argv  (--dry-run prints exactly this)
```

---

## 17. Open questions

1. **Assistant-driven allowlist requests.** Should a denied domain be
   discoverable as a structured suggestion (`aibox egress denied --suggest`)
   the user can approve, or does that erode the point of an allowlist?
2. **`workspace exec` and the git shim.** The shim lives on PATH inside the
   image, so an `exec` from the host inherits it. Correct, or should the host
   operator get an unshimmed git for repair work?
3. **Multiple assistants, one workspace container.** Sequential `run` calls
   against a persistent `workspace` mean both assistants' config volumes mount
   simultaneously. Acceptable, or one container per assistant?
4. **`.aibox/` in the workspace.** Handoff artefacts inside the repo are
   convenient and greppable but need gitignoring. Alternative: a host-side
   state directory keyed by project ID, at the cost of the agent not being able
   to write the handoff at all.
5. **Notes budget.** Is 2 KB right? Measure against a real session before fixing
   the number in code.
