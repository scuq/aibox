#!/usr/bin/env bash
# aibox git shim — /usr/local/bin/git, ahead of /usr/bin/git on PATH.
#
# This is agent UX, not security. The enforcement is the read-only .git mount
# (kernel EROFS); the shim exists so a write verb stops with one clear,
# agent-readable explanation instead of a bare filesystem error and three
# turns of --force escalation. It must never fake success: an agent builds a
# multi-step plan on the lie and the failure surfaces somewhere far away.
#
# Everything not on the denied list passes through to the real git untouched.
set -euo pipefail

REAL_GIT="${AIBOX_REAL_GIT:-/usr/bin/git}"

# Debugging escape hatch (`git.shim: off` in aibox config). The read-only
# mount still enforces; this only removes the explanation layer.
if [ "${AIBOX_GIT_SHIM:-}" = "off" ]; then
    exec "$REAL_GIT" "$@"
fi

deny() {
    local verb="$1"
    cat >&2 <<EOF
aibox: \`git $verb\` is not available in this container.

Repository history is mounted read-only. Commits are made by the user on
the host. Record what you changed and why in .aibox/HANDOFF.md, including a
suggested commit message. Read-only git (status, diff, log, show, blame)
works normally. See \`ainotes\`.
EOF
    exit 2
}

# Find the verb: it is not always $1. Global options and their arguments come
# first (`git -C DIR commit`, `git --git-dir=X push`), so walk past them.
# Options that consume a separate argument are listed explicitly; the --opt=val
# forms consume nothing extra.
args=("$@")
verb=""
verb_index=-1
i=0
while [ $i -lt ${#args[@]} ]; do
    a="${args[$i]}"
    case "$a" in
        -C|-c|--git-dir|--work-tree|--namespace|--super-prefix|--exec-path|--config-env|--attr-source)
            i=$((i + 2)) ;;              # option + its argument
        --version|--help|--html-path|--man-path|--info-path)
            exec "$REAL_GIT" "$@" ;;     # terminal options: never a write
        -*)
            i=$((i + 1)) ;;              # e.g. --no-pager, -p, --bare, --git-dir=X
        *)
            verb="$a"
            verb_index=$i
            break ;;
    esac
done

# No verb at all (`git`, `git --no-pager`): let the real git print its usage.
[ -n "$verb" ] || exec "$REAL_GIT" "$@"

case "$verb" in
    # Unconditionally denied write verbs. checkout/switch/restore/stash are
    # collateral damage of the read-only mount (they write the index or refs);
    # fetch/pull/clone are denied because remote-tracking refs are unwritable
    # and because pulling code in is the user's call, not the agent's.
    add|commit|push|rm|mv|merge|rebase|reset|revert|checkout|switch|restore|\
    stash|cherry-pick|am|apply|tag|worktree|gc|prune|fetch|pull|clone|\
    submodule|filter-repo|notes|update-ref|symbolic-ref|hook|update-index)
        deny "$verb"
        ;;
    branch)
        # Listing branches is read-only and allowed; only the mutating flags
        # are stopped.
        for a in "${args[@]:$((verb_index + 1))}"; do
            case "$a" in
                -d|-D|-m|-M|--delete|--move|--copy|-c)
                    deny "branch $a" ;;
            esac
        done
        ;;
    remote)
        # `git remote -v` / `show` are useful and read-only; mutations are not.
        for a in "${args[@]:$((verb_index + 1))}"; do
            case "$a" in
                add|set-url|remove|rm|rename|set-head|set-branches|prune|update)
                    deny "remote $a" ;;
                -*) continue ;;
                *) break ;;   # first non-option, non-mutating subcommand (e.g. show)
            esac
        done
        ;;
    config)
        # Local config writes die on EROFS anyway; --global and --system would
        # write the image's generated ~/.gitconfig or /etc, which must stay
        # exactly as aibox wrote them.
        for a in "${args[@]:$((verb_index + 1))}"; do
            case "$a" in
                --global|--system) deny "config $a" ;;
            esac
        done
        ;;
esac

exec "$REAL_GIT" "$@"
