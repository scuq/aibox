#!/usr/bin/env bash
# bclaude test suite. No dependencies beyond bash + podman.
#
#   tests/run-tests.sh          all tests
#   tests/run-tests.sh --fast   skip the tests that build/run the image
#
# Exit code 0 = all passed.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BCLAUDE="${BCLAUDE:-$HERE/../bclaude}"
FAST=""
[ "${1:-}" = "--fast" ] && FAST=1

# Isolate from the user's real image/volumes/credentials. These are set, not
# defaulted: inheriting them would point the suite — including the cleanup that
# removes volumes and the image at the end — at whatever the caller happens to
# have exported. The rest are cleared because they change what the dry-run
# assertions expect, or would leak a real token into a test container.
unset BCLAUDE_WORKSPACE BCLAUDE_VOLUME BCLAUDE_AUTH_VOLUME BCLAUDE_IMAGE \
      BCLAUDE_VOLUME_PER_PROJECT BCLAUDE_RO BCLAUDE_AUTOBUILD BCLAUDE_GIT_CONFIG \
      BCLAUDE_ALLOW_ROOT WORKSPACE VOLUME AUTH_VOLUME IMAGE CLAUDE_VERSION \
      MEMORY CPUS TMPFS_SIZE ALLOW_PKG SEED_CONFIG SEED_CREDS HOST_CREDS \
      ANTHROPIC_API_KEY BCLAUDE_EGRESS BCLAUDE_EGRESS_SUBNET BCLAUDE_PROXY_IMAGE \
      BCLAUDE_PROXY_LOG_DRIVER BCLAUDE_ALLOWLIST
export BCLAUDE_IMAGE=localhost/bclaude-test:latest
export BCLAUDE_VOLUME=bclaude-test-config
export BCLAUDE_AUTH_VOLUME=bclaude-test-auth
export HOST_CREDS=/nonexistent/.credentials.json

# Belt and braces for the cleanup: only ever remove things named like ours.
rm_test_volume() {
    case "$1" in
        bclaude-test-*) podman volume rm -f "$1" >/dev/null 2>&1 || true ;;
        *) printf '  !! refusing to remove volume %s (not a test volume)\n' "$1" ;;
    esac
}
rm_test_image() {
    case "$1" in
        *bclaude-test*) podman rmi -f "$1" >/dev/null 2>&1 || true ;;
        *) printf '  !! refusing to remove image %s (not a test image)\n' "$1" ;;
    esac
}

pass=0; fail=0
ok()   { printf '  ok   %s\n' "$1"; pass=$((pass+1)); }
notok() { printf '  FAIL %s\n     %s\n' "$1" "${2:-}"; fail=$((fail+1)); }

# check <name> <expected-substring> -- <command...>
check_contains() {
    local name="$1" want="$2"; shift 3
    local out; out="$("$@" 2>&1)"
    case "$out" in
        *"$want"*) ok "$name" ;;
        *) notok "$name" "expected to contain: $want
     got: $(printf '%s' "$out" | head -c 300)" ;;
    esac
}

check_not_contains() {
    local name="$1" unwanted="$2"; shift 3
    local out; out="$("$@" 2>&1)"
    case "$out" in
        *"$unwanted"*) notok "$name" "did not expect: $unwanted" ;;
        *) ok "$name" ;;
    esac
}

check_status() {
    local name="$1" want="$2"; shift 3
    "$@" >/dev/null 2>&1
    local got=$?
    [ "$got" = "$want" ] && ok "$name" || notok "$name" "exit $got, wanted $want"
}

section() { printf '\n== %s\n' "$1"; }

# ---------------------------------------------------------------------------
section "static checks"

check_status "script is executable" 0 -- test -x "$BCLAUDE"
check_status "script parses" 0 -- bash -n "$BCLAUDE"

if command -v shellcheck >/dev/null 2>&1; then
    check_status "shellcheck clean" 0 -- shellcheck -S warning "$BCLAUDE"
else
    printf '  skip shellcheck (not installed)\n'
fi

# The embedded files must themselves be valid.
check_status "embedded entrypoint parses" 0 -- \
    bash -c "\"$BCLAUDE\" show entrypoint | bash -n"
check_contains "embedded containerfile has a FROM" "FROM docker.io/library/node" -- \
    "$BCLAUDE" show containerfile
check_contains "embedded containerfile copies the entrypoint" "COPY entrypoint.sh" -- \
    "$BCLAUDE" show containerfile
check_contains "embedded containerfile pre-owns /vscode for dev containers" \
    "/vscode" -- "$BCLAUDE" show containerfile
check_status "script is self-contained (no Containerfile/entrypoint.sh needed)" 0 -- \
    bash -c "cd / && \"$BCLAUDE\" --workspace / --dry-run show containerfile"

section "cli surface"

check_status "--version" 0 -- "$BCLAUDE" --version
check_contains "--version prints a version" "bclaude 1." -- "$BCLAUDE" --version
check_status "help" 0 -- "$BCLAUDE" help
check_contains "help lists doctor" "doctor" -- "$BCLAUDE" --help
check_status "bad show target fails" 1 -- "$BCLAUDE" show nonsense
check_status "missing workspace fails" 1 -- \
    "$BCLAUDE" --workspace /no/such/dir --dry-run
check_status "refuses to mount /" 1 -- "$BCLAUDE" --workspace / --dry-run

section "run invocation (--dry-run)"

check_contains "mounts the workspace at /work" ":/work" -- \
    "$BCLAUDE" --workspace "$HERE" --dry-run
check_contains "workspace flag is honoured" "$HERE:/work" -- \
    "$BCLAUDE" --workspace "$HERE" --dry-run
check_not_contains "workspace is writable by default" ":/work:ro" -- "$BCLAUDE" --dry-run
check_contains "--ro mounts the workspace read-only" ":/work:ro" -- "$BCLAUDE" --ro --dry-run
check_contains "--ro says so" "read-only" -- "$BCLAUDE" --ro --dry-run
check_contains "BCLAUDE_RO=1 mounts read-only" ":/work:ro" -- \
    env BCLAUDE_RO=1 "$BCLAUDE" --dry-run
check_contains "--ro leaves the config volume writable" "myvol:/home/claude/.claude" -- \
    "$BCLAUDE" --ro --volume myvol --dry-run
check_contains "help documents --ro" "--ro " -- "$BCLAUDE" help
check_contains "hardening on by default" "--cap-drop=ALL" -- "$BCLAUDE" --dry-run
check_contains "no-new-privileges on by default" "no-new-privileges" -- "$BCLAUDE" --dry-run
check_not_contains "--allow-pkg drops cap-drop" "--cap-drop=ALL" -- \
    "$BCLAUDE" --allow-pkg --dry-run
check_contains "memory limit applied" "--memory 4g" -- "$BCLAUDE" --dry-run
check_not_contains "--no-limits removes caps" "--memory" -- "$BCLAUDE" --no-limits --dry-run
check_contains "--memory 8g honoured" "--memory 8g" -- "$BCLAUDE" --memory 8g --dry-run
check_contains "volume flag honoured" "myvol:/home/claude/.claude" -- \
    "$BCLAUDE" --volume myvol --dry-run
check_contains "image flag honoured" "example.com/img:1" -- \
    "$BCLAUDE" --image example.com/img:1 --dry-run
check_contains "claude flags pass through" " -p hello" -- "$BCLAUDE" --dry-run -p hello
check_contains "unknown claude flags pass through" "--model opus" -- \
    "$BCLAUDE" --dry-run --model opus
check_contains "-- stops bclaude parsing" "--dry-run" -- \
    "$BCLAUDE" --dry-run -- --dry-run
check_contains "shell command runs bash" "latest bash" -- "$BCLAUDE" --dry-run shell
check_contains "run command strips itself" "latest -p x" -- "$BCLAUDE" --dry-run run -p x
check_not_contains "no host creds mounted when absent" "host-claude" -- \
    "$BCLAUDE" --dry-run
check_contains "tmpfs /tmp is nosuid" "nosuid" -- "$BCLAUDE" --dry-run
check_not_contains "--no-git-config drops the gitconfig mount" ".gitconfig" -- \
    "$BCLAUDE" --no-git-config --dry-run
check_contains "run forces claude subcommands through" "latest install" -- \
    "$BCLAUDE" --dry-run run install

section "egress filtering (--dry-run)"

# Enforcement is the network layout: in proxy mode the container joins only the
# internal network, and the proxy env vars point at the sidecar's static IP.
check_not_contains "egress open is the default" "--network" -- "$BCLAUDE" --dry-run
check_contains "--egress proxy joins the internal network" \
    "--network bclaude-internal" -- "$BCLAUDE" --egress proxy --dry-run
check_contains "--egress proxy points HTTPS_PROXY at the sidecar" \
    "HTTPS_PROXY=http://10.199.0.2:3128" -- "$BCLAUDE" --egress proxy --dry-run
check_contains "--egress proxy sets the lowercase form too" \
    "https_proxy=http://10.199.0.2:3128" -- "$BCLAUDE" --egress proxy --dry-run
check_contains "--egress proxy keeps loopback unproxied" "NO_PROXY=localhost" -- \
    "$BCLAUDE" --egress proxy --dry-run
check_contains "BCLAUDE_EGRESS=proxy works as well" "--network bclaude-internal" -- \
    env BCLAUDE_EGRESS=proxy "$BCLAUDE" --dry-run
check_contains "the subnet is overridable" "HTTPS_PROXY=http://10.42.0.2:3128" -- \
    env BCLAUDE_EGRESS_SUBNET=10.42.0.0/24 "$BCLAUDE" --egress proxy --dry-run
check_status "an unknown egress mode is refused" 1 -- "$BCLAUDE" --egress nftables --dry-run
check_contains "hardening stays on in proxy mode" "--cap-drop=ALL" -- \
    "$BCLAUDE" --egress proxy --dry-run
check_contains "proxy mode warns about git over ssh" "git over SSH" -- \
    "$BCLAUDE" --egress proxy --dry-run
check_contains "help documents --egress" "--egress" -- "$BCLAUDE" help
check_contains "help documents the egress command" "egress [SUB]" -- "$BCLAUDE" help

# The embedded squid config: deny by default, allowlist file, stdout audit log.
check_contains "squid config denies by default" "http_access deny all" -- \
    "$BCLAUDE" show squid-conf
check_contains "squid config reads the allowlist" "/etc/squid/bclaude-allowlist" -- \
    "$BCLAUDE" show squid-conf
check_contains "squid config logs each request to stdout" "stdio:/dev/stdout" -- \
    "$BCLAUDE" show squid-conf
check_contains "squid config only serves the internal subnet" "10.199.0.0/24" -- \
    "$BCLAUDE" show squid-conf
check_contains "squid config never caches" "cache deny all" -- "$BCLAUDE" show squid-conf

# The default allowlist covers what Claude Code itself needs.
check_contains "default allowlist has the API host" "api.anthropic.com" -- \
    "$BCLAUDE" show allowlist
check_contains "default allowlist covers claude.ai login" "claude.ai" -- \
    "$BCLAUDE" show allowlist
check_contains "default allowlist covers the oauth host" "platform.claude.com" -- \
    "$BCLAUDE" show allowlist
check_contains "default allowlist explains the syntax" "dstdomain" -- \
    "$BCLAUDE" show allowlist

section "devcontainer"

dcdir="$(mktemp -d)"
check_status "devcontainer writes the file" 0 -- "$BCLAUDE" -w "$dcdir" devcontainer
check_status "the file exists and is not empty" 0 -- \
    test -s "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer uses the bclaude image" "localhost/bclaude-test:latest" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer mounts the config volume" \
    "source=bclaude-test-config,target=/home/claude/.claude,type=volume" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer mounts the auth volume" \
    "source=bclaude-test-auth,target=/home/claude/.claude-auth,type=volume" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer keeps the hardening" '"--cap-drop=ALL"' -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer keeps the userns mapping" "keep-id:uid=1000,gid=1000" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "devcontainer mounts the workspace at /work" "target=/work,type=bind" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_not_contains "no proxy plumbing without --egress proxy" "HTTPS_PROXY" -- \
    cat "$dcdir/.devcontainer/devcontainer.json"
check_status "an existing file is not overwritten" 1 -- "$BCLAUDE" -w "$dcdir" devcontainer
check_status "--force overwrites it" 0 -- "$BCLAUDE" -w "$dcdir" devcontainer --force
# Options after the command would be silently ignored — refuse them instead,
# and say where they belong.
check_status "trailing options are refused" 1 -- \
    "$BCLAUDE" -w "$dcdir" devcontainer --egress proxy
check_contains "the refusal says where options go" "before the command" -- \
    "$BCLAUDE" -w "$dcdir" devcontainer --egress proxy
"$BCLAUDE" -w "$dcdir" --egress proxy devcontainer --force >/dev/null 2>&1
check_contains "--egress proxy joins the internal network in the devcontainer" \
    '"--network=bclaude-internal"' -- cat "$dcdir/.devcontainer/devcontainer.json"
check_contains "--egress proxy sets the proxy env in the devcontainer" \
    '"HTTPS_PROXY"' -- cat "$dcdir/.devcontainer/devcontainer.json"
if command -v python3 >/dev/null 2>&1; then
    check_status "the generated file is valid json once comments are stripped" 0 -- \
        bash -c "sed 's|^\s*//.*||' '$dcdir/.devcontainer/devcontainer.json' \
                 | python3 -c 'import json,sys; json.load(sys.stdin)'"
else
    printf '  skip json validation (python3 not installed)\n'
fi
check_contains "help documents devcontainer" "devcontainer" -- "$BCLAUDE" help
rm -rf "$dcdir"

section "SELinux relabelling (--dry-run)"

# On an SELinux-enforcing host every bind mount needs the relabel option. The
# option belongs in the option list after a colon: ":z" on a mount that has no
# other options, ",z" appended on one that does. Getting that wrong by pasting
# ",z" onto an optionless mount is silent and severe — podman reads "/work,z" as
# the destination path, so the workspace mounts somewhere nobody looks and the
# empty /work from the image is what Claude sees. Same for both volumes, which
# is the login not persisting. A stub getenforce stands in for such a host.
selinuxbin="$(mktemp -d)"
printf '#!/bin/sh\necho Enforcing\n' > "$selinuxbin/getenforce"
chmod +x "$selinuxbin/getenforce"
enforcing() { PATH="$selinuxbin:$PATH" "$@"; }

check_contains "workspace gets :z when enforcing" ":/work:z" -- \
    enforcing "$BCLAUDE" --dry-run
check_contains "config volume gets :z when enforcing" \
    "bclaude-test-config:/home/claude/.claude:z" -- enforcing "$BCLAUDE" --dry-run
check_contains "auth volume gets :z when enforcing" \
    "bclaude-test-auth:/home/claude/.claude-auth:z" -- enforcing "$BCLAUDE" --dry-run

# The bug these three guard against: a relabel option concatenated onto the
# destination path instead of into the option list.
check_not_contains "workspace destination is not /work,z" '/work\,z' -- \
    enforcing "$BCLAUDE" --dry-run
check_not_contains "config volume destination is not .claude,z" '.claude\,z' -- \
    enforcing "$BCLAUDE" --dry-run
check_not_contains "auth volume destination is not .claude-auth,z" '.claude-auth\,z' -- \
    enforcing "$BCLAUDE" --dry-run

# The workdir has to be the directory the workspace actually mounted on.
check_contains "workdir matches the workspace mount when enforcing" "--workdir /work " -- \
    enforcing "$BCLAUDE" --dry-run

# A mount that already has options appends with a comma instead.
check_contains "read-only workspace appends the relabel" ':/work:ro\,z' -- \
    enforcing "$BCLAUDE" --ro --dry-run
selinuxcreds="$(mktemp)"; echo '{"x":1}' > "$selinuxcreds"
check_contains "read-only seed mounts append the relabel" 'credentials.json:ro\,z' -- \
    enforcing env HOST_CREDS="$selinuxcreds" "$BCLAUDE" --seed-creds --dry-run

# And nothing is relabelled on a host that is not enforcing.
check_not_contains "no relabel option without SELinux" ":z" -- "$BCLAUDE" --dry-run
check_contains "workspace mounts bare without SELinux" ":/work " -- "$BCLAUDE" --dry-run
rm -rf "$selinuxbin"; rm -f "$selinuxcreds"

section "userns mapping"

# The image runs as claude, uid 1000. Plain --userns=keep-id maps the caller to
# their own numeric uid instead, which for any host uid other than 1000 leaves
# the process unable to write a workspace owned by the caller.
check_contains "host user is mapped onto the container's uid 1000" \
    '--userns=keep-id:uid=1000\,gid=1000' -- "$BCLAUDE" --dry-run
check_not_contains "plain keep-id is not used" "--userns=keep-id " -- "$BCLAUDE" --dry-run

section "secret handling"

# The key is forwarded by name so podman imports it from our environment. With
# the value inline it would show up in ps output on a shared host, and --dry-run
# would print it for pasting into a bug report.
check_contains "the api key is forwarded by name, with no value attached" \
    "--env ANTHROPIC_API_KEY " -- \
    env ANTHROPIC_API_KEY=sk-ant-test-canary "$BCLAUDE" --dry-run
check_not_contains "the api key value never reaches podman's argv" "sk-ant-test-canary" -- \
    env ANTHROPIC_API_KEY=sk-ant-test-canary "$BCLAUDE" --dry-run

section "generic env vars are not inherited"

# IMAGE, WORKSPACE and VOLUME are ordinary words that projects and CI export for
# their own purposes, and they decide what gets mounted and what `clean` deletes.
# Only the BCLAUDE_ forms are read. The BCLAUDE_ ones this suite exports have to
# come off for these, or they would mask the fallback being tested; every command
# here only reads, so falling back to the real defaults is harmless.
check_not_contains "a stray IMAGE export is ignored" "evil/img" -- \
    env -u BCLAUDE_IMAGE IMAGE=evil/img:1 "$BCLAUDE" --dry-run
check_contains "a stray IMAGE export falls back to the default" "localhost/bclaude:latest" -- \
    env -u BCLAUDE_IMAGE IMAGE=evil/img:1 "$BCLAUDE" --dry-run
check_not_contains "a stray WORKSPACE export is ignored" "/etc:/work" -- \
    env WORKSPACE=/etc "$BCLAUDE" --dry-run
check_not_contains "a stray VOLUME export is ignored" "evil-vol" -- \
    env -u BCLAUDE_VOLUME VOLUME=evil-vol "$BCLAUDE" --dry-run
check_not_contains "a stray AUTH_VOLUME export is ignored" "evil-auth" -- \
    env -u BCLAUDE_AUTH_VOLUME AUTH_VOLUME=evil-auth "$BCLAUDE" --dry-run
# This is the one that matters most: `clean` runs podman rmi -f on the resolved
# image, and status reports the same resolution without deleting anything.
check_contains "the image clean would delete ignores a stray IMAGE export" \
    "image     : localhost/bclaude:latest" -- \
    env -u BCLAUDE_IMAGE IMAGE=evil/img:1 "$BCLAUDE" status
check_not_contains "the resolved workspace ignores a stray WORKSPACE export" \
    "workspace : /etc" -- env WORKSPACE=/etc "$BCLAUDE" status

section "shared auth volume"

check_contains "the auth volume is mounted" "bclaude-test-auth:/home/claude/.claude-auth" -- \
    "$BCLAUDE" --dry-run
check_contains "--auth-volume is honoured" "myauth:/home/claude/.claude-auth" -- \
    "$BCLAUDE" --auth-volume myauth --dry-run
check_contains "the auth volume is separate from the config volume" \
    "bclaude-test-config:/home/claude/.claude " -- "$BCLAUDE" --dry-run
check_contains "per-project volumes keep the shared auth volume" \
    "bclaude-test-auth:/home/claude/.claude-auth" -- \
    env -u BCLAUDE_VOLUME "$BCLAUDE" --volume-per-project --dry-run
check_contains "status reports the auth volume" "auth      : bclaude-test-auth" -- \
    "$BCLAUDE" status
check_contains "help documents --auth-volume" "--auth-volume" -- "$BCLAUDE" help

# The entrypoint carries the login between the two volumes.
check_contains "the entrypoint copies the login in from the auth volume" \
    'install -m 600 "$AUTH_CREDS" "$CREDS"' -- "$BCLAUDE" show entrypoint
check_contains "the entrypoint writes the login back out on exit" \
    "trap sync_auth_out EXIT" -- "$BCLAUDE" show entrypoint
check_not_contains "the entrypoint no longer execs, so the trap can run" \
    'exec -- "$@"' -- "$BCLAUDE" show entrypoint

section "login hand-off between overlapping sessions"

# The auth volume is shared, so two sessions can be up at once and both start
# from the same token. Writing the config dir back unconditionally on exit means
# a session that never touched the token can put a spent one back over a newer
# one and cost everybody a re-login. The entrypoint runs here directly, on the
# host, with its two directories pointed at temp dirs — no podman needed.
epfile="$(mktemp)"; "$BCLAUDE" show entrypoint > "$epfile"

# Both starting states are settable, because which of the two directories held a
# token going in is exactly what the entrypoint has to tell apart: a config dir
# holding the only copy has to be migrated out, while a config dir that merely
# received a copy from the volume has nothing to contribute. Echoes what the auth
# volume holds after the session exits. The session's code writing
# $CLAUDE_CONFIG_DIR/.credentials.json stands for claude refreshing the token;
# writing $BCLAUDE_AUTH_DIR/.credentials.json stands for a concurrent session
# refreshing and exiting while this one is still up.
ep_run() {   # ep_run <auth-token> <config-token> <session-code> <cfg|log>
    local d; d="$(mktemp -d)"; mkdir -p "$d/cfg" "$d/auth"
    [ -n "$1" ] && printf '%s' "$1" > "$d/auth/.credentials.json"
    [ -n "$2" ] && printf '%s' "$2" > "$d/cfg/.credentials.json"
    CLAUDE_CONFIG_DIR="$d/cfg" BCLAUDE_AUTH_DIR="$d/auth" HOME="$d" \
        bash "$epfile" bash -c "$3" >"$d/out" 2>&1
    case "$4" in
        cfg) cat "$d/auth/.credentials.json" 2>/dev/null ;;
        log) cat "$d/out" ;;
    esac
    rm -rf "$d"
}
ep_result() { ep_run "$1" "$2" "$3" cfg; }
ep_log()    { ep_run "$1" "$2" "$3" log; }

check_contains "a refreshed token reaches the auth volume" "C1" -- \
    ep_result C0 "" 'printf C1 > "$CLAUDE_CONFIG_DIR/.credentials.json"'
check_contains "a first login reaches the auth volume" "C1" -- \
    ep_result "" "" 'printf C1 > "$CLAUDE_CONFIG_DIR/.credentials.json"'
check_contains "an unchanged session leaves the token alone" "C0" -- \
    ep_result C0 "" 'true'
# The bug: this session never touched the token, so it must not undo the refresh
# another session made while it was running.
check_contains "an unchanged session does not restore a spent token over a newer one" "C1" -- \
    ep_result C0 "" 'printf C1 > "$BCLAUDE_AUTH_DIR/.credentials.json"'
check_not_contains "the spent token is really gone" "C0" -- \
    ep_result C0 "" 'printf C1 > "$BCLAUDE_AUTH_DIR/.credentials.json"'
# Both refreshed: the one that landed in the volume after we started is newer.
check_contains "a concurrent refresh wins over ours" "C2" -- \
    ep_result C0 "" 'printf C1 > "$CLAUDE_CONFIG_DIR/.credentials.json"
                     printf C2 > "$BCLAUDE_AUTH_DIR/.credentials.json"'
check_contains "yielding to a concurrent refresh is reported" "keeping theirs" -- \
    ep_log C0 "" 'printf C1 > "$CLAUDE_CONFIG_DIR/.credentials.json"
                  printf C2 > "$BCLAUDE_AUTH_DIR/.credentials.json"'

# A config volume holding the only copy of a login — one from before the auth
# volume existed, or an existing config volume paired with a fresh
# --auth-volume — has to reach the empty auth volume. `logged_in` promises this:
# it counts a config-only token as being logged in because the next run migrates
# it. Skipping the write because the session "changed nothing" would strand it.
check_contains "a config-only login migrates into an empty auth volume" "C0" -- \
    ep_result "" C0 'true'
check_contains "a config-only login migrates even after a refresh" "C1" -- \
    ep_result "" C0 'printf C1 > "$CLAUDE_CONFIG_DIR/.credentials.json"'
# But it must not overwrite a login another session established in the meantime.
check_contains "migration yields to a login another session just made" "C9" -- \
    ep_result "" C0 'printf C9 > "$BCLAUDE_AUTH_DIR/.credentials.json"'
# Nothing anywhere stays nothing.
check_not_contains "an empty auth volume stays empty with no login at all" "C" -- \
    ep_result "" "" 'true'
rm -f "$epfile"

section "credential seeding"

# Seeding is opt-in: the host token must not be mounted unless asked for, and
# when it is asked for it must be read-only and nowhere else.
tmpcreds="$(mktemp)"; echo '{"x":1}' > "$tmpcreds"
check_not_contains "host creds are not seeded by default" "host-claude" -- \
    env HOST_CREDS="$tmpcreds" "$BCLAUDE" --dry-run
check_contains "--seed-creds mounts host creds read-only" \
    "$tmpcreds:/run/host-claude/.credentials.json:ro" -- \
    env HOST_CREDS="$tmpcreds" "$BCLAUDE" --seed-creds --dry-run
check_contains "SEED_CREDS=1 seeds as well" "/run/host-claude/.credentials.json:ro" -- \
    env HOST_CREDS="$tmpcreds" SEED_CREDS=1 "$BCLAUDE" --dry-run
check_contains "--seed-creds without host creds warns" "no host credentials" -- \
    env HOST_CREDS=/nonexistent/.credentials.json "$BCLAUDE" --seed-creds --dry-run
check_contains "help documents --seed-creds" "--seed-creds" -- "$BCLAUDE" help
check_contains "help says seeding is off by default" "Off by default" -- "$BCLAUDE" help
rm -f "$tmpcreds"

# The first-login hint replaces the seed, so it has to actually say something
# useful. Sourced directly: it is suppressed under --dry-run.
hint() {   # hint [claude args...]
    bash -c '
        eval "$(sed "s/^main \"\$@\"$//" '"$BCLAUDE"')" 2>/dev/null || true
        VOLUME=testvol
        AUTH_VOLUME=testauth
        HOST_CREDS=/nonexistent/.credentials.json
        first_login_hint "$@"
    ' _ "$@"
}
check_contains "first-login hint explains the container login" \
    "log in inside the container" -- hint
check_contains "first-login hint says it is one-time" "one-time step for all your projects" -- hint
check_contains "first-login hint points at the auth volume" "volume 'testauth'" -- hint
check_contains "headless without a login is called out" "no way to log in" -- hint -p hello
check_status "first-login hint succeeds without host creds" 0 -- hint

section "per-project config volumes"

# The suite exports BCLAUDE_VOLUME, which counts as naming the volume by hand,
# so these run with it unset.
novol() { env -u BCLAUDE_VOLUME "$@"; }
vol_name() {   # vol_name <workspace>
    novol "$BCLAUDE" --volume-per-project --workspace "$1" --dry-run 2>/dev/null \
        | tr ' ' '\n' | sed -n 's#^\(bclaude-config[^:]*\):/home/claude/.claude$#\1#p'
}

check_contains "--volume-per-project derives the volume from the workspace" \
    "bclaude-config-tests-" -- novol "$BCLAUDE" --volume-per-project -w "$HERE" --dry-run
check_contains "BCLAUDE_VOLUME_PER_PROJECT=1 does the same" "bclaude-config-tests-" -- \
    novol env BCLAUDE_VOLUME_PER_PROJECT=1 "$BCLAUDE" -w "$HERE" --dry-run
check_contains "an explicit volume wins" "ignored" -- \
    novol "$BCLAUDE" --volume-per-project --volume mine --dry-run
check_contains "an explicit volume is the one used" "mine:/home/claude/.claude" -- \
    novol "$BCLAUDE" --volume-per-project --volume mine --dry-run
check_contains "the shared volume stays the default" "bclaude-config:/home/claude/.claude" -- \
    novol "$BCLAUDE" --dry-run
check_contains "status shows the derived volume" "bclaude-config-tests-" -- \
    novol "$BCLAUDE" --volume-per-project -w "$HERE" status
check_contains "help documents --volume-per-project" "--volume-per-project" -- "$BCLAUDE" help
check_contains "help documents clean --list" "clean --list" -- "$BCLAUDE" help

# Same workspace -> same volume, spelled differently -> still the same volume,
# same basename in a different place -> a different one.
n1="$(vol_name "$HERE")"; n2="$(vol_name "$HERE/../tests")"
[ -n "$n1" ] && [ "$n1" = "$n2" ] \
    && ok "the derived name is stable across spellings of the path" \
    || notok "the derived name is stable across spellings of the path" "'$n1' vs '$n2'"

d1="$(mktemp -d)/proj"; d2="$(mktemp -d)/proj"; mkdir -p "$d1" "$d2"
m1="$(vol_name "$d1")"; m2="$(vol_name "$d2")"
[ -n "$m1" ] && [ "$m1" != "$m2" ] \
    && ok "same basename in two places does not collide" \
    || notok "same basename in two places does not collide" "both '$m1'"
case "$m1" in *-proj-*) ok "the derived name carries the directory name" ;;
    *) notok "the derived name carries the directory name" "got '$m1'" ;; esac
rm -rf "$(dirname "$d1")" "$(dirname "$d2")"

# clean --list/--prune are exercised against stub volumes: a real prune would
# reach into the volumes of whoever is running the suite.
with_stubs() {   # with_stubs <stdin> <function> [args...]
    local answer="$1" fn="$2"; shift 2
    bash -c '
        eval "$(sed "s/^main \"\$@\"$//" '"$BCLAUDE"')" 2>/dev/null || true
        bclaude_volumes() {
            printf "%s\n" bclaude-config-live-aaaa bclaude-config-gone-bbbb bclaude-config-shared-cccc
        }
        all_bclaude_volumes() { bclaude_volumes; }
        volume_workspace() {
            case "$1" in
                *live*) printf "%s" "'"$HERE"'" ;;
                *gone*) printf "%s" /no/such/project ;;
                *)      printf "" ;;
            esac
        }
        volume_has_creds() { case "$1" in *auth*) return 0 ;; *) return 1 ;; esac; }
        podman() { printf "PODMAN %s\n" "$*" >&2; }
        require_podman() { :; }
        image_exists() { return 1; }
        VOLUME=bclaude-config-live-aaaa
        AUTH_VOLUME=bclaude-stub-auth
        fn="$1"; shift
        "$fn" "$@"
    ' _ "$fn" "$@" <<< "$answer"
}

check_contains "list marks the volume in use" "* bclaude-config-live-aaaa" -- \
    with_stubs "" list_volumes
check_contains "list shows the auth volume and its login state" \
    "bclaude-stub-auth (logged in)" -- with_stubs "" list_volumes
check_not_contains "list does not put the auth volume among the config volumes" \
    "* bclaude-stub-auth" -- with_stubs "" list_volumes
check_contains "list names the project of a per-project volume" "$HERE" -- \
    with_stubs "" list_volumes
check_contains "list calls out a project that is gone" "gone" -- with_stubs "" list_volumes
check_contains "list marks an unlabelled volume as shared" "(shared)" -- \
    with_stubs "" list_volumes

check_contains "prune asks before removing anything" "Remove them" -- \
    with_stubs n prune_volumes
check_not_contains "prune does not claim to remove credentials" "credentials" -- \
    with_stubs n prune_volumes
check_contains "answering no keeps the volume" "kept" -- with_stubs n prune_volumes
check_not_contains "answering no removes nothing" "removed volume" -- \
    with_stubs n prune_volumes
check_contains "answering yes removes the stale volume" \
    "removed volume bclaude-config-gone-bbbb" -- with_stubs y prune_volumes
check_contains "--yes skips the prompt" "removed volume" -- \
    with_stubs "" prune_volumes --yes
check_not_contains "--yes still does not prompt" "Remove them" -- \
    with_stubs "" prune_volumes --yes
check_not_contains "prune leaves a live project alone" "live-aaaa" -- \
    with_stubs y prune_volumes
check_not_contains "prune never touches a shared volume" "shared-cccc" -- \
    with_stubs y prune_volumes

# --all has to cover every config volume, not just the one for this directory.
check_contains "clean --all names every config volume before asking" \
    "bclaude-config-gone-bbbb" -- with_stubs n do_clean --all
check_contains "clean --all also names the current one" \
    "bclaude-config-live-aaaa" -- with_stubs n do_clean --all
check_contains "clean --all shows which project a volume belongs to" "$HERE" -- \
    with_stubs n do_clean --all
check_contains "clean --all asks about the login separately" "logs you out" -- \
    with_stubs n do_clean --all
check_contains "answering no to clean --all keeps the config volumes" \
    "kept the config volumes" -- with_stubs n do_clean --all
check_contains "answering yes to clean --all removes all of them" \
    "removed volume bclaude-config-shared-cccc" -- with_stubs y do_clean --all
check_not_contains "plain clean removes no volume" "removed volume" -- \
    with_stubs "" do_clean
check_contains "plain clean says where to look" "clean --list" -- with_stubs "" do_clean

section "rootless requirement"

# require_rootless is exercised by sourcing the script with main() stubbed out
# and id -u faked, so the guard can be tested without actually being root.
root_guard() {   # root_guard <allow-root:0|1>
    ALLOW_ROOT_ARG="$1" bash -c '
        eval "$(sed "s/^main \"\$@\"$//" '"$BCLAUDE"')" 2>/dev/null || true
        id() { [ "${1:-}" = "-u" ] && { echo 0; return; }; command id "$@"; }
        [ "$ALLOW_ROOT_ARG" = 1 ] && ALLOW_ROOT=1 || ALLOW_ROOT=""
        require_rootless
    '
}
check_status "refuses to run as root by default" 1 -- root_guard 0
check_contains "root refusal explains why" "designed for rootless podman" -- root_guard 0
check_status "--allow-root overrides the refusal" 0 -- root_guard 1
check_contains "--allow-root is accepted as a flag" ":/work" -- \
    "$BCLAUDE" --allow-root --dry-run
check_contains "help documents --allow-root" "--allow-root" -- "$BCLAUDE" help

section "install"

instdir="$(mktemp -d)"
check_status "install copies the script" 0 -- "$BCLAUDE" install "$instdir"
check_status "installed copy is executable" 0 -- test -x "$instdir/bclaude"
check_contains "installed copy runs standalone" "bclaude 1." -- "$instdir/bclaude" --version
check_contains "install reports the destination" "$instdir/bclaude" -- \
    "$BCLAUDE" install "$instdir"
rm -rf "$instdir"

# The curl|bash path: no script file on disk, so install must re-fetch it.
if command -v curl >/dev/null 2>&1; then
    pipedir="$(mktemp -d)"
    check_contains "piped install fetches and installs" "installed" -- \
        env BCLAUDE_URL="file://$(cd "$(dirname "$BCLAUDE")" && pwd)/$(basename "$BCLAUDE")" \
        bash -c "cat '$BCLAUDE' | bash -s -- install '$pipedir'"
    check_status "piped install produced a working script" 0 -- "$pipedir/bclaude" --version
    check_contains "piped install rejects a bad download" "not the bclaude script" -- \
        env BCLAUDE_URL="file://$pipedir/junk" \
        bash -c "echo nonsense > '$pipedir/junk'; cat '$BCLAUDE' | bash -s -- install '$pipedir/x'"
    rm -rf "$pipedir"
else
    printf '  skip piped install (curl not installed)\n'
fi

section "build arguments (--dry-run)"

check_contains "build passes the claude version" "CLAUDE_VERSION=2.1.218" -- \
    "$BCLAUDE" --claude-version 2.1.218 --dry-run build
check_contains "build stamps the recipe label" "org.bclaude.recipe=" -- \
    "$BCLAUDE" --dry-run build
check_contains "--no-cache forwarded" "--no-cache" -- "$BCLAUDE" --dry-run --no-cache build

section "environment"

if ! command -v podman >/dev/null 2>&1; then
    printf '  skip podman tests (podman not installed)\n'
elif [ -n "$FAST" ]; then
    printf '  skip build/run tests (--fast)\n'
else
    check_status "doctor succeeds" 0 -- "$BCLAUDE" doctor
    check_contains "doctor reports podman" "podman found" -- "$BCLAUDE" doctor

    printf '  .... building image %s (slow)\n' "$BCLAUDE_IMAGE"
    if "$BCLAUDE" build >/dev/null 2>&1; then
        ok "image builds"
    else
        notok "image builds" "podman build failed"
    fi

    if podman image exists "$BCLAUDE_IMAGE" 2>/dev/null; then
        ok "image exists after build"

        check_contains "claude-code is installed in the image" "Claude Code" -- \
            podman run --rm --entrypoint bash "$BCLAUDE_IMAGE" -lc 'claude --version || claude --help | head -1'
        check_contains "container user is claude with uid 1000" "uid=1000(claude)" -- \
            podman run --rm --entrypoint bash "$BCLAUDE_IMAGE" -lc id
        check_contains "workspace mount lands in /work" "hello-from-host" -- \
            bash -c "printf hello-from-host > '$HERE/.probe' && \"$BCLAUDE\" --workspace '$HERE' shell -c 'cat /work/.probe'; rm -f '$HERE/.probe'"
        check_contains "--ro makes /work unwritable in the container" "Read-only file system" -- \
            bash -c "\"$BCLAUDE\" --ro --workspace '$HERE' shell -c 'touch /work/.rocheck' 2>&1; rm -f '$HERE/.rocheck'"
        check_status "--ro still allows reads and writes elsewhere" 0 -- \
            "$BCLAUDE" --ro --workspace "$HERE" shell -c 'ls /work >/dev/null && touch /tmp/x && touch "$HOME/.claude/x"'
        check_contains "entrypoint routes unknown args to claude" "claude" -- \
            podman run --rm "$BCLAUDE_IMAGE" --version
        check_contains "config dir is the mounted volume" "/home/claude/.claude" -- \
            "$BCLAUDE" shell -c 'echo $CLAUDE_CONFIG_DIR'
        check_status "run exits cleanly through the wrapper" 0 -- \
            "$BCLAUDE" shell -c 'true'
        # The entrypoint runs claude as a child now instead of exec'ing it, so
        # the exit code has to be passed back by hand.
        check_status "a non-zero exit code survives the wrapper" 3 -- \
            "$BCLAUDE" shell -c 'exit 3'
        # Seeding end to end: the entrypoint copies the mounted seed into the
        # volume with mode 600, and it stays there on later runs without it.
        seeddir="$(mktemp -d)"; printf '{"seed":1}' > "$seeddir/.credentials.json"
        check_contains "--seed-creds copies the token into the volume" '{"seed":1}' -- \
            env HOST_CREDS="$seeddir/.credentials.json" "$BCLAUDE" --seed-creds shell -c \
            'cat "$HOME/.claude/.credentials.json"'
        check_contains "seeded credentials are mode 600" "600" -- \
            env HOST_CREDS="$seeddir/.credentials.json" "$BCLAUDE" --seed-creds shell -c \
            'stat -c %a "$HOME/.claude/.credentials.json"'
        check_contains "the login persists without the flag" '{"seed":1}' -- \
            env HOST_CREDS="$seeddir/.credentials.json" "$BCLAUDE" shell -c \
            'cat "$HOME/.claude/.credentials.json"'
        printf '{"seed":2}' > "$seeddir/.credentials.json"
        check_contains "--seed-creds replaces a stale token" '{"seed":2}' -- \
            env HOST_CREDS="$seeddir/.credentials.json" "$BCLAUDE" --seed-creds shell -c \
            'cat "$HOME/.claude/.credentials.json"'
        rm -rf "$seeddir"

        # The login is in the auth volume, so a different config volume — what
        # --volume-per-project gives you — is logged in without seeding again.
        check_contains "a second config volume is already logged in" '{"seed":2}' -- \
            env BCLAUDE_VOLUME=bclaude-test-config2 "$BCLAUDE" shell -c \
            'cat "$HOME/.claude/.credentials.json"'
        # ... and a token refreshed during that session reaches the first one.
        env BCLAUDE_VOLUME=bclaude-test-config2 "$BCLAUDE" shell -c \
            'printf %s "{\"seed\":3}" > "$HOME/.claude/.credentials.json"' >/dev/null 2>&1
        check_contains "a refreshed token reaches the other config volume" '{"seed":3}' -- \
            "$BCLAUDE" shell -c 'cat "$HOME/.claude/.credentials.json"'
        check_contains "the auth volume holds the login, not the config volume" '{"seed":3}' -- \
            bash -c "cat \"\$(podman volume inspect '$BCLAUDE_AUTH_VOLUME' --format '{{.Mountpoint}}')/.credentials.json\""
        rm_test_volume bclaude-test-config2

        check_contains "image carries the recipe label" "org.bclaude.recipe" -- \
            podman image inspect "$BCLAUDE_IMAGE" --format '{{json .Labels}}'
        check_contains "status reports the image as up to date" "up-to-date" -- \
            "$BCLAUDE" status
    else
        notok "image exists after build" "image missing"
    fi

    if [ -n "${BCLAUDE_KEEP:-}" ]; then
        printf '  .... keeping %s, %s and %s (BCLAUDE_KEEP set)\n' "$BCLAUDE_IMAGE" "$BCLAUDE_VOLUME" "$BCLAUDE_AUTH_VOLUME"
    else
        rm_test_volume "$BCLAUDE_VOLUME"
        rm_test_volume "$BCLAUDE_AUTH_VOLUME"
        rm_test_image  "$BCLAUDE_IMAGE"
    fi
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
