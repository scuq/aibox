package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/runtime"
)

// cmdPodman turns nested podman on or off for one workspace. The state is a
// host-side marker, not a config key: see config.SecurityConfig.AllowNestedPodman
// for why the workspace must not be able to set it.
func cmdPodman(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("podman needs a subcommand: enable | disable | status")
	}
	sub := args[0]
	switch sub {
	case "enable", "disable", "status":
	default:
		return fmt.Errorf("unknown podman subcommand %q (enable | disable | status)", sub)
	}
	fs := flag.NewFlagSet("podman "+sub, flag.ContinueOnError)
	workspace := fs.String("workspace", "", "workspace")
	fs.StringVar(workspace, "w", "", "workspace")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("podman %s takes no positional arguments (got %v)", sub, rest)
	}
	ws, err := resolveWorkspace(*workspace, p)
	if err != nil {
		return err
	}
	projectID, err := project.ID(ws)
	if err != nil {
		return err
	}

	switch sub {
	case "enable":
		if err := config.SetNestedPodman(projectID, true); err != nil {
			return err
		}
		p.Info("nested podman enabled for %s", ws)
		p.Warn("new sessions here run without no-new-privileges and cap-drop=ALL, with /dev/fuse, label=disable and an unmasked /proc; sudo apt-get works in them too")
		p.Info("still rootless: inner containers share the session's network, egress policy and pids/memory/cpu caps")
		p.Info("images persist in volume %s; in proxy mode allow the registry hosts with 'aibox egress allow'", project.PodmanVolumeName(projectID))
	case "disable":
		if err := config.SetNestedPodman(projectID, false); err != nil {
			return err
		}
		p.Info("nested podman disabled for %s; new sessions use the full hardening again", ws)
		p.Info("volume %s is kept; remove it with: podman volume rm %s", project.PodmanVolumeName(projectID), project.PodmanVolumeName(projectID))
	case "status":
		return podmanStatus(ctx, rt, projectID)
	}
	if _, err := os.Stat(filepath.Join(ws, ".devcontainer", "devcontainer.json")); err == nil {
		p.Warn("devcontainer.json is generated and still has the old flags: run 'aibox devcontainer create --force'")
	}
	return nil
}

func podmanStatus(ctx context.Context, rt *runtime.Podman, projectID string) error {
	marker := config.NestedPodmanEnabled(projectID)
	state, source := "off", "default"
	if marker {
		state, source = "on", "aibox podman enable"
	}
	if v, ok := os.LookupEnv(config.EnvAllowPodman); ok {
		source = config.EnvAllowPodman + "=" + v
		state = "off"
		if v == "1" || v == "true" {
			state = "on"
		}
	}
	fmt.Printf("nested podman: %s (%s)\n", state, source)
	name := project.PodmanVolumeName(projectID)
	if rt.Available() {
		if exists, _ := rt.VolumeExists(ctx, name); exists {
			fmt.Printf("volume       : %s\n", name)
		}
	}
	return nil
}
