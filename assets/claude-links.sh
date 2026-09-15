#!/usr/bin/env bash
# Fills the Claude config dir's skills/ and agents/ with symlinks: one per
# entry baked into the image (/usr/share/aibox/claude) and one per entry in
# the host's ~/.claude/skills and ~/.claude/agents, which aibox mounts
# read-only under /run/host-aibox/claude. Run on every start by the entrypoint
# (`aibox run`) and by postStartCommand (devcontainer).
#
# Why links and not a mount over ~/.claude/skills: a read-only bind there
# leaves no way to add the built-in entries next to the host's. The links live
# in the writable config volume; what they point at stays read-only, and a
# skill edited on the host is current immediately.
#
#   claude-links.sh [CONFIG_DIR]
#
# A host entry with the same name as a built-in one wins, so a user can
# override the built-in skill. A real (non-link) file or directory already in
# the volume is left alone and wins over both.
set -uo pipefail

CONFIG_DIR="${1:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}"
# Test knobs: the suite runs this on the host against temp dirs.
BUILTIN="${AIBOX_CLAUDE_BUILTIN:-/usr/share/aibox/claude}"
HOST="${AIBOX_CLAUDE_HOST:-/run/host-aibox/claude}"

for kind in skills agents; do
    dst="$CONFIG_DIR/$kind"
    # A volume that older aibox versions bind-mounted over still holds the
    # empty mountpoint podman created, owned by root, so it is not writable.
    # It is empty, so rmdir works (that needs write access to the parent only).
    if [ -d "$dst" ] && [ ! -L "$dst" ] && [ ! -w "$dst" ]; then
        rmdir "$dst" 2>/dev/null || true
    fi
    if ! mkdir -p "$dst" 2>/dev/null || [ ! -w "$dst" ]; then
        echo "[claude-links] warning: $dst is not writable; built-in $kind not linked" >&2
        continue
    fi
    # Drop the previous start's links, so an entry removed on the host or from
    # the image disappears. Real files are not ours and stay.
    find "$dst" -mindepth 1 -maxdepth 1 -type l -delete 2>/dev/null || true

    # Built-in first, host second: a later link of the same name replaces an
    # earlier one, which is what makes the host win.
    for src in "$BUILTIN/$kind"/* "$HOST/$kind"/*; do
        [ -e "$src" ] || continue
        target="$dst/${src##*/}"
        if [ -L "$target" ]; then
            rm -f "$target"
        elif [ -e "$target" ]; then
            continue
        fi
        ln -s "$src" "$target" 2>/dev/null \
            || echo "[claude-links] warning: cannot link $target" >&2
    done
done
exit 0
