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
SKOPEO_V="$(ver skopeo --version)"
GH_V="$(ver gh --version)"
GLAB_V="$(ver glab --version)"

cat > "$SHARE/image-manifest.json" <<EOF
{
  "schemaVersion": 1,
  "image": { "profile": "${AIBOX_PROFILE:-base}", "release": "${AIBOX_RELEASE:-local}" },
  "toolchains": { "go": "$GO_V", "node": "$NODE_V", "python": "$PYTHON_V", "lua": "$LUA_V", "powershell": "$PWSH_V" },
  "assistants": { "claude": "$CLAUDE_V", "codex": "$CODEX_V" },
  "tools": { "ripgrep": "$RG_V", "fd": "$FD_V", "bat": "$BAT_V", "yq": "$YQ_V", "oc": "$OC_V", "ansible-core": "$ANSIBLE_V", "openbao": "$BAO_V", "openssl": "$OPENSSL_V", "skopeo": "$SKOPEO_V", "gh": "$GH_V", "glab": "$GLAB_V" }
}
EOF

# The image layer of the notes. The git-policy section leads on purpose: git
# writes work here, and the assistant must know they are the user's call
# before the first one, not after.
# The network and "no runtime here" sections turn the two things an agent most
# often gets wrong in this environment — a blocked domain read as an outage,
# and trying to start a container that cannot start — into a hand-off to the
# user with the exact command to run.
cat > "$SHARE/ainotes-image.md" <<EOF
# aibox environment notes

## Repository writes — read this first
git works here, writes included — but every commit, push and tag is the
USER's decision. Before each one: show the exact command and the full
message, ask, and wait for a yes. A yes covers that one action only; never
assume it from an earlier one. Identity: see "This session"; if none, ask
for it. A push over https needs a token for that one command:
    GH_TOKEN="\$(creds get NAME)" git -c credential.helper='!gh auth git-credential' push
If "This session" says .git is read-only, do not try: write .aibox/HANDOFF.md
with a suggested commit message and hand the user the host commands.
No AI attribution, ever: no "Co-Authored-By", "Generated with" or mention
of Claude, Claude Code, Codex, an AI or an agent in commit messages, PR/MR
text, issues, comments or release notes. They are the user's own words.

## Forge CLIs — gh (GitHub) and glab (GitLab)
Writes (pr/mr create, comment, review, merge, issue, release) are allowed
under the same rule: show the command, ask, wait for a yes — every time.
Token from /creds, scoped to one command:
    GH_TOKEN="\$(creds get NAME)" gh pr create …
    GITLAB_TOKEN="\$(creds get NAME)" glab mr create …   # GITLAB_HOST if self-hosted

## aibox skill and agent (Claude)
Built in: skill \`aibox\` and agent \`aibox\` (linked into ~/.claude from
/usr/share/aibox/claude). Load the skill, or delegate to the agent, when: a
change is ready to commit/push, a PR/MR is to be opened, a secret from /creds
is needed, a request/registry/relay fails, or something must run on the
host. The agent does not write application code.

## Changelog & releases
Keep CHANGELOG.md (an ordinary, writable file) current under "## Unreleased".
A release is a semver tag (ask first, as for any tag): rename Unreleased to
"## X.Y.Z - YYYY-MM-DD", then
    git tag -a vX.Y.Z -m "vX.Y.Z" && git push --tags

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
Granted relay services: /run/aibox/services.json.

## No container runtime here
No podman/docker. If the project needs services (database, cache, broker, a
compose stack), do NOT try to start them here — hand the user the commands
to run on the host, e.g.:
    podman run -d --name pg -e POSTGRES_PASSWORD=dev -p 5432:5432 postgres:16

## /creds — secrets (encrypted, access-gated)
Credentials are at /creds as encrypted, read-only files (NAME.enc). They are
useless without a passphrase the user exposes only by running, on the host,
'aibox secret allow-access'. Read one ONLY into an env var, never onto the
terminal or into a file:  export TOKEN="\$(creds get NAME)"  ('creds list'
shows names). If access is not granted, 'creds get' fails — ask the user to
grant it; do NOT work around it. NEVER print, log, echo, commit, write into a
test, script or /ephemeral, or send a secret in cleartext. If a secret is ever
exposed by any means, STOP, tell the user it is compromised, and have them
rotate it with 'aibox secret rotate-key' before continuing.

## /ephemeral — scratch shared with the host
/ephemeral is read-write, shared with the host and OUTSIDE the git repo. Use
it to hand work to the host: drop a probe/test script there and ask the user
to run it on the host (unfiltered network) with:
    cd "\$(aibox ephemeral)" && ./yourscript
Its files are shared both ways and wiped at every session start. It is the
place for anything that must run outside the sandbox — never work around the
egress policy from inside.

## Tools (prefer these over defaults)
search  rg  fd  ast-grep     view  bat  tree  less     data  jq  yq(YAML)
net     curl  nc  openssl  expect        vcs  git  gh  glab
k8s     oc  kubectl          secrets  bao(OpenBao)       shells bash pwsh lua
oci     skopeo (registry inspect/copy; no daemon)
iac     ansible ansible-lint (own venv; galaxy→~/.cache/ansible)
build   go(gopls,staticcheck,dlv,goimports)  python(uv,ruff,mypy,pytest,paramiko) node
arch    tar  bzip2  unzip
versions: go $GO_V · node $NODE_V · python $PYTHON_V · oc $OC_V · ansible $ANSIBLE_V
· pwsh $PWSH_V · lua $LUA_V · yq $YQ_V · bao $BAO_V · openssl $OPENSSL_V
· rg $RG_V · fd $FD_V · bat $BAT_V · skopeo $SKOPEO_V · gh $GH_V · glab $GLAB_V
~/.cache persists across runs; re-downloading is unnecessary.

## Writable paths
/work (except /work/.git) · /work/.aibox · /ephemeral (shared w/ host) · /tmp
· ~/.cache · ~/.ssh
EOF

# The budget gate. The full notes (image + policy + project) land in the
# agent's context every session — a recurring token cost, not free
# documentation. 6144 bytes total; the image layer gets 5120, leaving room for
# the run-time policy section and a short project layer. Exceeding it fails
# the image build, here, loudly.
size="$(wc -c < "$SHARE/ainotes-image.md")"
if [ "$size" -gt 5120 ]; then
    echo "ainotes image layer is $size bytes (budget 5120) — trim it" >&2
    exit 1
fi
