# bash completion for aibox.
# Print it with 'aibox completion bash'; install it with 'aibox completion install'.

# Adds the arguments that start with $cur. A literal prefix test, not
# compgen -W: compgen expands its word list.
_aibox_add() {
    local w
    for w in "$@"; do
        if [[ $w == "$cur"* ]]; then
            COMPREPLY+=("$w")
        fi
    done
}

# Adds the first field of each stdin line. Service names come from .aibox.yaml,
# which the agent can write, so a name is data: keep only plain tokens and never
# pass one to anything that expands it.
_aibox_add_names() {
    local w _rest
    while read -r w _rest; do
        if [[ $w =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
            _aibox_add "$w"
        fi
    done
}

_aibox_add_dirs() {
    local d
    while IFS= read -r d; do
        COMPREPLY+=("$d")
    done < <(compgen -d -- "$cur")
    compopt -o filenames 2>/dev/null
}

_aibox() {
    local cur prev cword
    COMPREPLY=()
    cword=$COMP_CWORD
    cur=${COMP_WORDS[cword]}
    prev=${COMP_WORDS[cword-1]:-}
    # readline splits --flag=value at the '='.
    if [[ $cur == "=" ]]; then
        cur=""
    elif [[ $prev == "=" ]]; then
        prev=${COMP_WORDS[cword-2]:-}
    fi

    case "$prev" in
        -w | --workspace)
            _aibox_add_dirs
            return
            ;;
        --egress)
            _aibox_add open proxy
            return
            ;;
        --assistant)
            _aibox_add claude codex claude,codex none
            return
            ;;
        --layer)
            _aibox_add http relay
            return
            ;;
        -i | --image | --memory | --cpus | --ttl)
            return
            ;;
        --ca-certs)
            # Falls through to readline's filename completion.
            return
            ;;
    esac

    local command="" sub="" i word skip=0
    for ((i = 1; i < cword; i++)); do
        word=${COMP_WORDS[i]}
        if ((skip)); then
            skip=0
            continue
        fi
        case "$word" in
            -w | --workspace | -i | --image | --egress | --memory | --cpus | --assistant | --layer | --ttl | --ca-certs)
                skip=1
                ;;
            -* | =) ;;
            *)
                if [[ -z $command ]]; then
                    command=$word
                elif [[ -z $sub ]]; then
                    sub=$word
                fi
                ;;
        esac
    done

    case "$command" in
        "")
            _aibox_add run stop-all net image egress relay devcontainer ephemeral \
                secret notes handoff podman init config status doctor completion \
                version help
            ;;
        run)
            # The first bare word is the assistant; the rest belongs to it.
            if [[ -n $sub ]]; then
                return
            fi
            if [[ $cur == -* ]]; then
                _aibox_add -w --workspace -i --image --ro --egress --allow-pkg \
                    --allow-podman --seed-creds --seed-config --memory --cpus \
                    --no-limits --rebuild --no-cache --no-autobuild --allow-root \
                    --dry-run
            else
                _aibox_add claude codex shell
            fi
            ;;
        stop-all)
            _aibox_add --dry-run
            ;;
        net)
            if [[ -z $sub ]]; then
                _aibox_add up down status
            elif [[ $sub == status ]]; then
                _aibox_add --json
            fi
            ;;
        image)
            if [[ -z $sub ]]; then
                _aibox_add build
            elif [[ $sub == build ]]; then
                _aibox_add -w --workspace -i --image --no-cache --dry-run --ca-certs
            fi
            ;;
        egress)
            case "$sub" in
                "")
                    _aibox_add status start stop reload allow remove deny reset \
                        list denied logs live
                    ;;
                logs) _aibox_add -f --layer ;;
                denied) _aibox_add --json ;;
            esac
            ;;
        relay)
            case "$sub" in
                "")
                    _aibox_add start stop restart status list test logs
                    ;;
                list) _aibox_add --json ;;
                logs) _aibox_add -f ;;
                test)
                    _aibox_add_names < <(command aibox relay list 2>/dev/null | tail -n +2)
                    ;;
            esac
            ;;
        devcontainer)
            case "$sub" in
                "")
                    _aibox_add create here list status stop remove recreate
                    ;;
                create | here)
                    _aibox_add --assistant --force --fresh --rm --dry-run -w --workspace
                    ;;
                list | status | stop | remove | recreate)
                    _aibox_add --json -w --workspace
                    ;;
            esac
            ;;
        ephemeral)
            if [[ -z $sub ]]; then
                _aibox_add shell clear
            fi
            _aibox_add --json -w --workspace
            ;;
        secret)
            case "$sub" in
                "")
                    _aibox_add add list delete allow-access revoke-access revoke-all \
                        rotate-key
                    ;;
                list) _aibox_add --json ;;
                allow-access) _aibox_add --ttl ;;
                delete | rm)
                    _aibox_add_names < <(command aibox secret list 2>/dev/null)
                    ;;
            esac
            ;;
        notes)
            if [[ -z $sub ]]; then
                _aibox_add project --size --claude-md -w --workspace
            elif [[ $sub == project ]]; then
                _aibox_add init
            fi
            ;;
        handoff)
            _aibox_add --diff --clear -w --workspace
            ;;
        podman)
            if [[ -z $sub ]]; then
                _aibox_add enable disable status
            fi
            _aibox_add -w --workspace
            ;;
        init)
            _aibox_add_dirs
            ;;
        config)
            if [[ -z $sub ]]; then
                _aibox_add show
            else
                _aibox_add --json -w --workspace
            fi
            ;;
        status)
            _aibox_add --json -w --workspace
            ;;
        doctor)
            _aibox_add --json --allow-root
            ;;
        completion)
            if [[ -z $sub ]]; then
                _aibox_add bash install
            fi
            ;;
    esac
}

complete -o bashdefault -o default -F _aibox aibox
