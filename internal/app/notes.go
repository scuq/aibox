package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/image"
	"github.com/scuq/aibox/internal/notes"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/runtime"
)

func cmdNotes(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	fs := flag.NewFlagSet("notes", flag.ContinueOnError)
	size := fs.Bool("size", false, "report byte usage against the budget")
	claudeMD := fs.Bool("claude-md", false, "emit the CLAUDE.md include snippet")
	workspace := fs.String("workspace", "", "workspace")
	fs.StringVar(workspace, "w", "", "workspace")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *claudeMD {
		fmt.Print(notes.ClaudeMDSnippet)
		return nil
	}

	ws, err := resolveWorkspace(*workspace, p)
	if err != nil {
		return err
	}
	cfg, err := config.Load(ws)
	if err != nil {
		return err
	}

	if rest := fs.Args(); len(rest) > 0 {
		if len(rest) == 2 && rest[0] == "project" && rest[1] == "init" {
			return scaffoldProjectNotes(p, cfg, ws)
		}
		return fmt.Errorf("unknown notes arguments %v (use: aibox notes [--size|--claude-md] | aibox notes project init)", rest)
	}

	// The image layer is exact only inside a built image; read it from there
	// when podman has one, otherwise say so rather than inventing versions.
	imageLayer := "# aibox environment notes\n(image layer: built into the image — run `ainotes` inside the container,\nor build the image first for the exact version inventory)\n"
	if rt.Available() {
		if exists, _ := rt.ImageExists(ctx, image.Ref(cfg)); exists {
			if out, err := rt.Capture(ctx, "run", "--rm", "--entrypoint", "cat",
				image.Ref(cfg), "/usr/share/aibox/ainotes-image.md"); err == nil {
				imageLayer = out
			}
		}
	}

	full := imageLayer + notes.PolicyLayer(cfg) + notes.ProjectLayer(cfg, ws)
	if *size {
		fmt.Printf("%d bytes of %d budget (%s)\n", len(full), cfg.Notes.MaxBytes,
			budgetVerdict(len(full), cfg.Notes.MaxBytes))
		return notes.CheckBudget(full, cfg.Notes.MaxBytes)
	}
	fmt.Print(full)
	return notes.CheckBudget(full, cfg.Notes.MaxBytes)
}

func budgetVerdict(n, max int) string {
	if n > max {
		return "OVER BUDGET"
	}
	return "ok"
}

func scaffoldProjectNotes(p *output.Printer, cfg config.Config, ws string) error {
	path := filepath.Join(ws, cfg.Notes.Project)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(notes.ProjectScaffold), 0o644); err != nil {
		return err
	}
	p.Good("wrote", "%s", path)
	return nil
}

// cmdHandoff is the host side of the handoff convention: the container cannot
// commit, so it records what it changed in /work/.aibox/ and this command
// turns that into something the user can act on. It prints git commands and
// never executes them. There is no --yes, and there will not be one.
func cmdHandoff(p *output.Printer, args []string) error {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	diff := fs.Bool("diff", false, "show review.patch")
	clear := fs.Bool("clear", false, "remove the handoff artefacts after committing")
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
	dir := filepath.Join(ws, cfg.Git.Handoff)

	if *clear {
		removed := 0
		for _, f := range []string{"HANDOFF.md", "changed.txt", "review.patch"} {
			path := filepath.Join(dir, f)
			if err := os.Remove(path); err == nil {
				removed++
			}
		}
		// Only the empty directory: anything else in there was not ours to take.
		_ = os.Remove(dir)
		p.Info("removed %d handoff artefact(s) from %s", removed, dir)
		return nil
	}

	if *diff {
		patch := filepath.Join(dir, "review.patch")
		data, err := os.ReadFile(patch)
		if err != nil {
			return fmt.Errorf("no %s — the session left no review patch", patch)
		}
		// Through delta when available, for a readable skim; plain otherwise.
		if deltaPath, err := exec.LookPath("delta"); err == nil {
			cmd := exec.Command(deltaPath)
			cmd.Stdin = strings.NewReader(string(data))
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
		fmt.Print(string(data))
		return nil
	}

	handoff := filepath.Join(dir, "HANDOFF.md")
	data, err := os.ReadFile(handoff)
	if err != nil {
		return errors.New("no handoff found — the session left nothing in " + dir)
	}
	fmt.Print(string(data))
	if !strings.HasSuffix(string(data), "\n") {
		fmt.Println()
	}
	fmt.Println("---")
	fmt.Println("To commit (run these yourself; aibox never runs git for you):")
	fmt.Printf("  cd %s\n", ws)
	if _, err := os.Stat(filepath.Join(dir, "changed.txt")); err == nil {
		fmt.Printf("  cat %s   # the changed paths\n", filepath.Join(cfg.Git.Handoff, "changed.txt"))
	}
	fmt.Println("  git add -A            # or stage selectively from the list above")
	fmt.Println("  git commit             # use the suggested message from HANDOFF.md")
	fmt.Printf("  aibox handoff --clear  # then remove %s/\n", cfg.Git.Handoff)
	return nil
}
