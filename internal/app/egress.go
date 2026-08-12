package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/runtime"
)

// egressManager wires the egress package to a runtime for one configuration.
type egressManager struct {
	rt        runtime.Runtime
	p         *output.Printer
	cfg       config.Config
	projectID string
}

func newEgressManager(rt runtime.Runtime, p *output.Printer, cfg config.Config, projectID string) *egressManager {
	return &egressManager{rt: rt, p: p, cfg: cfg, projectID: projectID}
}

func (m *egressManager) enabledAssistants() []string {
	var out []string
	if m.cfg.Assistants.Claude.Enabled {
		out = append(out, "claude")
	}
	if m.cfg.Assistants.Codex.Enabled {
		out = append(out, "codex")
	}
	return out
}

// writeFiles renders squid.conf and the composed allowlist. Both are
// overwritten in place, never renamed into place: the files are bind-mounted
// into the sidecar, and a rename would leave the container reading a dead
// inode.
func (m *egressManager) writeFiles() (confChanged bool, err error) {
	if err := egress.EnsureFragments(m.enabledAssistants()); err != nil {
		return false, err
	}
	acl, err := egress.Compose(m.enabledAssistants(), m.projectID, m.cfg.Egress.Allowlist)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(egress.GeneratedPath(), []byte(acl), 0o644); err != nil {
		return false, err
	}
	conf, err := egress.SquidConf(m.cfg.Egress.Subnet)
	if err != nil {
		return false, err
	}
	old, _ := os.ReadFile(egress.SquidConfPath())
	if string(old) == conf {
		return false, nil
	}
	return true, os.WriteFile(egress.SquidConfPath(), []byte(conf), 0o644)
}

// Ensure brings the networks and sidecar up (or up to date).
func (m *egressManager) Ensure(ctx context.Context) error {
	if err := m.ensureNetworks(ctx); err != nil {
		return err
	}
	confChanged, err := m.writeFiles()
	if err != nil {
		return err
	}
	running, _ := m.rt.ContainerRunning(ctx, egress.ProxyName)
	if running {
		if confChanged {
			_ = m.rt.Kill(ctx, egress.ProxyName, "HUP")
		}
		return nil
	}
	// A stopped or misconfigured leftover is recreated rather than restarted,
	// so image/log-driver/network changes actually take effect.
	if exists, _ := m.rt.ContainerExists(ctx, egress.ProxyName); exists {
		_ = m.rt.Remove(ctx, egress.ProxyName, runtime.RemoveOptions{Force: true})
	}
	proxyIP, err := egress.ProxyIP(m.cfg.Egress.Subnet)
	if err != nil {
		return err
	}
	if exists, _ := m.rt.ImageExists(ctx, m.cfg.Egress.ProxyImage); !exists {
		m.p.Info("pulling proxy image %s (first time only)", m.cfg.Egress.ProxyImage)
	}

	labels := container.BaseLabels(container.RoleProxy)
	spec := &container.Spec{
		Image:  m.cfg.Egress.ProxyImage,
		Name:   egress.ProxyName,
		Detach: true,
		Mounts: []container.Mount{
			{Type: container.MountBind, Source: egress.SquidConfPath(), Dest: "/etc/squid/squid.conf", Options: []string{"ro"}},
			{Type: container.MountBind, Source: egress.GeneratedPath(), Dest: egress.AllowlistContainerPath, Options: []string{"ro"}},
		},
		NoNewPrivileges: true,
		PidsLimit:       256,
		Labels:          labels,
	}
	argv := spec.Argv(selinuxEnforcing())
	// The sidecar sits on both networks: the internal one it serves and an
	// ordinary one that is its way out. Its address on the internal network
	// is fixed (.2) so clients never need DNS to find it.
	networkArgs := []string{
		"--network", fmt.Sprintf("%s:ip=%s", egress.NetInternal, proxyIP),
		"--network", egress.NetEgress,
	}
	if m.cfg.Egress.ProxyLogDriver != "" {
		networkArgs = append(networkArgs, "--log-driver", m.cfg.Egress.ProxyLogDriver)
	}
	// Insert the network args right after "run --detach --name <n>".
	full := append([]string{argv[0]}, argv[1:]...)
	insertAt := 4 // run, --detach, --name, <name>
	full = append(full[:insertAt], append(networkArgs, full[insertAt:]...)...)

	if err := m.rt.Run(ctx, full); err != nil {
		return fmt.Errorf("could not start the egress proxy %s: %w", egress.ProxyName, err)
	}
	m.p.Info("egress proxy %s up — allowlist: %s (%d entries)",
		egress.ProxyName, egress.GeneratedPath(), egress.Entries(egress.GeneratedPath()))
	return nil
}

func (m *egressManager) ensureNetworks(ctx context.Context) error {
	labels := container.BaseLabels(container.RoleNetwork)
	if exists, _ := m.rt.NetworkExists(ctx, egress.NetInternal); !exists {
		if err := m.rt.NetworkCreate(ctx, egress.NetInternal, true, m.cfg.Egress.Subnet, labels); err != nil {
			return err
		}
		m.p.Info("created internal network %s (%s)", egress.NetInternal, m.cfg.Egress.Subnet)
	}
	if exists, _ := m.rt.NetworkExists(ctx, egress.NetEgress); !exists {
		if err := m.rt.NetworkCreate(ctx, egress.NetEgress, false, "", labels); err != nil {
			return err
		}
		m.p.Info("created network %s", egress.NetEgress)
	}
	return nil
}

// Reload revalidates and applies the composed allowlist. The `squid -k parse`
// gate is what bclaude lacked: a malformed edit used to be discovered by a
// proxy that would not come back. The previous allowlist is restored when the
// new one fails to parse, so the active configuration is only replaced by one
// squid accepts.
func (m *egressManager) Reload(ctx context.Context) error {
	previous, _ := os.ReadFile(egress.GeneratedPath())
	if _, err := m.writeFiles(); err != nil {
		return err
	}
	running, _ := m.rt.ContainerRunning(ctx, egress.ProxyName)
	if !running {
		// Nothing to validate against; the next Ensure starts fresh.
		m.p.Info("proxy not running — configuration will apply on the next start")
		return nil
	}
	res, err := m.rt.Exec(ctx, egress.ProxyName, []string{"squid", "-k", "parse"})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		if previous != nil {
			_ = os.WriteFile(egress.GeneratedPath(), previous, 0o644)
		}
		return fmt.Errorf("squid rejected the new configuration — previous allowlist restored:\n%s", strings.TrimSpace(res.Stderr))
	}
	if _, err := m.rt.Exec(ctx, egress.ProxyName, []string{"squid", "-k", "reconfigure"}); err != nil {
		return err
	}
	m.p.Info("proxy reloaded (%d entries)", egress.Entries(egress.GeneratedPath()))
	return nil
}

func (m *egressManager) Remove(ctx context.Context) error {
	if exists, _ := m.rt.ContainerExists(ctx, egress.ProxyName); exists {
		if err := m.rt.Remove(ctx, egress.ProxyName, runtime.RemoveOptions{Force: true}); err != nil {
			return err
		}
		m.p.Info("removed egress proxy %s", egress.ProxyName)
	}
	for _, net := range []string{egress.NetInternal, egress.NetEgress} {
		if exists, _ := m.rt.NetworkExists(ctx, net); exists {
			if err := m.rt.NetworkRemove(ctx, net); err != nil {
				m.p.Warn("could not remove network %s (still in use?)", net)
			} else {
				m.p.Info("removed network %s", net)
			}
		}
	}
	return nil
}

func selinuxEnforcing() bool {
	out, err := os.ReadFile("/sys/fs/selinux/enforce")
	if err == nil {
		return strings.TrimSpace(string(out)) == "1"
	}
	return false
}

func cmdEgress(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}

	ws, err := resolveWorkspace("", p)
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
	m := newEgressManager(rt, p, cfg, projectID)

	requirePodman := func() error {
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		return nil
	}

	switch sub {
	case "status":
		state := "not created"
		if rt.Available() {
			if running, _ := rt.ContainerRunning(ctx, egress.ProxyName); running {
				state = "running"
			} else if exists, _ := rt.ContainerExists(ctx, egress.ProxyName); exists {
				state = "stopped"
			}
		}
		fmt.Printf("aibox egress\n")
		fmt.Printf("  mode      : %s (per run, via --egress / AIBOX_EGRESS)\n", cfg.Egress.Mode)
		fmt.Printf("  proxy     : %s, %s (%s)\n", egress.ProxyName, state, cfg.Egress.ProxyImage)
		for _, net := range []string{egress.NetInternal, egress.NetEgress} {
			note := ""
			if rt.Available() {
				if exists, _ := rt.NetworkExists(ctx, net); !exists {
					note = " (not created)"
				}
			}
			fmt.Printf("  network   : %s%s\n", net, note)
		}
		fmt.Printf("  fragments : %s\n", egress.ConfigDir())
		fmt.Printf("  generated : %s (%d entries)\n", egress.GeneratedPath(), egress.Entries(egress.GeneratedPath()))
		return nil
	case "start":
		if err := requirePodman(); err != nil {
			return err
		}
		if err := requireRootless(cfg.Runtime.AllowRoot, p); err != nil {
			return err
		}
		return m.Ensure(ctx)
	case "stop":
		if err := requirePodman(); err != nil {
			return err
		}
		return m.Remove(ctx)
	case "reload":
		if err := requirePodman(); err != nil {
			return err
		}
		return m.Reload(ctx)
	case "allow":
		if len(args) == 0 {
			return fmt.Errorf("egress allow needs at least one domain")
		}
		for _, d := range args {
			if err := egress.ValidateDomain(d); err != nil {
				return err
			}
		}
		if err := egress.EnsureFragments(m.enabledAssistants()); err != nil {
			return err
		}
		// Project-scoped: the addition composes on top of base + assistants
		// for this project only.
		frag := egress.FragmentPath("project-" + projectID)
		existing, _ := os.ReadFile(frag)
		f, err := os.OpenFile(frag, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		for _, d := range args {
			if containsLine(string(existing), d) {
				p.Info("%s is already in the allowlist", d)
				continue
			}
			fmt.Fprintln(f, d)
			p.Info("allowed %s (project %s)", d, projectID)
		}
		f.Close()
		if rt.Available() {
			if running, _ := rt.ContainerRunning(ctx, egress.ProxyName); running {
				return m.Reload(ctx)
			}
		}
		return nil
	case "deny":
		if len(args) == 0 {
			return fmt.Errorf("egress deny needs at least one domain")
		}
		fragments := append([]string{"base", "project-" + projectID}, m.enabledAssistants()...)
		removed := false
		for _, name := range fragments {
			path := egress.FragmentPath(name)
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var kept []string
			for _, line := range strings.Split(string(data), "\n") {
				drop := false
				for _, d := range args {
					if strings.TrimSpace(line) == d {
						drop = true
						removed = true
						p.Info("removed %s from %s", d, path)
					}
				}
				if !drop {
					kept = append(kept, line)
				}
			}
			if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
				return err
			}
		}
		if !removed {
			p.Info("nothing removed — the domain(s) were not in any fragment")
			return nil
		}
		if rt.Available() {
			if running, _ := rt.ContainerRunning(ctx, egress.ProxyName); running {
				return m.Reload(ctx)
			}
		}
		return nil
	case "list":
		if err := egress.EnsureFragments(m.enabledAssistants()); err != nil {
			return err
		}
		acl, err := egress.Compose(m.enabledAssistants(), projectID, cfg.Egress.Allowlist)
		if err != nil {
			return err
		}
		fmt.Print(acl)
		return nil
	case "denied":
		if err := requirePodman(); err != nil {
			return err
		}
		if exists, _ := rt.ContainerExists(ctx, egress.ProxyName); !exists {
			return fmt.Errorf("the egress proxy has not run yet — start a session with --egress proxy first")
		}
		logs, err := rt.Logs(ctx, egress.ProxyName, 5000)
		if err != nil {
			return err
		}
		counts := egress.ParseDenied(logs)
		if len(counts) == 0 {
			p.Info("nothing denied in the proxy's recent log")
			return nil
		}
		type entry struct {
			domain string
			count  int
		}
		var entries []entry
		for d, c := range counts {
			entries = append(entries, entry{d, c})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
		fmt.Printf("  COUNT DOMAIN\n")
		for _, e := range entries {
			fmt.Printf("  %5d %s\n", e.count, e.domain)
		}
		fmt.Printf("\nallow one with: aibox egress allow <domain>\n")
		return nil
	case "logs":
		if err := requirePodman(); err != nil {
			return err
		}
		fs := flag.NewFlagSet("egress logs", flag.ContinueOnError)
		layer := fs.String("layer", "", "http (squid) | relay (haproxy); default merges both")
		follow := fs.Bool("f", false, "follow")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return egressLogs(ctx, p, rt, *layer, *follow)
	default:
		return fmt.Errorf("unknown egress subcommand %q (status | start | stop | reload | allow <domain..> | deny <domain..> | list | denied | logs [-f])", sub)
	}
}

// egressLogs shows the egress audit trail. --layer http streams squid, --layer
// relay streams the relay; the default merges both in timestamp order so one
// command answers "what did this session touch" (§8.8). Follow mode requires a
// single layer — interleaving two live streams in true timestamp order is not
// something a byte-stream tail can promise.
func egressLogs(ctx context.Context, p *output.Printer, rt *runtime.Podman, layer string, follow bool) error {
	switch layer {
	case "http":
		return streamLogs(ctx, rt, egress.ProxyName, follow)
	case "relay":
		return streamLogs(ctx, rt, relayName(), follow)
	case "":
		if follow {
			return fmt.Errorf("egress logs -f needs --layer http or --layer relay (a merged live tail cannot promise timestamp order)")
		}
		return mergedLogs(ctx, rt)
	default:
		return fmt.Errorf("unknown --layer %q (use: http, relay)", layer)
	}
}

func relayName() string { return "aibox-relay" }

func streamLogs(ctx context.Context, rt *runtime.Podman, name string, follow bool) error {
	if exists, _ := rt.ContainerExists(ctx, name); !exists {
		return fmt.Errorf("%s has not run yet", name)
	}
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, name)
	return rt.Run(ctx, args)
}

// mergedLogs captures both sidecars' recent logs, tags each line with its
// layer, and sorts by the leading timestamp. squid logs unix epoch seconds
// first; haproxy tcplog does not lead with a sortable timestamp, so its lines
// are tagged and appended in order — good enough for the "what happened"
// question without pretending to a precision the formats do not share.
func mergedLogs(ctx context.Context, rt *runtime.Podman) error {
	type line struct {
		ts    float64
		text  string
		order int
	}
	var lines []line
	order := 0
	add := func(name, tag string) {
		if exists, _ := rt.ContainerExists(ctx, name); !exists {
			return
		}
		out, err := rt.Logs(ctx, name, 5000)
		if err != nil {
			return
		}
		for _, raw := range strings.Split(out, "\n") {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			ts := parseLeadingEpoch(raw)
			lines = append(lines, line{ts: ts, text: fmt.Sprintf("[%s] %s", tag, raw), order: order})
			order++
		}
	}
	add(egress.ProxyName, "http")
	add(relayName(), "relay")
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].ts == lines[j].ts {
			return lines[i].order < lines[j].order
		}
		return lines[i].ts < lines[j].ts
	})
	for _, l := range lines {
		fmt.Println(l.text)
	}
	if len(lines) == 0 {
		p := output.NewStderr()
		p.Info("no egress logs yet")
	}
	return nil
}

// parseLeadingEpoch reads squid's leading "1723400000.123" timestamp; returns
// 0 for lines that do not start with one (they sort first, stably).
func parseLeadingEpoch(s string) float64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	var v float64
	if _, err := fmt.Sscanf(fields[0], "%f", &v); err != nil {
		return 0
	}
	return v
}

func containsLine(content, line string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == line {
			return true
		}
	}
	return false
}
