---
name: aibox
description: >
  How to work inside an aibox container. Load before any git commit, push or
  tag, when work is ready to commit or push, when a pull request, merge
  request, issue, comment or release is to be made with gh or glab, when a
  secret, token, key or password is needed, when a network request fails or
  times out, when a container, registry, package install or host-side action
  is needed, or when you are unsure what persists or what you may write.
---

# Working inside aibox

You run in a hardened, rootless container. The limits below are policy. They
are not failures to work around. When a limit blocks you, give the user the
exact host command in one fenced block, then stop or continue with something
else. `ainotes` prints this session's actual state. Read its "This session"
section once: egress mode, git history, relay services.

## Rules

1. Git writes work, but a commit, push, tag, merge, rebase, reset, branch
   deletion or force operation never happens on your own initiative: rule 6
   applies to each one. If `ainotes` → "This session" says `.git` is
   read-only, do not attempt any write verb; use the handoff below.
2. Never print, echo, log, or write a secret to a file. Never put a secret
   in argv, because `ps` shows argv.
3. Never retry a blocked network request in a loop. Never tunnel, change
   proxy variables, or use another host to get around the allowlist.
4. Start podman only if `ainotes` → "This session" says nested podman is
   ENABLED. There is no docker. Otherwise there is no container runtime.
5. Every host command you give the user goes in one copy-pasteable block.
   Do not scatter commands through prose.
6. Ask the user before every commit, push and forge write, and wait for a
   yes. This applies each time, also when the same kind of action was
   approved before. Show the exact command or message you intend to use.
7. Never put AI attribution into a commit message, PR or MR description,
   issue, comment, review or release note. No `Co-Authored-By`, no
   "Generated with", no mention of Claude, Claude Code, Codex, an AI, an
   assistant or an agent. The text is the user's own words.

## Git: commit and push

Read-only git (status, diff, log, show, blame, grep, branch list,
`remote -v`) needs no question. For a commit, push or tag:

1. Check `ainotes` → "This session" for the git mode and the identity. In a
   read-only session go to "The handoff" below instead.
2. Review what goes in: `git status --porcelain`, `git diff HEAD --stat`.
   Stage exact paths, not `-A`, when unrelated files are dirty.
3. Show the user the paths and the full commit message, ask, and wait for a
   yes. Then commit with a heredoc so the message survives quoting:

   ```bash
   git add path/one path/two
   git commit -F - <<'EOF'
   type: imperative subject, max 72 chars

   body: why, not what. No trailers, no AI attribution.
   EOF
   ```

   If "This session" reports no identity, ask the user for name and email
   and pass them: `git -c user.name=… -c user.email=… commit …`.
4. Push only after a separate yes for the push. Over https the token comes
   from `/creds`, scoped to that one command:

   ```bash
   GH_TOKEN="$(creds get github-token)" git -c credential.helper='!gh auth git-credential' push -u origin <branch>
   GITLAB_TOKEN="$(creds get gitlab-token)" git -c credential.helper='!glab auth git-credential' push
   ```

   Over ssh the forge must be reachable (a relay service in proxy mode) and
   a key loaded; otherwise hand the push to the user as a host command.
5. A new branch (`git switch -c <branch>`) and a release tag
   (`git tag -a vX.Y.Z -m "vX.Y.Z" && git push --tags`) each get their own
   question. Keep `CHANGELOG.md` current under `## Unreleased`.

### The handoff

For a read-only session, or when the user prefers to commit on the host:

1. Write the handoff files into `/work/.aibox/`. That directory is writable,
   and `aibox handoff` on the host reads it:

   ```bash
   mkdir -p /work/.aibox
   { git status --porcelain | cut -c4-; } > /work/.aibox/changed.txt
   { git diff HEAD
     git ls-files --others --exclude-standard -z |
       xargs -0 -r -I{} git diff --no-index /dev/null {} ; } \
     > /work/.aibox/review.patch 2>/dev/null || true
   ```

   Then write `/work/.aibox/HANDOFF.md`:

   ```markdown
   # Handoff
   ## Summary
   What changed and why, in 2-5 lines.
   ## Suggested commit message
   <type>: <imperative subject, max 72 chars>

   <body: why, not what — no trailers, no AI attribution>
   ## Files
   - path — one-line reason
   ## Verify
   Commands that were run and their result (tests, build). Say what was NOT run.
   ```

2. Give the user the host commands. Stage the exact paths, not `-A`, when
   unrelated files are dirty. Use a heredoc so the message survives quoting:

   ```bash
   # on the host, in the workspace root
   aibox handoff --diff          # review the patch
   git add path/one path/two
   git commit -F - <<'EOF'
   type: subject

   body
   EOF
   git push                      # add -u origin <branch> for a new branch
   aibox handoff --clear
   ```

   For a new branch, put `git switch -c <branch>` first. For a release tag:
   `git tag -a vX.Y.Z -m "vX.Y.Z" && git push --tags`.

`.aibox/` is the handoff directory. Do not commit it. If it is not in
`.gitignore`, tell the user.

## Forge: `gh` and `glab`

`gh` (GitHub) and `glab` (GitLab) are installed, and forge writes are
allowed: `pr`/`mr create`, `comment`, `review`, `merge`, `issue`,
`release`. They go over the API, so they do not need a git write path. The
rules:

1. Ask before every write. Show the exact command and, for a PR, MR, issue
   or comment, the full text. Wait for a yes. Reads (`pr list`, `pr view`,
   `issue view`, `api GET`) need no question.
2. A PR or MR needs the branch on the forge first: that push is its own
   question (see "Git: commit and push"). Open the PR or MR after the push
   has succeeded.
3. No AI attribution in any text you send (rule 7). Do not use a template or
   flag that adds one.
4. The token comes from `/creds`, scoped to one command (see Secrets):

   ```bash
   GH_TOKEN="$(creds get github-token)" gh pr create --fill
   GITLAB_TOKEN="$(creds get gitlab-token)" glab mr create --fill
   # self-hosted GitLab: GITLAB_HOST=gitlab.example.com (allowlist it first)
   ```

   Do not run `gh auth login` or `glab auth login`. They write a token to
   `$HOME`, which does not persist and must not hold secrets.

In proxy mode `api.github.com`, `uploads.github.com` and `gitlab.com` are
allowlisted by default. A self-hosted forge needs `aibox egress allow HOST`.

## Secrets: `creds`

Secrets are encrypted files at `/creds/NAME.enc`. The decryption passphrase is
present only while the user runs `aibox secret allow-access` on the host. The
default grant is 10 minutes.

The efficient flow uses one request and one grant:

1. `creds list` shows names only and works without a grant. Find every
   secret the task needs before you ask.
2. Try one of them: `creds get NAME >/dev/null`. Exit code 3 means no grant.
   Ask once, and name every secret and the reason:

   ```bash
   # on the host — lets the container decrypt for 10 minutes
   aibox secret allow-access --ttl 10m
   ```

   If a secret is missing, ask for `aibox secret add NAME`. The value comes
   from a prompt, never the command line.
3. Do all the secret-dependent work inside the grant window.

Each Bash tool call starts a new shell, so an `export` does not survive to
the next call. Decrypt in the same command that uses the secret, and pass it
by stdin or by environment, never by argv:

```bash
# stdin (best)
creds get registry-pass | skopeo login --username me --password-stdin quay.io
creds get ssh-key | ssh-add -          # needs an agent in the same command
# HTTP header from stdin, not argv
printf 'Authorization: Bearer %s\n' "$(creds get api-token)" | curl -sS -H @- https://api.example.com/x
# environment, scoped to one command
GITHUB_TOKEN="$(creds get github-token)" gh api user
# file-descriptor path for tools that only take a file
ssh -i <(creds get ssh-key) host
```

In Python (including paramiko), read the secret at run time:

```python
import subprocess
def secret(name: str) -> str:
    return subprocess.run(["creds", "get", name], capture_output=True,
                          text=True, check=True).stdout.rstrip("\n")
```

When the work is done, tell the user they can run `aibox secret revoke-access`.
If a secret was ever exposed (printed, logged, committed, or written to a
file), stop. Tell the user it is compromised and ask them to rotate it
(`aibox secret rotate-key`, then replace the value at its source).

## Network

Check `ainotes` → "This session". In **proxy** mode, HTTP/HTTPS goes
through an allowlisting squid. Other TCP goes through named relay services
only. `git` over SSH to a forge does not work in this mode.

A failure is an ACL denial until you have proved otherwise. Test once:

```bash
curl -sSfI https://HOST/ >/dev/null && echo allowed   # a 403 from squid = denied
nc -z -w3 SERVICE PORT && echo reachable             # a relay service
```

Then give the user the host command, and name the exact domain:

```bash
aibox egress allow HOST            # an HTTPS domain
aibox egress denied                # what the proxy blocked (for the user)
```

Relay services: `/run/aibox/services.json` holds one JSON object per line
(`name`, `proto`, `address`, `aliases`). SSH services are ready in
`~/.ssh/config`: `ssh <name>`. For a new service, the user adds it under
`services:` in `.aibox.yaml` (`name`, `backend: host:port`, optional
`listen`, `aliases`) and runs `aibox relay restart`.

Package registries (pypi, npm, proxy.golang.org, galaxy) are allowlisted by
default in most setups. If one fails, it is usually a missing domain, not
an outage.

## Containers and images

Without nested podman, for services a task needs (database, broker), print
the `podman run ...` command for the host, or ask the user to run
`aibox podman enable` and restart the session. Do not try to get around it.

With nested podman ENABLED, `podman run` works here. Use full image names
(`docker.io/library/postgres:16`). Inner containers share this sandbox's
network and egress allowlist, and images persist per project. Do not use
`-p`: the network is shared, so a service is already on `localhost`.

For registry work, use **skopeo**. It needs no daemon:

```bash
skopeo inspect docker://quay.io/org/img:tag
skopeo list-tags docker://docker.io/library/alpine
skopeo copy docker://src/img:tag dir:/tmp/img     # or oci:/tmp/img
```

The registry domain must be allowlisted in proxy mode.

## Installing things

- `sudo` works only when the session was started with `--allow-pkg` or has
  nested podman ENABLED. Do not try it otherwise. Ask the user to restart
  with `--allow-pkg`, or install in user space.
- Python: `uv` / `python -m venv` in the project, or `pip install --user`.
  For something that must survive across sessions, use
  `pip install --target ~/.cache/pylibs` (already on `PYTHONPATH`).
  paramiko is preinstalled for the system `python3`.
- Node: `npm install -g` goes to `~/.npm-global` (on PATH).
- Go: `go install` goes to `~/.cache/go/bin` (on PATH, persists).
- Preinstalled: rg fd bat fzf tree jq yq curl nc openssl expect skopeo gh
  glab oc kubectl bao pwsh lua ansible ansible-lint go uv ruff mypy pytest
  node.
  Check `ainotes` before you install anything.

## What persists and what you may write

| Path | Writable | Persists |
|---|---|---|
| `/work` (incl. `.git` unless the session is read-only) | yes (unless `--ro`) | yes — it is the host repo |
| `/work/.aibox/` | yes | yes — handoff to host |
| `~/.cache` | yes | yes — cache volume, all projects |
| `/ephemeral` | yes | wiped at every session start; shared with host |
| `/tmp`, rest of `$HOME` | yes | no |
| `/creds`, `/run/host-aibox`, `/usr/share/aibox` | no | — |

`~/.claude/skills` and `~/.claude/agents` hold symlinks, which are rebuilt at
every start. Their targets are read-only. Do not edit them.

## Running something on the host

If a task needs the unfiltered network, a container runtime, or host tools,
put a script in `/ephemeral`. Then give the user this:

```bash
cd "$(aibox ephemeral)" && ./script.sh
```

Have the script write its output back into the same directory, so you can
read it. Never put secrets in these scripts.
