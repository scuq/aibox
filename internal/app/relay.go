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
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/relay"
	"github.com/scuq/aibox/internal/runtime"
)

// relayManager wires the relay package to a runtime for one configuration.
type relayManager struct {
	rt       *runtime.Podman
	p        *output.Printer
	cfg      config.Config
	services []relay.Service
}

func newRelayManager(rt *runtime.Podman, p *output.Printer, cfg config.Config) (*relayManager, error) {
	services, err := relay.Resolve(cfg.Services)
	if err != nil {
		return nil, err
	}
	return &relayManager{rt: rt, p: p, cfg: cfg, services: services}, nil
}

// configPath is where the rendered haproxy.cfg lives on the host (bind-mounted
// into the sidecar read-only).
func (m *relayManager) configPath() string {
	return filepath.Join(egress.ConfigDir(), "haproxy.cfg")
}

func (m *relayManager) writeConfig() error {
	if err := os.MkdirAll(egress.ConfigDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.configPath(), []byte(relay.HAProxyConfig(m.services)), 0o644)
}

// validateConfig gates a (re)start on `haproxy -c -f` — the relay's equivalent
// of squid's `-k parse` (§8.12): a config that does not parse must not replace
// a running one.
func (m *relayManager) validateConfig(ctx context.Context) error {
	out, err := runCapture(ctx, m.rt, "run", "--rm",
		"--volume", m.configPath()+":"+relay.ConfigContainerPath+":ro",
		"--entrypoint", "haproxy", m.cfg.Egress.RelayImage,
		"-c", "-f", relay.ConfigContainerPath)
	if err != nil {
		return fmt.Errorf("haproxy rejected the relay configuration:\n%s", strings.TrimSpace(out))
	}
	return nil
}

// Ensure brings the relay sidecar up (or restarts it for a config change).
// haproxy has no clean live reconfigure, so a service change is a restart,
// which drops open sessions — callers warn before doing it.
func (m *relayManager) Ensure(ctx context.Context) error {
	if len(m.services) == 0 {
		return nil
	}
	if udp := m.udpServices(); len(udp) > 0 {
		m.p.Warn("udp service(s) %s are declared but best-effort UDP relaying is not started by this build (§8.6) — TCP services are unaffected", strings.Join(udp, ", "))
	}
	if err := m.writeConfig(); err != nil {
		return err
	}
	if err := m.validateConfig(ctx); err != nil {
		return err
	}
	// A restart drops open sessions; recreate rather than reuse so image and
	// network changes take effect too.
	if exists, _ := m.rt.ContainerExists(ctx, relay.Name); exists {
		_ = m.rt.Remove(ctx, relay.Name, runtime.RemoveOptions{Force: true})
	}
	relayIP, err := relay.RelayIP(m.cfg.Egress.Subnet)
	if err != nil {
		return err
	}
	if exists, _ := m.rt.ImageExists(ctx, m.cfg.Egress.RelayImage); !exists {
		m.p.Info("pulling relay image %s (first time only)", m.cfg.Egress.RelayImage)
	}
	labels := container.BaseLabels(container.RoleProxy)
	labels["io.aibox.relay"] = "true"
	spec := &container.Spec{
		Image:  m.cfg.Egress.RelayImage,
		Name:   relay.Name,
		Detach: true,
		Mounts: []container.Mount{
			{Type: container.MountBind, Source: m.configPath(), Dest: relay.ConfigContainerPath, Options: []string{"ro"}},
		},
		NoNewPrivileges: true,
		PidsLimit:       256,
		Labels:          labels,
	}
	argv := spec.Argv(selinuxEnforcing())
	// Two networks, exactly like squid: the internal one it serves (at the
	// fixed .3 address, so clients never need DNS to find it) and the
	// aibox-egress network that is its way out to the backends. The workload
	// is never on aibox-egress — it can only address the two sidecars.
	networkArgs := []string{
		"--network", fmt.Sprintf("%s:ip=%s", egress.NetInternal, relayIP),
		"--network", egress.NetEgress,
	}
	insertAt := 4 // run, --detach, --name, <name>
	full := append(argv[:insertAt:insertAt], append(networkArgs, argv[insertAt:]...)...)
	if err := m.rt.Run(ctx, full); err != nil {
		return fmt.Errorf("could not start the relay %s: %w", relay.Name, err)
	}
	m.p.Info("relay %s up — %d service(s), ports from %d", relay.Name, m.tcpCount(), relay.BasePort)
	return nil
}

func (m *relayManager) Remove(ctx context.Context) error {
	if exists, _ := m.rt.ContainerExists(ctx, relay.Name); exists {
		if err := m.rt.Remove(ctx, relay.Name, runtime.RemoveOptions{Force: true}); err != nil {
			return err
		}
		m.p.Info("removed relay %s", relay.Name)
	} else {
		m.p.Info("relay not running")
	}
	return nil
}

func (m *relayManager) udpServices() []string {
	var out []string
	for _, s := range m.services {
		if s.Proto() == "udp" {
			out = append(out, s.Name)
		}
	}
	return out
}

func (m *relayManager) tcpCount() int {
	n := 0
	for _, s := range m.services {
		if s.Proto() == "tcp" {
			n++
		}
	}
	return n
}

func cmdRelay(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
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
	if err := cfg.Validate(); err != nil {
		return err
	}
	m, err := newRelayManager(rt, p, cfg)
	if err != nil {
		return err
	}
	relayIP, _ := relay.RelayIP(cfg.Egress.Subnet)

	switch sub {
	case "list":
		fs := flag.NewFlagSet("relay list", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *jsonOut {
			return output.JSON(relay.Inventory(m.services, relayIP))
		}
		if len(m.services) == 0 {
			p.Info("no services configured")
			return nil
		}
		fmt.Printf("  %-24s %-6s %-6s %s\n", "SERVICE", "PORT", "PROTO", "BACKEND")
		for _, s := range m.services {
			backend := s.Backend
			if !s.BackendDisclosed {
				backend = "(hidden)"
			}
			fmt.Printf("  %-24s %-6d %-6s %s\n", s.Name, s.Port, s.Proto(), backend)
		}
		return nil
	case "start", "restart":
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		if err := requireRootless(cfg.Runtime.AllowRoot, p); err != nil {
			return err
		}
		if len(m.services) == 0 {
			return fmt.Errorf("no services configured — nothing to relay")
		}
		if sub == "restart" {
			p.Warn("restarting the relay drops any open sessions")
		}
		// The internal network must exist; the egress manager owns it.
		if err := newEgressManager(rt, p, cfg, "").ensureNetworks(ctx); err != nil {
			return err
		}
		return m.Ensure(ctx)
	case "stop":
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		return m.Remove(ctx)
	case "status":
		state := "not created"
		if rt.Available() {
			if running, _ := rt.ContainerRunning(ctx, relay.Name); running {
				state = "running"
			} else if exists, _ := rt.ContainerExists(ctx, relay.Name); exists {
				state = "stopped"
			}
		}
		fmt.Printf("aibox relay\n")
		fmt.Printf("  sidecar  : %s, %s (%s)\n", relay.Name, state, cfg.Egress.RelayImage)
		fmt.Printf("  address  : %s (internal .3)\n", relayIP)
		fmt.Printf("  services : %d configured\n", len(m.services))
		return nil
	case "test":
		if len(args) != 1 {
			return fmt.Errorf("relay test needs exactly one service name")
		}
		return relayTest(ctx, p, rt, cfg, m, args[0])
	case "logs":
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		if exists, _ := rt.ContainerExists(ctx, relay.Name); !exists {
			return fmt.Errorf("the relay has not run yet")
		}
		logsArgs := append([]string{"logs"}, args...)
		logsArgs = append(logsArgs, relay.Name)
		return rt.Run(ctx, logsArgs)
	default:
		return fmt.Errorf("unknown relay subcommand %q (start | stop | restart | status | list | test | logs)", sub)
	}
}

// relayTest probes a service's listener from inside the internal network — the
// workload's own vantage point, which is where every reachability incident
// starts. It answers "policy or upstream?": a refused connect to the relay
// port is the relay down; a connect that opens but the backend never answers
// is an upstream problem.
func relayTest(ctx context.Context, p *output.Printer, rt *runtime.Podman, cfg config.Config, m *relayManager, name string) error {
	var svc *relay.Service
	for i := range m.services {
		if m.services[i].Name == name {
			svc = &m.services[i]
			break
		}
		for _, a := range m.services[i].Aliases {
			if a == name {
				svc = &m.services[i]
				break
			}
		}
	}
	if svc == nil {
		return fmt.Errorf("no service named %q — 'aibox relay list' shows the configured ones", name)
	}
	if !rt.Available() {
		return fmt.Errorf("podman is not installed")
	}
	relayIP, err := relay.RelayIP(cfg.Egress.Subnet)
	if err != nil {
		return err
	}
	// A throwaway container on the internal network doing a TCP connect. bash's
	// /dev/tcp is enough and needs nothing installed in the relay image.
	probe := fmt.Sprintf("timeout 5 bash -c '</dev/tcp/%s/%d' && echo OPEN || echo REFUSED", relayIP, svc.Port)
	out, err := runCapture(ctx, rt, "run", "--rm", "--network", egress.NetInternal,
		"--entrypoint", "bash", cfg.Image.Reference, "-c", probe)
	out = strings.TrimSpace(out)
	if strings.Contains(out, "OPEN") {
		p.Info("%s: listener %s:%d is reachable from inside the network", name, relayIP, svc.Port)
		return nil
	}
	if err != nil && out == "" {
		return fmt.Errorf("probe could not run: %w", err)
	}
	p.Warn("%s: listener %s:%d refused the connection — the relay is down or the service is not configured on it", name, relayIP, svc.Port)
	return nil
}

// runCapture runs a podman invocation and returns combined output for
// inspection (validation, probes).
func runCapture(ctx context.Context, rt *runtime.Podman, args ...string) (string, error) {
	return rt.Capture(ctx, args...)
}
