# bclaude

<img src="assets/bclaude.svg" alt="" width="96" align="right">

Run the [Claude Code](https://claude.com/claude-code) CLI inside a rootless
podman container. It sees one directory of yours and nothing else.

The image is a working dev environment — Go, Python and Node toolchains — so
what Claude writes it can also build, run and test, in the container rather than
on your machine. Same image in VS Code via `bclaude devcontainer`.

Everything is in a single script — the Containerfile and the container
entrypoint are embedded in it, so one download is the whole install.

**WARNING: Use at your own risk!**

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/torlenor/bclaude/main/bclaude | bash -s -- install
```

That drops `bclaude` into `~/.local/bin` (pass a different directory as the last
argument). Then:

```bash
bclaude doctor                       # check podman is present and configured
cd ~/git/my-project && bclaude       # first run builds the image, once
```

Prefer to read before you run? It's one file:

```bash
curl -fsSLO https://raw.githubusercontent.com/torlenor/bclaude/main/bclaude
less bclaude && chmod +x bclaude && ./bclaude install
```

Requires rootless [podman](https://podman.io) and nothing else on the host.

## Usage

```bash
bclaude                             # interactive session on $PWD
bclaude -p "explain this repo"      # claude flags pass straight through
bclaude -w ~/git/proj               # mount a different directory
bclaude shell                       # a shell in the container instead
bclaude --ro -p "review this repo"  # read-only workspace: it cannot write anything
bclaude --volume-per-project        # config volume of its own for this repo
bclaude --allow-pkg                 # let Claude `sudo apt-get install` things
bclaude --egress proxy              # outbound traffic allowlisted and logged
bclaude egress denied               # what the allowlist blocked, by domain
bclaude devcontainer                # VS Code into the container, same setup
bclaude doctor                      # diagnose a broken setup
bclaude update                      # rebuild with the newest Claude Code
bclaude clean --all                 # remove image + every config volume + the login
```

`bclaude help` prints the full interface.

## Commands

| Command | Effect |
| --- | --- |
| *(none)* / `run` | Run Claude Code on the workspace |
| `shell [args]` | Shell in the container (`bclaude shell -c 'ls /work'`) |
| `build` | Build/rebuild the image |
| `update` | Rebuild from scratch with the newest Claude Code |
| `doctor` | Check podman, rootless setup, cgroups, subuid, image, credentials |
| `status` | Image, volumes, credential and workspace state |
| `install [DIR]` | Copy `bclaude` onto your PATH (default `~/.local/bin`); also works piped from curl |
| `egress [SUB]` | Egress proxy control: `status`, `denied`, `allow <domain..>`, `log [-f]`, `start`, `reload`, `stop` (see [Egress filtering](#egress-filtering---egress-proxy)) |
| `devcontainer` | Write `.devcontainer/devcontainer.json` so VS Code attaches into the same image, volumes and hardening; `--fresh` rebuilds the image and recreates the container on every start (see [VS Code](#vs-code-devcontainer)) |
| `clean [--all]` | Remove the image, egress proxy and networks; `--all` also offers to drop every config volume and the login, and removes the cache volume |
| `clean --list` | List the auth volume and the config volumes, with the project each belongs to |
| `clean --prune` | Drop per-project volumes whose project directory is gone (`--yes` to skip the prompt) |
| `show <part>` | Print an embedded file: `containerfile`, `entrypoint`, `squid-conf`, `allowlist` |
| `version` | Print the bclaude version |

A command name wins over a same-named claude subcommand — use `bclaude run
install` to force something through to claude.

## Options

Every option has an env-var equivalent, so both `bclaude -w ~/p` and
`BCLAUDE_WORKSPACE=~/p bclaude` work. Note the `BCLAUDE_` prefix on workspace,
volume, auth volume and image: bare `WORKSPACE`, `VOLUME`, `AUTH_VOLUME` and
`IMAGE` are deliberately ignored, since they pick what gets mounted and what
`bclaude clean` deletes — a stray `export IMAGE=` should not be able to aim
`podman rmi -f` at your own image.

| Option | Env | Default | Effect |
| --- | --- | --- | --- |
| `-w, --workspace DIR` | `BCLAUDE_WORKSPACE` | `$PWD` | Host dir mounted at `/work` — the only host path Claude can reach |
| `--ro` | `BCLAUDE_RO=1` | off | Mount the workspace read-only — Claude can read `/work`, never write it |
| `-V, --volume NAME` | `BCLAUDE_VOLUME` | `bclaude-config` | Named volume for `~/.claude` (sessions, settings; not the login) |
| `--volume-per-project` | `BCLAUDE_VOLUME_PER_PROJECT=1` | off | A config volume per workspace (see [Config volumes](#config-volumes)) |
| `--auth-volume NAME` | `BCLAUDE_AUTH_VOLUME` | `bclaude-auth` | Volume holding the login, shared by every project |
| `--cache-volume NAME` | `BCLAUDE_CACHE_VOLUME` | `bclaude-cache` | Volume holding `~/.cache` — Go modules, pip/uv wheels, npm (see [Toolchains](#toolchains)) |
| `-i, --image REF` | `BCLAUDE_IMAGE` | `localhost/bclaude:latest` | Image to run |
| `--claude-version V` | `CLAUDE_VERSION` | `latest` | Claude Code npm version baked into the image |
| `--memory SIZE` / `--cpus N` | `MEMORY` / `CPUS` | `4g` / `2` | Resource caps; `none` or `--no-limits` disables |
| | `TMPFS_SIZE` | `512m` | Size of the `nosuid` tmpfs at `/tmp` |
| `--egress MODE` | `BCLAUDE_EGRESS` | `open` | `proxy` restricts outbound traffic to an allowlist, logged per request (see [Egress filtering](#egress-filtering---egress-proxy)) |
| | `BCLAUDE_EGRESS_SUBNET` | `10.199.0.0/24` | Subnet of the internal network (change on collision with your LAN) |
| | `BCLAUDE_PROXY_IMAGE` | `docker.io/ubuntu/squid:latest` | Image for the egress sidecar |
| | `BCLAUDE_PROXY_LOG_DRIVER` | podman default | Log driver for the sidecar, e.g. `journald` to forward the audit trail |
| | `BCLAUDE_ALLOWLIST` | `~/.config/bclaude/allowlist` | The egress allowlist file |
| `--allow-pkg` | `ALLOW_PKG=1` | off | Passwordless `sudo apt` (relaxes two hardening flags — see below) |
| `--seed-config` | `SEED_CONFIG=1` | off | Copy host `settings.json` (model, tui, statusline) + statusline script in, rewriting host paths |
| `--seed-creds` | `SEED_CREDS=1` | off | Copy the host's Claude login into the auth volume (see [Credentials](#credentials)) |
| `--no-git-config` | `BCLAUDE_GIT_CONFIG=0` | mounted | `~/.gitconfig` is mounted read-only so `git commit` works inside |
| `--allow-root` | `BCLAUDE_ALLOW_ROOT=1` | off | Permit rootful podman — refused by default (see below) |
| `--rebuild` / `--no-cache` | | off | Rebuild the image before running |
| `--no-autobuild` | `BCLAUDE_AUTOBUILD=0` | off | Fail instead of auto-building a missing/stale image |
| `--dry-run` | | off | Print the `podman` command instead of running it |
| | `ANTHROPIC_API_KEY` | unset | Forwarded into the container if set — by name, so the value stays out of the `podman` command line |

## How it's wired

- **Rootless only** — running as root is refused unless you pass `--allow-root`.
  Rootless podman is what keeps a container escape from becoming host root.
- **Workspace** — `/work` is the only host project path mounted; everything else
  is throwaway container fs. `--ro` mounts it read-only for review sessions:
  Claude keeps working (config volume and `/tmp` stay writable), it just cannot
  change your files.
- **`--userns=keep-id:uid=1000,gid=1000`** maps your host user onto the image's
  `claude` user (uid/gid 1000), so files Claude writes in `/work` are owned by
  you — that's why the image renames `node` → `claude`. The explicit `uid=`/`gid=`
  matters when your host uid isn't 1000: plain `keep-id` would leave the
  container process unable to write a workspace owned by you.
- **Config** — lives in the `bclaude-config` volume, separate from your host
  `~/.claude`. The login is not in there; it sits in its own `bclaude-auth`
  volume that every project shares.
- **Caches** — `~/.cache` is the `bclaude-cache` volume, also shared: the Go
  module and build caches, pip/uv wheels and npm's cache outlive a `--rm` run
  (see [Toolchains](#toolchains)).
- **Network** — default rootless (pasta), unrestricted outbound: needed for
  npm/pip/git and the API. `--egress proxy` swaps that for an internal network
  whose only way out is an allowlisting squid sidecar — see
  [Egress filtering](#egress-filtering---egress-proxy).
- **Hardening** — `cap-drop=ALL`, `no-new-privileges`, seccomp, `pids-limit 2048`,
  memory/cpu caps, `nosuid` tmpfs `/tmp`, setuid stripped from all but `sudo`.
- **Image freshness** — the image is stamped with a hash of the embedded
  Containerfile + entrypoint, so editing the script rebuilds it on the next run.
- **Portability** — SELinux hosts get the `:z` mount relabel automatically; hosts
  that can't enforce rootless cgroup limits get them dropped with a warning
  instead of a podman error; a TTY is only requested when there is one, so
  `bclaude -p ...` works in pipes and CI.

## Config volumes

There are three volumes, and the split is the point:

- **`bclaude-auth`** holds nothing but the login, and every project shares it.
- **`bclaude-config`** holds the rest of `~/.claude` — sessions, project
  settings, MCP servers, plugins.
- **`bclaude-cache`** holds `~/.cache`: toolchain caches, nothing of yours.
  Shared as well, and covered under [Toolchains](#toolchains).

By default every project shares that one config volume too, so what you did in
one repo is visible in the next. `--volume-per-project` gives each workspace its
own, named after the directory plus a hash of its full path
(`bclaude-config-myrepo-1a2b3c4d`), so two repos called `api` don't collide and
the name doesn't change with how you spell the path. The auth volume is
unaffected either way: a new project is **not** a new login.

```bash
bclaude --volume-per-project      # this repo gets its own sessions and settings
bclaude clean --list              # what exists, for which project
bclaude clean --prune             # drop volumes whose project directory is gone
```

Claude Code only knows about one config directory, so the entrypoint copies the
login in from the auth volume at startup and writes it back when the session
ends — the token gets refreshed while you work, and that refresh has to reach
your other projects. It writes back only a token this session actually changed,
and stands aside if a session that started alongside it refreshed later, so
running several at once won't put a spent token over a live one.

Volumes bclaude creates are labelled, which is how `--list` and `--prune` know
what they're looking at. `--prune` only ever considers per-project volumes whose
recorded project directory is gone — never the shared config, auth or cache
volume.

Isolation caveat: separate config volumes separate *state*, not trust. Every
project's container mounts the same auth volume, so anything that runs in one
can read the login.

## Credentials

**Your host token stays on your host.** bclaude does not copy
`~/.claude/.credentials.json` anywhere: the container logs in on its own the
first time you run it, and that login is stored in the `bclaude-auth` volume —
a one-time step for all your projects.

| How to be logged in | What it costs you |
| --- | --- |
| **Log in inside the container** (default) | one login prompt, ever; the host token is never exposed |
| `ANTHROPIC_API_KEY` | forwarded into the container when set; no OAuth token involved |
| `--seed-creds` | copies your host login into the auth volume — convenient, but the host refresh token is now in there too |

`--seed-creds` mounts the host file read-only and the entrypoint copies it in,
replacing any login already there — which makes it the fix for a seeded token
that went stale, too. Once credentials exist the entrypoint sets
`hasCompletedOnboarding: true`, so interactive launches skip the onboarding flow.

Caveats: whichever way you log in, the token ends up in the auth volume, which
every project's container mounts and anything running as the container user can
read (same as on your host); and with `--seed-creds` host and container hold
independent copies of the refresh token, so a rotation may force one side to
re-auth. **If untrusted code ran in the container, rotate by re-logging in on
the host.**

## Toolchains

The image is not just a Claude Code runtime — it is a dev environment, so
Claude can build and test what it writes instead of handing you code it never
ran:

| | What's in the image |
| --- | --- |
| **Go** | the upstream toolchain in `/usr/local/go` (pinned to a version and its SHA-256), plus `gopls`, `dlv`, `staticcheck`, `goimports` |
| **Python** | the system `python3` with headers and a compiler, so native extensions build; `uv`, `ruff`, `mypy`, `pytest` and `ipython` in a venv of their own, on `PATH` but unable to collide with a project's dependencies |
| **Node** | the base image's node 22 and npm |

Everything a build downloads lands in `~/.cache`, which is the shared
`bclaude-cache` volume — so a second run doesn't refetch the module cache and
the wheels. Nothing in it is precious, which is why `bclaude clean --all` drops
it without a prompt while it asks about sessions and the login.

Notes:

- **`pip install` works.** Debian's PEP 668 "externally managed" marker is
  removed from the image, so `pip install --user` behaves; `~/.local/bin` is on
  `PATH`. Those installs are in the container layer, so they vanish with `--rm` —
  a project venv (or `uv`) is what survives, because the workspace does.
- **A `go.mod` that wants a newer toolchain** gets one at run time through the
  module proxy, verified against the checksum database and cached in the volume.
- **Bigger builds want more room** than the 4g/2cpu default:
  `bclaude --memory 8g --cpus 4`.
- **Under `--egress proxy`**, `proxy.golang.org`, `sum.golang.org`, `pypi.org`
  and `files.pythonhosted.org` are in the default allowlist. Anything else a
  build reaches for needs `bclaude egress allow <domain>`.
- The toolchains cost image size (a couple of GB) and build time. To trim them,
  edit the embedded Containerfile (`bclaude show containerfile`) and rebuild.

## Installing packages (`--allow-pkg`)

```bash
bclaude --allow-pkg
# inside: sudo apt-get update && sudo apt-get install -y <pkg>
```

`apt-get update` is required first (the image ships empty lists), and installs
**don't persist** (`--rm`) — for anything permanent, edit the embedded
Containerfile in the script (`bclaude show containerfile` to see it) and run
`bclaude build`. `--allow-pkg` relaxes `no-new-privileges` + `cap-drop=ALL`;
sudo is scoped to `apt-get`/`apt`/`dpkg` in `/etc/sudoers.d/claude-apt`.

## Egress filtering (`--egress proxy`)

```bash
bclaude --egress proxy            # this session can only reach allowlisted domains
bclaude egress denied             # what got blocked, counted by domain
bclaude egress allow api.foo.com  # extend the allowlist, live
bclaude egress log -f             # follow the audit trail (one line per request)
```

Not nftables inside the container — `--cap-drop=ALL` plus a non-root user means
nothing in there could load a ruleset anyway, and granting `NET_ADMIN` to get
one would trade away more than it buys. Instead the *network layout* is the
enforcement:

- The claude container joins only an **internal podman network**
  (`bclaude-internal`): no route off-host, no external DNS. There is nothing to
  reach except the sidecar.
- A **squid sidecar** (`bclaude-proxy`) sits on that network and an ordinary
  one, enforcing a `dstdomain` allowlist, deny-by-default. `HTTPS_PROXY` etc.
  are set in the claude container only so tools know where the one door is.
- One log line per request lands on the sidecar's stdout, so
  `podman logs bclaude-proxy` is the audit trail (`BCLAUDE_PROXY_LOG_DRIVER=journald`
  if you'd rather it landed somewhere forwardable).

This closes DNS exfiltration too: the container cannot resolve external names
at all — squid does all resolution on its outward leg.

The allowlist lives at `~/.config/bclaude/allowlist`, seeded on first use from
the [Claude Code network requirements](https://code.claude.com/docs/en/network-config)
plus common dev hosts (npm, pip, github, apt), each line commented with what it
is for so trimming is informed (`bclaude show allowlist` prints the default).
`bclaude egress allow <domain>` appends and reloads the live proxy; after
editing the file by hand, run `bclaude egress reload`. The tuning loop is:
run a session, see what broke, `bclaude egress denied`, allow what you meant.

Caveats, honestly:

- **git over SSH does not work** in proxy mode (nothing but the proxy is
  reachable, and squid only speaks HTTP). Use https remotes.
- **An allowed domain is still a channel**: with `github.com` on the list,
  code that can push can exfiltrate. Trim the list for review sessions
  (`--ro --egress proxy` is a good pairing).
- `--allow-pkg` + proxy mode: sudo strips the proxy env, so pass it through
  apt explicitly:
  `sudo apt-get -o Acquire::http::Proxy="$http_proxy" -o Acquire::https::Proxy="$https_proxy" update`.
- The sidecar and its networks persist across sessions (cheap, and shared by
  concurrent sessions); `bclaude egress stop` or `bclaude clean` removes them.
  The allowlist file is never deleted.

## VS Code (devcontainer)

The Claude Code extension talks to the CLI over a loopback WebSocket on a
random port (advertised in `$CLAUDE_CONFIG_DIR/ide/<port>.lock`) — exactly the
thing a separate network namespace can't reach. So bclaude doesn't try to
bridge it; it moves the VS Code server *into* the container instead:

```bash
cd ~/git/my-project
bclaude devcontainer              # writes .devcontainer/devcontainer.json
# in VS Code: "Dev Containers: Reopen in Container"
```

The generated file uses the same image, volumes, userns mapping and hardening
flags as `bclaude` itself, so the extension, the CLI and your session state all
behave identically — diffs and selection context included, because everything
shares the container's loopback. Requires the Dev Containers extension with
`"dev.containers.dockerPath": "podman"`.

It also brings the Go and Python extensions and points them at the
[toolchains](#toolchains) already in the image — `go.goroot`, the baked-in
`gopls`/`dlv`/`staticcheck`, the interpreter — with the Go extension's own tool
bootstrap turned off, since that wants a module proxy `--egress proxy` may not
be allowing. The `bclaude-cache` volume is mounted too, so the module cache is
the same one the CLI uses.

### Keeping it from going stale

VS Code reuses the container it already has, so `bclaude build` alone doesn't
reach a project that's been opened before — you need **Dev Containers: Rebuild
Container**. `--fresh` removes that step:

```bash
bclaude devcontainer --fresh --force
```

It adds two things: an `initializeCommand` that runs `bclaude build` **on the
host** before every start (the spec runs it on creation *and* subsequent
starts), and `--rm`, so the container is discarded when VS Code stops it and the
next start creates one from the image that was just rebuilt. Anything installed
into the container layer (`pip install --user`, `--allow-pkg` apt installs) goes
with it; the caches, your config and the VS Code server are all in volumes and
survive.

The file names the absolute path of the script that generated it, so a `--fresh`
file is local to your machine — regenerate without it before committing one to a
shared repo. Piped from curl there is no path to name, and `--fresh` refuses
rather than write a broken file.

The other half of the staleness problem is already handled: the generated file
sets `"updateRemoteUserUID": false`, which stops the extension deriving its own
`localhost/vsc-<project>-<hash>-uid` image from bclaude's. That derivation
exists only to rewrite the container user's UID, which `--userns=keep-id` has
already done — and it was a second cached image sitting between `bclaude build`
and what actually ran.

It honours the current flags: `bclaude --volume-per-project devcontainer`
pins this repo's config volume, and `bclaude --egress proxy devcontainer`
puts the whole IDE session behind the egress allowlist (run
`bclaude egress start` before opening the container; the VS Code server
download hosts are in the default allowlist). Regenerate with `--force` after
changing your mind.

If you only want a terminal in VS Code and no diff view, you don't need any of
this — a terminal profile that runs `bclaude` is a two-line settings change.

## Known trade-offs

- **keep-id**: correct `/work` ownership, but an escape lands as your host user
  rather than a throwaway subuid.
- **Egress is open by default**: the container can reach your LAN and host
  services (e.g. Postgres:5432). `--egress proxy` closes this down to an
  allowlist — but an allowed domain is still a usable channel, and the default
  list includes github.com. See
  [Egress filtering](#egress-filtering---egress-proxy).

## Tests

```bash
tests/run-tests.sh --fast    # ~0.5s, no podman needed: parsing, flags, arg routing
tests/run-tests.sh           # also builds the image and runs the container
```

Safe to run on a machine you actually use bclaude on: the suite clears every
`BCLAUDE_*` variable from your environment and works only on
`localhost/bclaude-test:latest`, `bclaude-test-config`, `bclaude-test-auth` and
`bclaude-test-cache`, which it removes when it finishes (`BCLAUDE_KEEP=1` keeps
them). It never reads
your host credentials and never writes outside those volumes, `tests/` and
`mktemp` directories. The full run does build an image (a few minutes, needs
network) and start containers.

CI (`.github/workflows/ci.yml`) runs shellcheck, the fast suite, and the full
suite with a real podman build on every push.

## Notes

- Build-time `can't raise ambient capability` warnings are normal in rootless mode.
- `bclaude doctor` tells you exactly what's missing on a fresh machine
  (podman, `uidmap`, `/etc/subuid` entries, cgroup delegation).

## License

MIT — see [LICENSE](LICENSE).
