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

# Isolate from the user's real image/volume/credentials.
export BCLAUDE_IMAGE="${BCLAUDE_IMAGE:-localhost/bclaude-test:latest}"
export BCLAUDE_VOLUME="${BCLAUDE_VOLUME:-bclaude-test-config}"
export HOST_CREDS="${HOST_CREDS:-/nonexistent/.credentials.json}"

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

# A seeded credential file must be mounted read-only and nowhere else.
tmpcreds="$(mktemp)"; echo '{"x":1}' > "$tmpcreds"
check_contains "host creds mounted read-only" "$tmpcreds:/run/host-claude/.credentials.json:ro" -- \
    env HOST_CREDS="$tmpcreds" "$BCLAUDE" --dry-run
rm -f "$tmpcreds"

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
        check_contains "entrypoint routes unknown args to claude" "claude" -- \
            podman run --rm "$BCLAUDE_IMAGE" --version
        check_contains "config dir is the mounted volume" "/home/claude/.claude" -- \
            "$BCLAUDE" shell -c 'echo $CLAUDE_CONFIG_DIR'
        check_status "run exits cleanly through the wrapper" 0 -- \
            "$BCLAUDE" shell -c 'true'
        check_contains "image carries the recipe label" "org.bclaude.recipe" -- \
            podman image inspect "$BCLAUDE_IMAGE" --format '{{json .Labels}}'
        check_contains "status reports the image as up to date" "up-to-date" -- \
            "$BCLAUDE" status
    else
        notok "image exists after build" "image missing"
    fi

    if [ -n "${BCLAUDE_KEEP:-}" ]; then
        printf '  .... keeping %s and %s (BCLAUDE_KEEP set)\n' "$BCLAUDE_IMAGE" "$BCLAUDE_VOLUME"
    else
        podman volume rm -f "$BCLAUDE_VOLUME" >/dev/null 2>&1 || true
        podman rmi -f "$BCLAUDE_IMAGE" >/dev/null 2>&1 || true
    fi
fi

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
