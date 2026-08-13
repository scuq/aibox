// Package app wires the command surface. Parsing stays thin here; behaviour
// lives in the internal packages so it can be tested without a CLI.
package app

import (
	"context"
	"fmt"
	"os"

	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/runtime"
)

// Version is the aibox version, overridable at link time
// (-ldflags "-X github.com/scuq/aibox/internal/app.Version=v0.1.0").
var Version = "0.1.0-dev"

const usage = `aibox — controlled, disposable AI development environments.

USAGE
    aibox <command> [options] [args...]

COMMANDS
    run claude|codex|shell [args...]
                      Ephemeral session on a workspace (--rm; nothing persists
                      except the named auth/config/cache volumes)
    net <sub>         Stand up the persistent egress topology: up | down |
                      status. 'up' creates the aibox-internal (no route out)
                      and aibox-egress networks, starts squid and the relay on
                      both, and leaves them running so every 'aibox run
                      --egress proxy' and devcontainer reuses them
    image build       Build the local image from the embedded recipe
    egress <sub>      Egress proxy control: status | start | stop | reload |
                      allow <domain..> | deny <domain..> | list | denied | logs [-f]
    relay <sub>       Service relay control (§8): start | stop | restart |
                      status | list [--json] | test <service> | logs [-f].
                      Named TCP services from the config reachable by the
                      workload — the listener port is the destination, so the
                      agent cannot redirect it. Requires egress proxy mode
    devcontainer <sub>
                      create [--assistant claude|codex|claude,codex|none]
                      [--fresh] [--force] writes .devcontainer/devcontainer.json;
                      here does the same scoped to the current git repo's root;
                      list | status | stop | remove | recreate manage the
                      containers VS Code created (label-scoped, never by name)
    ephemeral         Print the host path of the /ephemeral scratch mount
                      (cd "$(aibox ephemeral)"); shell opens a shell there,
                      clear empties it. Shared with the container, outside the
                      git repo — a drop point for scripts run on the host
    notes             Print the environment notes (--size | --claude-md |
                      project init). Inside the container: 'ainotes'
    handoff           Print the session's HANDOFF.md and the exact git commands
                      to run — printed, never executed (--diff | --clear)
    init              Write a .aibox.yaml skeleton into the workspace
    config show       Print the resolved configuration (--json for JSON)
    status            Show image, volume and egress state for this workspace
    doctor            Check that podman and the host are set up correctly
    version           Print the aibox version
    help              This text

RUN OPTIONS (before the assistant name)
    -w, --workspace DIR   Host directory to mount at /work        (default: $PWD)
    -i, --image REF       Image to run          (default: localhost/aibox:latest)
        --ro              Mount the workspace read-only
        --egress MODE     open or proxy: proxy puts the container on an internal
                          network whose only way out is an allowlisting squid
                          sidecar. 'aibox egress denied' shows what was blocked;
                          git over SSH does not work in this mode
        --allow-pkg       Enable sudo apt-get inside (relaxes two hardening flags)
        --seed-creds      Copy the host's assistant login into the auth volume,
                          replacing any login already there. Off by default: the
                          container logs in on its own, so the host refresh token
                          is never exposed. Also the way to replace a stale token
        --seed-config     Copy host Claude settings.json + statusline in
        --memory SIZE     Memory cap, e.g. 8g; "none" disables      (default: 4g)
        --cpus N          CPU cap; "none" disables                  (default: 2)
        --no-limits       Shorthand for --memory none --cpus none
        --rebuild         Rebuild the image before running
        --no-autobuild    Fail instead of building a missing/stale image
        --allow-root      Permit rootful podman. Refused by default: rootless is
                          what keeps a container escape from becoming host root
        --dry-run         Print the podman command instead of running it

GIT POLICY
    Inside the container, .git is mounted read-only: git write commands (add,
    commit, push, checkout, ...) cannot work and fail loudly. The agent records
    what it changed in .aibox/HANDOFF.md; you commit on the host. Read-only
    git (status, diff, log, show, blame) works normally.

ENVIRONMENT
    Only AIBOX_-prefixed variables are read: AIBOX_WORKSPACE, AIBOX_IMAGE,
    AIBOX_EGRESS, AIBOX_EGRESS_SUBNET, AIBOX_PROXY_IMAGE, AIBOX_MEMORY,
    AIBOX_CPUS, AIBOX_RO=1, AIBOX_ALLOW_PKG=1, AIBOX_ALLOW_ROOT=1,
    AIBOX_AUTOBUILD=0. ANTHROPIC_API_KEY / OPENAI_API_KEY are forwarded into
    the container (by name, never by value) when set.

CONFIGURATION
    ~/.config/aibox/config.yaml, then ./.aibox.yaml, then environment, then
    flags. 'aibox config show' prints the resolved result.
`

// Main runs the CLI and returns the process exit code.
func Main(args []string) int {
	p := output.NewStderr()
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 0
	}

	ctx := context.Background()
	rt := runtime.NewPodman()

	var err error
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprint(os.Stdout, usage)
	case "version", "--version":
		fmt.Printf("aibox %s\n", Version)
	case "run":
		return cmdRun(ctx, p, rt, args[1:])
	case "image":
		err = cmdImage(ctx, p, rt, args[1:])
	case "egress":
		err = cmdEgress(ctx, p, rt, args[1:])
	case "net":
		err = cmdNet(ctx, p, rt, args[1:])
	case "relay":
		err = cmdRelay(ctx, p, rt, args[1:])
	case "devcontainer":
		err = cmdDevcontainer(ctx, p, rt, args[1:])
	case "ephemeral":
		err = cmdEphemeral(p, args[1:])
	case "notes":
		err = cmdNotes(ctx, p, rt, args[1:])
	case "handoff":
		err = cmdHandoff(p, args[1:])
	case "config":
		err = cmdConfig(args[1:])
	case "init":
		err = cmdInit(p, args[1:])
	case "status":
		err = cmdStatus(ctx, p, rt, args[1:])
	case "doctor":
		return cmdDoctor(ctx, p, rt, args[1:])
	default:
		p.Error("unknown command %q — see 'aibox help'", args[0])
		return 1
	}
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	return 0
}
