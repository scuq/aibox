package app

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
)

// cmdEphemeral is the host-side handle on the /ephemeral scratch mount:
//
//	aibox ephemeral            print the host path (cd "$(aibox ephemeral)")
//	aibox ephemeral --json     the path as JSON
//	aibox ephemeral shell      open a shell in it (the closest a CLI gets to cd)
//	aibox ephemeral clear      empty it
//
// A binary cannot change its parent shell's directory, so "cd there" is either
// `cd "$(aibox ephemeral)"` or `aibox ephemeral shell`.
func cmdEphemeral(p *output.Printer, args []string) error {
	sub := ""
	if len(args) > 0 && (args[0] == "shell" || args[0] == "clear") {
		sub = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("ephemeral", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	workspace := fs.String("workspace", "", "workspace")
	fs.StringVar(workspace, "w", "", "workspace")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := resolveWorkspace(*workspace, p)
	if err != nil {
		return err
	}
	cfg, err := config.Load(ws)
	if err != nil {
		return err
	}
	if !cfg.Ephemeral.Enabled {
		return fmt.Errorf("the ephemeral scratch mount is disabled (ephemeral.enabled: false)")
	}
	id, err := project.ID(ws)
	if err != nil {
		return err
	}
	dir := project.EphemeralDir(id)
	// Create it so the path always exists when asked for — the user may reach
	// for it before the first run.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	switch sub {
	case "clear":
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
		p.Info("cleared %s (%d entries)", dir, len(entries))
		return nil
	case "shell":
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		p.Info("opening a shell in %s (this is the host, not the container)", dir)
		cmd := exec.Command(shell)
		cmd.Dir = dir
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	default:
		if *jsonOut {
			return output.JSON(struct {
				Path  string `json:"path"`
				Mount string `json:"mount"`
			}{dir, cfg.Ephemeral.Mount})
		}
		// Just the path on stdout, so `cd "$(aibox ephemeral)"` works.
		fmt.Println(dir)
		return nil
	}
}
