package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/doctor"
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/image"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/runtime"
)

func cmdConfig(args []string) error {
	if len(args) == 0 || args[0] != "show" {
		return fmt.Errorf("config needs a subcommand: show")
	}
	fs := flag.NewFlagSet("config show", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	workspace := fs.String("workspace", "", "workspace")
	fs.StringVar(workspace, "w", "", "workspace")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ws := *workspace
	if ws == "" {
		ws, _ = os.Getwd()
	}
	cfg, err := config.Load(ws)
	if err != nil {
		return err
	}
	if *jsonOut {
		return output.JSON(cfg)
	}
	rendered, err := config.Render(cfg)
	if err != nil {
		return err
	}
	fmt.Print(rendered)
	return nil
}

const initTemplate = `# aibox project configuration — resolved on top of the user config and the
# compiled defaults. 'aibox config show' prints the merged result.
version: 1

# assistants:
#   claude: { enabled: true }
#   codex:  { enabled: false }

# runtime:
#   memory: 6g
#   cpus: "4"

# git:
#   history: read-only   # read-only | none — there is never a git write path

# egress:
#   mode: proxy
#   allowlist:
#     - api.example.com
`

func cmdInit(p *output.Printer, args []string) error {
	ws := "."
	if len(args) > 0 {
		ws = args[0]
	}
	canon, err := resolveWorkspace(ws, p)
	if err != nil {
		return err
	}
	path := filepath.Join(canon, config.ProjectConfigName)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	if err := os.WriteFile(path, []byte(initTemplate), 0o644); err != nil {
		return err
	}
	p.Good("wrote", "%s", path)
	return nil
}

func cmdStatus(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
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
	projectID, err := project.ID(ws)
	if err != nil {
		return err
	}

	type volState struct {
		Name   string `json:"name"`
		Exists bool   `json:"exists"`
	}
	st := struct {
		Version   string     `json:"version"`
		Workspace string     `json:"workspace"`
		ProjectID string     `json:"projectId"`
		Image     string     `json:"image"`
		ImageInfo string     `json:"imageState"`
		Volumes   []volState `json:"volumes"`
		Egress    string     `json:"egress"`
	}{
		Version:   Version,
		Workspace: ws,
		ProjectID: projectID,
		Image:     image.Ref(cfg),
		Egress:    cfg.Egress.Mode,
	}

	podman := rt.Available()
	st.ImageInfo = "unknown (podman not available)"
	if podman {
		exists, current, _ := image.IsCurrent(ctx, rt, cfg)
		switch {
		case !exists:
			st.ImageInfo = "not built"
		case current:
			st.ImageInfo = "up-to-date"
		default:
			st.ImageInfo = "stale (built from a different aibox recipe)"
		}
	}

	var volNames []string
	if cfg.Assistants.Claude.Enabled {
		volNames = append(volNames, project.AuthVolumeName("claude"), project.ConfigVolumeName("claude", projectID))
	}
	if cfg.Assistants.Codex.Enabled {
		volNames = append(volNames, project.AuthVolumeName("codex"), project.ConfigVolumeName("codex", projectID))
	}
	volNames = append(volNames, project.CacheVolumeName)
	for _, v := range volNames {
		exists := false
		if podman {
			exists, _ = rt.VolumeExists(ctx, v)
		}
		st.Volumes = append(st.Volumes, volState{Name: v, Exists: exists})
	}
	if cfg.Egress.Mode == "proxy" && podman {
		if running, _ := rt.ContainerRunning(ctx, egress.ProxyName); running {
			st.Egress = "proxy (sidecar running; 'aibox egress status' for detail)"
		} else {
			st.Egress = "proxy (sidecar not running — starts on the next run)"
		}
	}

	if *jsonOut {
		return output.JSON(st)
	}
	fmt.Printf("aibox %s\n", Version)
	fmt.Printf("  workspace : %s\n", st.Workspace)
	fmt.Printf("  project   : %s (%s)\n", filepath.Base(ws), st.ProjectID)
	fmt.Printf("  image     : %s (%s)\n", st.Image, st.ImageInfo)
	for _, v := range st.Volumes {
		note := ""
		if !v.Exists {
			note = " (not created)"
		}
		fmt.Printf("  volume    : %s%s\n", v.Name, note)
	}
	fmt.Printf("  limits    : memory=%s cpus=%s\n", orNone(cfg.Runtime.Memory), orNone(cfg.Runtime.CPUs))
	fmt.Printf("  egress    : %s\n", st.Egress)
	fmt.Printf("  git       : history=%s, no write path (commits happen on the host)\n", cfg.Git.History)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func cmdDoctor(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	allowRoot := fs.Bool("allow-root", false, "permit rootful podman")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	ws, _ := os.Getwd()
	cfg, cfgErr := config.Load(ws)

	var pinfo *doctor.PodmanInfo
	var podmanDetail string
	if !rt.Available() {
		podmanDetail = "podman not installed (apt-get/dnf/pacman install podman, or brew on macOS)"
	} else if info, err := rt.Info(ctx); err != nil {
		podmanDetail = fmt.Sprintf("`podman info` fails — podman is installed but not usable: %v", err)
	} else {
		pinfo = &doctor.PodmanInfo{
			Rootless:       info.Rootless,
			CgroupsVersion: info.CgroupsVersion,
			NetworkBackend: info.NetworkBackend,
		}
	}

	report := doctor.Run(*allowRoot || (cfgErr == nil && cfg.Runtime.AllowRoot),
		cfgErr == nil && cfg.Egress.Mode == "proxy", pinfo)
	if pinfo == nil && podmanDetail != "" {
		report.Checks[0].Detail = podmanDetail
	} else if pinfo != nil {
		report.Checks = append([]doctor.Check{{Name: "podman", Status: "ok", Detail: "podman is functional"}}, report.Checks...)
	}

	// Image and workspace state, host-side.
	if pinfo != nil && cfgErr == nil {
		exists, current, _ := image.IsCurrent(ctx, rt, cfg)
		switch {
		case !exists:
			report.Checks = append(report.Checks, doctor.Check{Name: "image", Status: "warn",
				Detail: fmt.Sprintf("image %s not built yet — the first run builds it (or run 'aibox image build')", image.Ref(cfg))})
		case current:
			report.Checks = append(report.Checks, doctor.Check{Name: "image", Status: "ok",
				Detail: fmt.Sprintf("image %s present and up to date", image.Ref(cfg))})
		default:
			report.Checks = append(report.Checks, doctor.Check{Name: "image", Status: "warn",
				Detail: fmt.Sprintf("image %s is stale (built from a different aibox) — will rebuild on next run", image.Ref(cfg))})
		}
	}
	if cfgErr != nil {
		report.Checks = append(report.Checks, doctor.Check{Name: "config", Status: "fail", Detail: cfgErr.Error()})
		report.OK = false
	}

	if *jsonOut {
		if err := output.JSON(report); err != nil {
			return 1
		}
		if report.OK {
			return 0
		}
		return 1
	}

	fmt.Printf("aibox %s — environment check\n\n", Version)
	for _, c := range report.Checks {
		mark := map[string]string{"ok": "✓", "warn": "•", "fail": "✗"}[c.Status]
		fmt.Printf("  %s %s\n", mark, c.Detail)
	}
	fmt.Println()
	if report.OK {
		fmt.Println("Ready. Run `aibox run claude` in a project directory.")
		return 0
	}
	fmt.Println("Problems found — see the ✗ lines above.")
	return 1
}
