#!/usr/bin/env bash
# aibox container entrypoint. Owns the credential hand-off between the shared
# per-assistant auth volume and the per-project config volume, then runs the
# requested command as a child (not exec — see the bottom).
set -euo pipefail

# Which assistant's credential layout applies. "none" (or unset) skips the
# credential logic entirely — a plain shell session has no login to manage.
ASSISTANT="${AIBOX_ASSISTANT:-none}"

# The per-assistant layout. AIBOX_CONFIG_DIR / AIBOX_AUTH_DIR / AIBOX_CRED_FILE
# exist so the credential protocol can be tested on the host by pointing both
# directories at temp dirs — the test suite runs this file directly under bash.
case "$ASSISTANT" in
    claude)
        CONFIG_DIR="${AIBOX_CONFIG_DIR:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}"
        AUTH_DIR="${AIBOX_AUTH_DIR:-$HOME/.claude-auth}"
        CRED_FILE="${AIBOX_CRED_FILE:-.credentials.json}"
        ;;
    codex)
        CONFIG_DIR="${AIBOX_CONFIG_DIR:-${CODEX_HOME:-$HOME/.codex}}"
        AUTH_DIR="${AIBOX_AUTH_DIR:-$HOME/.codex-auth}"
        CRED_FILE="${AIBOX_CRED_FILE:-auth.json}"
        ;;
    *)
        CONFIG_DIR=""
        AUTH_DIR=""
        CRED_FILE=""
        ;;
esac

if [ -n "$CONFIG_DIR" ]; then
    # AIBOX_SEED_DIR is another host-test knob; in the container the seed is
    # always mounted under /run/host-aibox.
    SEED="${AIBOX_SEED_DIR:-/run/host-aibox}/$CRED_FILE"
    CREDS="$CONFIG_DIR/$CRED_FILE"
    AUTH_CREDS="$AUTH_DIR/$CRED_FILE"

    mkdir -p "$CONFIG_DIR"
    chmod 700 "$CONFIG_DIR"

    # The login lives in its own volume, shared by every project, while the
    # config dir around it is per-project. The assistant only ever looks at
    # $CONFIG_DIR, so the login is copied in on the way up and back out on the
    # way down: the token is refreshed during a session, and that refreshed
    # token has to reach the other projects.
    #
    # Direction of truth: the auth volume decides what you start with, the
    # config dir decides what you end with -- but only where the two actually
    # differ, and only if nobody else moved the auth volume underneath us.
    # AUTH_CREDS_IN is what the auth volume held when we started, taken before
    # anything is copied in, and every decision on the way out is a comparison
    # against it:
    #
    #   config ends equal to it    nothing of ours to add, leave the volume alone
    #   volume no longer equals it a session that started with us refreshed and
    #                              exited first; its token is newer, so it stands
    #   otherwise                  publish
    #
    # The first rule is what stops an untouched session putting a spent token
    # back over a live one. The catch it must not swallow: a config dir holding
    # the only copy of a login (a volume from before the auth/config split, or
    # a fresh auth volume alongside an existing config volume) has to reach the
    # empty auth volume. Fingerprinting the volume rather than the config dir
    # is what keeps those two apart -- "the volume gave us nothing and we have
    # something" is a difference, so it publishes.

    # Empty for a missing file, and never non-zero: this runs under `set -e`
    # inside an assignment, where a failing substitution would take the whole
    # session down.
    creds_fingerprint() {
        [ -s "$1" ] || return 0
        { sha256sum < "$1" 2>/dev/null | cut -d' ' -f1; } || true
    }
    AUTH_CREDS_IN=""
    SEEDED=""

    sync_auth_out() {
        { [ -d "$AUTH_DIR" ] && [ -s "$CREDS" ]; } || return 0
        # An explicit --seed-creds is an instruction: it always publishes.
        if [ -z "$SEEDED" ]; then
            # Moved under us by a session that refreshed after we started --
            # which includes it logging in while our auth volume was still empty.
            if [ "$(creds_fingerprint "$AUTH_CREDS")" != "$AUTH_CREDS_IN" ]; then
                echo "[entrypoint] another session refreshed the login; keeping theirs" >&2
                return 0
            fi
            # We end with exactly what the volume gave us: nothing to say.
            [ "$(creds_fingerprint "$CREDS")" = "$AUTH_CREDS_IN" ] && return 0
        fi
        install -m 600 "$CREDS" "$AUTH_CREDS" 2>/dev/null \
            || echo "[entrypoint] warning: could not store the login in $AUTH_DIR" >&2
    }

    if [ -d "$AUTH_DIR" ]; then
        trap sync_auth_out EXIT
        # Before the copy-in, so this is the volume's state, not the config dir's.
        AUTH_CREDS_IN="$(creds_fingerprint "$AUTH_CREDS")"
        [ -s "$AUTH_CREDS" ] && install -m 600 "$AUTH_CREDS" "$CREDS"
    fi

    # Copy the host's credentials in. The seed is mounted (read-only) only when
    # the caller asked for it with --seed-creds, so its presence is the
    # instruction: copy it in, replacing whatever is there — that is how a
    # stale token gets refreshed. Without the flag there is no seed and the
    # container logs in on its own.
    if [ -s "$SEED" ]; then
        install -m 600 "$SEED" "$CREDS"
        SEEDED=1
        echo "[entrypoint] seeded credentials from host" >&2
    fi

    if [ "$ASSISTANT" = "claude" ]; then
        if [ ! -s "$CREDS" ] && [ -z "${ANTHROPIC_API_KEY:-}" ]; then
            echo "[entrypoint] warning: no credentials found; run 'claude' and log in." >&2
        fi

        # With valid credentials present, an interactive launch still shows the
        # login/onboarding flow unless hasCompletedOnboarding is set. Headless
        # (-p) runs skip onboarding, so this only bites interactive sessions.
        # Seed the flag (and a theme, to skip the theme picker) into the
        # config's .claude.json.
        CONFIG_JSON="$CONFIG_DIR/.claude.json"
        if [ -s "$CREDS" ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then
            [ -s "$CONFIG_JSON" ] || echo '{}' > "$CONFIG_JSON"
            tmp="$(mktemp "$CONFIG_DIR/.claude.json.XXXXXX")"
            if jq '.hasCompletedOnboarding = true
                   | .theme = (.theme // "dark")
                   | .lastOnboardingVersion = (.lastOnboardingVersion // "2.1.218")' \
                   "$CONFIG_JSON" > "$tmp" 2>/dev/null; then
                chmod 600 "$tmp"
                mv "$tmp" "$CONFIG_JSON"
            else
                rm -f "$tmp"
                echo "[entrypoint] warning: could not update $CONFIG_JSON" >&2
            fi
        fi

        # Opt-in config seeding (--seed-config on the host mounts these as
        # read-only seed sources under /run/host-aibox). Copied every run so
        # the config reflects the current host config. Host paths in
        # settings.json are rewritten from $HOST_HOME/.claude to the
        # container's $CONFIG_DIR (e.g. the statusline command's script path).
        if [ -s /run/host-aibox/settings.json ]; then
            if [ -n "${HOST_HOME:-}" ]; then
                sed "s#${HOST_HOME}/.claude#${CONFIG_DIR}#g" \
                    /run/host-aibox/settings.json > "$CONFIG_DIR/settings.json"
            else
                cp /run/host-aibox/settings.json "$CONFIG_DIR/settings.json"
            fi
            chmod 600 "$CONFIG_DIR/settings.json"
            echo "[entrypoint] seeded settings.json from host" >&2
        fi
        if [ -s /run/host-aibox/statusline-command.sh ]; then
            install -m 700 /run/host-aibox/statusline-command.sh \
                "$CONFIG_DIR/statusline-command.sh"
        fi
    fi
fi

# Relay client wiring (§8.3). aibox mounts the generated ssh config and the
# services inventory read-only under /run/host-aibox; install them where the
# tools and the agent expect. ~/.ssh/config is 0400 and the directory 0700 —
# ssh refuses a group/other-writable config. The inventory goes to the tmpfs
# /run/aibox alongside the notes.
if [ -s /run/host-aibox/ssh_config ]; then
    mkdir -p "$HOME/.ssh"
    chmod 700 "$HOME/.ssh"
    install -m 400 /run/host-aibox/ssh_config "$HOME/.ssh/config"
fi
if [ -s /run/host-aibox/services.json ] && [ -d /run/aibox ] && [ -w /run/aibox ]; then
    cp /run/host-aibox/services.json /run/aibox/services.json
fi

# Assemble the environment notes: image layer (baked at build, versions from
# the manifest) + policy layer (this run's actual constraints, from the env
# aibox set) + project layer (the repo's own additions, last so a repo can add
# conventions but not delete the git policy). /run/aibox is a small tmpfs the
# host mounts; if it is missing (bare `podman run`) the notes stay at the
# image layer and `ainotes` falls back to it.
NOTES=/run/aibox/ainotes.md
if [ -d /run/aibox ] && [ -w /run/aibox ]; then
    {
        cat /usr/share/aibox/ainotes-image.md 2>/dev/null || true
        # The policy layer states THIS session's actual state; the model is
        # already described in the image layer above, so keep it to status.
        printf '\n## This session\n'
        if [ "${AIBOX_EGRESS_MODE:-open}" = "proxy" ]; then
            printf 'Egress: allowlisted proxy (the Network section above applies).\n'
        else
            printf 'Egress: OPEN/unfiltered — the Network allowlist guidance above does\nnot apply this session; outbound requests are not proxied.\n'
        fi
        if [ "${AIBOX_GIT_HISTORY:-read-only}" = "none" ]; then
            printf 'Git history: unavailable this session (history: none).\n'
        fi
        if [ -s /run/aibox/services.json ]; then
            printf 'Relay services (reach by name, the port is fixed per service):\n'
            # Names only; the backend stays hidden unless a service disclosed it.
            jq -r '.name + (if .aliases and (.aliases|length>0) then " (" + (.aliases|join(", ")) + ")" else "" end)' \
                /run/aibox/services.json 2>/dev/null | sed 's/^/  /' || true
        else
            printf 'Relay services: none configured this session.\n'
        fi
        if [ -s /work/.aibox/ainotes.md ]; then
            printf '\n'
            cat /work/.aibox/ainotes.md
        fi
    } > "$NOTES" 2>/dev/null || true
    [ -e "$HOME/.ainotes" ] || ln -s "$NOTES" "$HOME/.ainotes" 2>/dev/null || true
fi

# aibox passes the command explicitly (`aibox run claude -p hi` arrives here
# as `claude -p hi`), so there is no CMD-reattachment guessing. An empty argv
# falls back to a shell so `podman run` without args is still usable.
if [ $# -eq 0 ]; then
    set -- bash
fi

# Not exec: the login has to be written back to the shared auth volume when
# the session ends (the EXIT trap above), so this stays as pid 1 and the
# command runs as its child, in the foreground, with the terminal.
rc=0
"$@" || rc=$?
exit "$rc"
