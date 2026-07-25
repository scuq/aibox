# bclaude

<img src="assets/bclaude.svg" alt="" width="96" align="right">

Run [Claude Code](https://claude.com/claude-code) inside a rootless podman
container. It sees one directory of yours and nothing else.

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
bclaude --model opus                # ... any of them
bclaude -w ~/git/proj               # mount a different directory
bclaude shell                       # a shell in the container instead
bclaude --ro -p "review this repo"  # read-only workspace: it cannot write anything
bclaude --volume-per-project        # config volume of its own for this repo
bclaude --allow-pkg                 # let Claude `sudo apt-get install` things
bclaude doctor                      # diagnose a broken setup
bclaude status                      # image / volume / login state
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
| `clean [--all]` | Remove the image; `--all` also offers to drop every config volume and the login |
| `clean --list` | List the auth volume and the config volumes, with the project each belongs to |
| `clean --prune` | Drop per-project volumes whose project directory is gone (`--yes` to skip the prompt) |
| `show containerfile` / `show entrypoint` | Print the embedded files (to inspect or fork) |

A command name wins over a same-named claude subcommand — use `bclaude run
install` to force something through to claude.

## Options

Every option has an env-var equivalent, so both `bclaude -w ~/p` and
`BCLAUDE_WORKSPACE=~/p bclaude` work.

| Option | Env | Default | Effect |
| --- | --- | --- | --- |
| `-w, --workspace DIR` | `BCLAUDE_WORKSPACE` | `$PWD` | Host dir mounted at `/work` — the only host path Claude can reach |
| `--ro` | `BCLAUDE_RO=1` | off | Mount the workspace read-only — Claude can read `/work`, never write it |
| `-V, --volume NAME` | `BCLAUDE_VOLUME` | `bclaude-config` | Named volume for `~/.claude` (sessions, settings; not the login) |
| `--volume-per-project` | `BCLAUDE_VOLUME_PER_PROJECT=1` | off | A config volume per workspace instead of one shared one (see [Config volumes](#config-volumes)) |
| `--auth-volume NAME` | `BCLAUDE_AUTH_VOLUME` | `bclaude-auth` | Volume holding the login, shared by every project |
| `-i, --image REF` | `BCLAUDE_IMAGE` | `localhost/bclaude:latest` | Image to run |
| `--claude-version V` | `CLAUDE_VERSION` | `latest` | Claude Code npm version baked into the image |
| `--memory SIZE` / `--cpus N` | `MEMORY` / `CPUS` | `4g` / `2` | Resource caps; `none` or `--no-limits` disables |
| | `TMPFS_SIZE` | `512m` | Size of the `nosuid` tmpfs at `/tmp` |
| `--allow-pkg` | `ALLOW_PKG=1` | off | Passwordless `sudo apt` (relaxes two hardening flags — see below) |
| `--seed-config` | `SEED_CONFIG=1` | off | Copy host `settings.json` (model, tui, statusline) + statusline script in, rewriting host paths |
| `--seed-creds` | `SEED_CREDS=1` | off | Copy the host's Claude login into the auth volume (see [Credentials](#credentials)) |
| `--no-git-config` | `BCLAUDE_GIT_CONFIG=0` | mounted | `~/.gitconfig` is mounted read-only so `git commit` works inside |
| `--allow-root` | `BCLAUDE_ALLOW_ROOT=1` | off | Permit rootful podman — refused by default (see below) |
| `--rebuild` / `--no-cache` | | off | Rebuild the image before running |
| `--no-autobuild` | `BCLAUDE_AUTOBUILD=0` | off | Fail instead of auto-building a missing/stale image |
| `--dry-run` | | off | Print the `podman` command instead of running it |
| | `ANTHROPIC_API_KEY` | unset | Forwarded into the container if set |

## How it's wired

- **Rootless only** running as root is refused unless you pass `--allow-root`.
  Rootless podman is what keeps a container escape from becoming host root; with
  rootful podman the sandbox is worth much less.
- **Workspace** `/work` is the only host project path mounted; everything else
  is throwaway container fs. `--ro` mounts it read-only for analysis and review
  sessions — Claude keeps working (the config volume and `/tmp` stay writable),
  it just cannot change your files.
- **`--userns=keep-id`** maps container uid 1000 → your host user, so files
  Claude writes in `/work` are owned by you (why the image renames `node` →
  `claude`).
- **Config** lives in the `bclaude-config` volume at `/home/claude/.claude`,
  separate from your host `~/.claude` — one volume shared by every project, or
  one per project with `--volume-per-project`. The login is not in there: it
  sits in its own `bclaude-auth` volume that every project shares.
- **Network** default rootless (pasta), unrestricted outbound — needed for
  npm/pip/git and the API.
- **Hardening** `cap-drop=ALL`, `no-new-privileges`, seccomp, `pids-limit 2048`,
  memory/cpu caps, `nosuid` tmpfs `/tmp`, setuid stripped from all but `sudo`.
- **Image freshness** the image is stamped with a hash of the embedded
  Containerfile + entrypoint. Edit the script and the next run rebuilds itself.
- **Portability** SELinux hosts get the `,z` mount relabel automatically; hosts
  that can't enforce rootless cgroup limits get them dropped with a warning
  instead of a podman error; a TTY is only requested when there is one, so
  `bclaude -p ...` works in pipes and CI.

## Config volumes

There are two volumes, and the split is the point:

- **`bclaude-auth`** holds nothing but the login, and every project shares it.
- **`bclaude-config`** holds the rest of `~/.claude` — sessions, project
  settings, MCP servers, plugins.

By default every project shares that one config volume too, so what you did in
one repo is visible in the next. `--volume-per-project` gives each workspace its
own, named after the directory plus a hash of its full path
(`bclaude-config-myrepo-1a2b3c4d`) — so two repos called `api` in different
places don't collide, and the name stays the same no matter how you spell the
path. `-V/--volume` still wins when you name one; the auth volume is unaffected
either way, so a new project is **not** a new login.

```bash
bclaude --volume-per-project      # this repo gets its own sessions and settings
bclaude clean --list              # what exists, for which project
bclaude clean --prune             # drop volumes whose project directory is gone
```

Claude Code only knows about one config directory, so the entrypoint copies the
login in from the auth volume at startup and writes it back out when the session
ends — the token gets refreshed while you work, and that refresh has to reach
your other projects. Two sessions running at once means last writer wins.

Volumes bclaude creates are labelled (`org.bclaude.managed`, `org.bclaude.role`,
and the project path for per-project ones), which is how `--list` and `--prune`
know what they are looking at. `--prune` only ever considers volumes whose
recorded project directory no longer exists — never the shared config volume,
never the auth volume.

Isolation caveat: separate config volumes separate *state*, not trust. Every
project's container mounts the same auth volume, so anything that runs in one
can read the login.

## Credentials

**Your host token stays on your host.** bclaude does not copy
`~/.claude/.credentials.json` anywhere: the container logs in on its own the
first time you run it, and that login is stored in the `bclaude-auth` volume.
It's a one-time step for all your projects — every later run picks it up.

| How to be logged in | What it costs you |
| --- | --- |
| **Log in inside the container** (default) | one login prompt, ever; the host token is never exposed |
| `ANTHROPIC_API_KEY` | forwarded into the container when set; no OAuth token involved |
| `--seed-creds` | copies your host login into the auth volume — convenient, but the host refresh token is now in there too |

`--seed-creds` mounts the host file read-only and the entrypoint copies it in,
replacing any login already there — which makes it the fix for a seeded token
that went stale, too. The entrypoint also sets `hasCompletedOnboarding: true`
once credentials exist, so interactive launches skip the onboarding flow.

Caveats: whichever way you log in, the token ends up in the auth volume, which
every project's container mounts and anything running as the container user can
read (same as on your host); and with `--seed-creds` host and container hold
independent copies of the refresh token, so a rotation may force one side to
re-auth. **If untrusted code ran in the container, rotate by re-logging in on
the host.**

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

## Known trade-offs

- **keep-id**: correct `/work` ownership, but an escape lands as your host user
  rather than a throwaway subuid.
- **Unrestricted egress**: the container can reach your LAN and host services
  (e.g. Postgres:5432). Claude needs the API, so the useful restriction isn't
  `--network=none` but an allowlist proxy that permits only `api.anthropic.com`.

## Tests

```bash
tests/run-tests.sh --fast    # ~0.5s, no podman needed: parsing, flags, arg routing
tests/run-tests.sh           # also builds the image and runs the container
```

Safe to run on a machine you actually use bclaude on. The suite clears every
`BCLAUDE_*` variable from your environment and works only on
`localhost/bclaude-test:latest`, `bclaude-test-config` and `bclaude-test-auth`,
which it removes when it finishes (`BCLAUDE_KEEP=1` keeps them); the cleanup
refuses to touch anything not named like a test artifact. It never reads your
host credentials and never writes outside the volumes above, `tests/` and
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
