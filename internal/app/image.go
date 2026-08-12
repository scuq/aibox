package app

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/image"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/runtime"
)

func cmdImage(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("image needs a subcommand: build")
	}
	switch args[0] {
	case "build":
		return cmdImageBuild(ctx, p, rt, args[1:])
	default:
		return fmt.Errorf("unknown image subcommand %q (use: build)", args[0])
	}
}

func cmdImageBuild(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	var o runOptions
	fs := flag.NewFlagSet("image build", flag.ContinueOnError)
	fs.StringVar(&o.workspace, "w", "", "workspace (for .aibox.yaml resolution)")
	fs.StringVar(&o.workspace, "workspace", "", "workspace (for .aibox.yaml resolution)")
	fs.StringVar(&o.imageRef, "i", "", "image tag to build")
	fs.StringVar(&o.imageRef, "image", "", "image tag to build")
	fs.BoolVar(&o.noCache, "no-cache", false, "ignore the layer cache")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the podman command instead of running it")
	caCerts := fs.String("ca-certs", "", "file or directory of CA certificates (*.crt/*.pem) to bake into the image trust store")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := resolveWorkspace(o.workspace, p)
	if err != nil {
		return err
	}
	cfg, err := config.Load(ws)
	if err != nil {
		return err
	}
	applyRunFlags(&cfg, &o)
	if *caCerts != "" {
		cfg.Image.CACertificates = *caCerts
	}
	return buildImage(ctx, p, rt, cfg, o.noCache, o.dryRun)
}

func buildImage(ctx context.Context, p *output.Printer, rt *runtime.Podman, cfg config.Config, noCache, dryRun bool) error {
	if dryRun {
		// The context path is not created for a dry run; a placeholder shows
		// the shape of the invocation without side effects.
		argv := image.BuildArgs(cfg, "<context>", noCache, Version)
		fmt.Println("podman " + container.ShellQuote(argv))
		return nil
	}
	if !rt.Available() {
		return fmt.Errorf("podman is not installed")
	}
	if err := requireRootless(cfg.Runtime.AllowRoot, p); err != nil {
		return err
	}
	ctxDir, err := os.MkdirTemp("", "aibox-build.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(ctxDir)
	if err := image.WriteContext(ctxDir, cfg.Image.CACertificates); err != nil {
		return err
	}
	p.Info("building %s — this takes a few minutes", image.Ref(cfg))
	if err := rt.ImageBuild(ctx, image.BuildArgs(cfg, ctxDir, noCache, Version)); err != nil {
		return fmt.Errorf("image build failed: %w", err)
	}
	p.Good("built", "%s", image.Ref(cfg))
	return nil
}

// ensureImage builds a missing or recipe-stale image on demand. The recipe
// hash answers "the binary on your PATH changed, your local image did not" —
// it is checked here, at run time, not merely recorded at build time.
func ensureImage(ctx context.Context, p *output.Printer, rt *runtime.Podman, cfg config.Config, forceRebuild, noCache bool) error {
	if forceRebuild {
		return buildImage(ctx, p, rt, cfg, noCache, false)
	}
	exists, current, err := image.IsCurrent(ctx, rt, cfg)
	if err != nil {
		return err
	}
	if !exists {
		if !cfg.Image.Autobuild {
			return fmt.Errorf("image %q does not exist. Run 'aibox image build'", image.Ref(cfg))
		}
		p.Info("image %q not found — building it once", image.Ref(cfg))
		return buildImage(ctx, p, rt, cfg, false, false)
	}
	if !current {
		if cfg.Image.Autobuild {
			p.Info("image is older than this aibox — rebuilding")
			return buildImage(ctx, p, rt, cfg, false, false)
		}
		p.Warn("image %q was built from a different aibox recipe; run 'aibox image build'", image.Ref(cfg))
	}
	return nil
}
