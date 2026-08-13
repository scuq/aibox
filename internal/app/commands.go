package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/doctor"
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/image"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/relay"
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

// appendNetworkChecks adds the egress-topology section to a doctor report:
// the two aibox networks and their internal/external posture, the sidecars and
// workloads attached to them, and an active reachability probe from the egress
// leg. Structural checks are authoritative and fast; the probe is best-effort.
func appendNetworkChecks(ctx context.Context, rt *runtime.Podman, cfg config.Config, report *doctor.Report) {
	add := func(status, detail string) {
		report.Checks = append(report.Checks, doctor.Check{Name: "network", Status: status, Detail: detail})
		if status == "fail" {
			report.OK = false
		}
	}

	// aibox-internal must exist as --internal, or the workload's "no route
	// out" isolation is not actually there.
	internalExists, _ := rt.NetworkExists(ctx, egress.NetInternal)
	if !internalExists {
		add("warn", fmt.Sprintf("%s not created — run 'aibox net up'", egress.NetInternal))
	} else if internal, err := rt.NetworkInternal(ctx, egress.NetInternal); err == nil && internal {
		add("ok", fmt.Sprintf("%s: internal, no route out — workload isolation intact", egress.NetInternal))
	} else {
		add("fail", fmt.Sprintf("%s exists but is NOT --internal — the workload would have a route out; recreate with 'aibox net down && aibox net up'", egress.NetInternal))
	}

	// aibox-egress is the sidecars' way out; it must NOT be internal.
	egressExists, _ := rt.NetworkExists(ctx, egress.NetEgress)
	if !egressExists {
		add("warn", fmt.Sprintf("%s not created — run 'aibox net up'", egress.NetEgress))
	} else if internal, err := rt.NetworkInternal(ctx, egress.NetEgress); err == nil && internal {
		add("fail", fmt.Sprintf("%s is --internal — squid and the relay cannot reach out", egress.NetEgress))
	} else {
		add("ok", fmt.Sprintf("%s: external, the sidecars' way out", egress.NetEgress))
	}

	// Sidecars and their attachments.
	for _, sc := range []struct{ label, name string }{
		{"squid", egress.ProxyName},
		{"relay", relay.Name},
	} {
		if running, _ := rt.ContainerRunning(ctx, sc.name); running {
			nets, _ := rt.ContainerNetworks(ctx, sc.name)
			add("ok", fmt.Sprintf("%s (%s) attached to: %s", sc.label, sc.name, strings.Join(nets, ", ")))
		} else if sc.label == "squid" {
			add("warn", fmt.Sprintf("%s (%s) not running — 'aibox net up' starts it", sc.label, sc.name))
		} else if len(cfg.Services) > 0 {
			add("warn", fmt.Sprintf("%s (%s) not running though %d service(s) are configured — 'aibox relay start'", sc.label, sc.name, len(cfg.Services)))
		}
		// A relay with no configured services is correctly absent; stay quiet.
	}

	// Running workloads: show each aibox-managed workspace container and the
	// networks it is on, so "attached to what" covers the containers too.
	if workloads, err := rt.List(ctx, runtime.Filter{Labels: map[string]string{
		container.LabelManaged: "true",
		container.LabelRole:    container.RoleWorkspace,
	}}); err == nil {
		for _, c := range workloads {
			if c.State != "running" {
				continue
			}
			nets, _ := rt.ContainerNetworks(ctx, c.Name)
			joined := strings.Join(nets, ", ")
			if joined == "" {
				joined = "(none)"
			}
			onInternal := false
			for _, n := range nets {
				if n == egress.NetInternal {
					onInternal = true
				}
			}
			// If the isolated network exists on this host but a managed
			// workload is not on it, that workload has a route out and its
			// egress is not going through squid — surface it, with the fix.
			// Most commonly a devcontainer generated before egress proxy was
			// enabled, so its runArgs carry no --network=aibox-internal.
			if internalExists && !onInternal {
				detail := fmt.Sprintf("workload %s attached to: %s — NOT on %s, so it has a route out and its egress bypasses squid", c.Name, joined, egress.NetInternal)
				if c.Labels[container.LabelMode] == container.ModeDevcontainer {
					detail += "; set 'egress: { mode: proxy }' in .aibox.yaml and run 'aibox devcontainer here --force', then rebuild the container"
				}
				add("warn", detail)
			} else {
				add("ok", fmt.Sprintf("workload %s attached to: %s", c.Name, joined))
			}
		}
	}

	// Active reachability from the egress leg — the "does it actually reach
	// out" question. Best-effort: needs the built image (for bash) and the
	// egress network; a couple of TCP/443 probes to well-known anycast hosts.
	if egressExists {
		if imgExists, _ := rt.ImageExists(ctx, image.Ref(cfg)); imgExists {
			probe := "for h in 1.1.1.1 8.8.8.8; do timeout 3 bash -c \"</dev/tcp/$h/443\" 2>/dev/null && { echo OK; exit 0; }; done; echo NONE"
			out, _ := rt.Capture(ctx, "run", "--rm", "--network", egress.NetEgress,
				"--entrypoint", "bash", image.Ref(cfg), "-c", probe)
			if strings.Contains(out, "OK") {
				add("ok", fmt.Sprintf("%s reaches the internet (tcp/443 to 1.1.1.1 or 8.8.8.8)", egress.NetEgress))
			} else {
				add("warn", fmt.Sprintf("%s could not reach 1.1.1.1/8.8.8.8:443 — the sidecars may have no upstream, or those hosts are blocked here", egress.NetEgress))
			}
		} else {
			add("warn", fmt.Sprintf("skipping the egress reachability probe — image %s is not built yet", image.Ref(cfg)))
		}
	}
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

	// The egress topology: which networks exist, whether their internal/
	// external posture is what the security model requires, what is attached
	// to what, and whether the egress leg actually reaches out.
	if pinfo != nil && cfgErr == nil {
		appendNetworkChecks(ctx, rt, cfg, &report)
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
