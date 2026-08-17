#!/usr/bin/env bash
# Runs once, at image build time, as root. Writes the image manifest and the
# image layer of the environment notes, then gates the notes' size.
#
# Generated, never hand-maintained: the manifest and the notes derive from the
# same probe of the installed tools, so the versions in the notes cannot drift
# from the versions in the image. Hand-written notes rot in about two releases.
set -euo pipefail

# AIBOX_SHARE is a test knob: the notes test runs this script on the host,
# where /usr/share is not writable and the probed tools are absent.
SHARE="${AIBOX_SHARE:-/usr/share/aibox}"
mkdir -p "$SHARE"

ver() {   # ver <command...> — first x.y.z-looking token, or "unknown"
    "$@" 2>/dev/null | head -1 | grep -oE '[0-9]+\.[0-9]+(\.[0-9]+)?' | head -1 || echo unknown
}

CLAUDE_V="$(ver claude --version)"
CODEX_V="$(ver codex --version)"
GO_V="$(ver /usr/local/go/bin/go version)"
NODE_V="$(ver node --version)"
PYTHON_V="$(ver python3 --version)"
RG_V="$(ver rg --version)"
FD_V="$(ver fd --version)"
BAT_V="$(ver bat --version)"
OC_V="$(ver oc version --client)"
ANSIBLE_V="$(ver ansible --version)"
LUA_V="$(ver lua -v)"
PWSH_V="$(ver pwsh --version)"
BAO_V="$(ver bao --version)"
OPENSSL_V="$(ver openssl version)"
YQ_V="$(ver yq --version)"

cat > "$SHARE/image-manifest.json" <<EOF
{
  "schemaVersion": 1,
  "image": { "profile": "${AIBOX_PROFILE:-base}", "release": "${AIBOX_RELEASE:-local}" },
  "toolchains": { "go": "$GO_V", "node": "$NODE_V", "python": "$PYTHON_V", "lua": "$LUA_V", "powershell": "$PWSH_V" },
  "assistants": { "claude": "$CLAUDE_V", "codex": "$CODEX_V" },
  "tools": { "ripgrep": "$RG_V", "fd": "$FD_V", "bat": "$BAT_V", "yq": "$YQ_V", "oc": "$OC_V", "ansible-core": "$ANSIBLE_V", "openbao": "$BAO_V", "openssl": "$OPENSSL_V" }
}
EOF

# The image layer of the notes. The git-policy section leads on purpose: the
# assistant plans around the constraint instead of discovering it by failure.
# The network and "no runtime here" sections turn the two things an agent most
# often gets wrong in this environment — a blocked domain read as an outage,
# and trying to start a container that cannot start — into a hand-off to the
# user with the exact command to run.
cat > "$SHARE/ainotes-image.md" <<EOF
# aibox environment notes

## Repository writes — read this first
Do NOT use git write commands (add, commit, push, checkout, reset, stash…).
.git is read-only; the user commits on the host. Record intended changes in
.aibox/HANDOFF.md with a suggested commit message. Read-only git (status,
diff, log, show, blame, grep) works. When a push is needed, do NOT attempt it
— hand the user the commands to run on the host, e.g.:
    git -C <repo> add -A && git commit -m "…" && git push

## Changelog & releases
Keep a CHANGELOG.md in the repo root and update it as you work: record every
notable change under a "## Unreleased" heading. You CAN write CHANGELOG.md —
it is an ordinary file, not under .git. Releases use semver and git tags, cut
by the user on the host — when one is due, rename Unreleased to "## X.Y.Z -
YYYY-MM-DD" and hand over:
    git -C <repo> tag -a vX.Y.Z -m "vX.Y.Z" && git push --tags

## Network — allowlisted, not open
Outbound HTTP/HTTPS goes through a squid proxy that permits only allowlisted
domains; other TCP endpoints go through a named relay. A failed connection is
almost always an ACL denial, NOT an outage — so verify before concluding, and
never retry-loop:
    curl -sSfI https://HOST        # is this domain allowed?
    nc -z RELAYHOST PORT           # is this relay service reachable?
If it fails, tell the user the exact resource and the command to allow it on
the host — do not work around it:
    aibox egress allow HOST                    # an HTTPS domain (int or ext)
    # a raw TCP service (ssh/db/api): add it under services: in .aibox.yaml,
    # then: aibox relay restart   — reach it by name, e.g.  ssh <service>
Relay services already granted are listed in /run/aibox/services.json.

## No container runtime here
There is no podman/docker in this environment. If the project needs services
(database, cache, broker, a compose stack) to build, run or test, do NOT try
to start them here — print the commands and hand them to the user to run on
the host, e.g.:
    podman run -d --name pg -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:16

## /ephemeral — scratch shared with the host
/ephemeral is a read-write directory shared with the host and OUTSIDE the git
repo; nothing in it is part of the project. Use it to hand work to the host:
drop a probe/test script there and ask the user to run it on the host, which
has an unfiltered network this container does not, with:
    cd "\$(aibox ephemeral)" && ./yourscript
Files you create in /ephemeral are the user's on the host, and theirs are
yours here. It is wiped clean at the start of every session, so treat it as
transient. It is the right place for anything that needs to run outside the
sandbox — never work around the egress policy from inside.

## Tools (prefer these over defaults)
search  rg  fd  ast-grep     view  bat  tree  less     data  jq  yq(YAML)
net     curl  nc  openssl  expect                       vcs   git(read-only)
k8s     oc  kubectl          secrets  bao(OpenBao)       shells bash pwsh lua
iac     ansible ansible-lint (own venv; galaxy→~/.cache/ansible)
build   go(gopls,staticcheck,dlv,goimports)  python(uv,ruff,mypy,pytest) node
arch    tar  bzip2  unzip
versions: go $GO_V · node $NODE_V · python $PYTHON_V · oc $OC_V · ansible $ANSIBLE_V
· pwsh $PWSH_V · lua $LUA_V · yq $YQ_V · bao $BAO_V · openssl $OPENSSL_V
· rg $RG_V · fd $FD_V · bat $BAT_V
Caches under ~/.cache persist across runs; re-downloading is unnecessary.

## Writable paths
/work (except /work/.git) · /work/.aibox · /ephemeral (shared w/ host) · /tmp
· ~/.cache · ~/.ssh
EOF

# The budget gate. The full notes (image + policy + project) land in the
# agent's context every session — a recurring token cost, not free
# documentation. 5120 bytes total; the image layer gets 4096, leaving room for
# the run-time policy section and a short project layer. Exceeding it fails
# the image build, here, loudly.
size="$(wc -c < "$SHARE/ainotes-image.md")"
if [ "$size" -gt 4096 ]; then
    echo "ainotes image layer is $size bytes (budget 4096) — trim it" >&2
    exit 1
fi
