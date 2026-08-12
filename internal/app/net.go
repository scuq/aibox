package app

import (
	"context"
	"flag"
	"fmt"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/relay"
	"github.com/scuq/aibox/internal/runtime"
)

// cmdNet stands up (or tears down) the persistent egress topology as a unit,
// independent of any single run:
//
//   - aibox-internal — the workload's only network, --internal, no route out.
//   - aibox-egress   — the sidecars' way out; the workload is never on it.
//   - squid          — on both, enforcing the domain allowlist (§8.0).
//   - relay          — on both, the named-service byte pipe (§8), when
//     services are configured.
//
// `aibox run --egress proxy` and a generated devcontainer both attach only to
// aibox-internal and reuse whatever this command left running, so the sidecars
// survive across sessions and across VS Code opening/closing the container.
func cmdNet(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
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
	// The topology exists to filter egress; the internal network only makes
	// sense in proxy mode, so `net up` forces it rather than standing up an
	// internal network the workload would never join.
	cfg.Egress.Mode = "proxy"
	if err := cfg.Validate(); err != nil {
		return err
	}

	switch sub {
	case "up":
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		if err := requireRootless(cfg.Runtime.AllowRoot, p); err != nil {
			return err
		}
		// Squid brings up both networks and stays running.
		if err := newEgressManager(rt, p, cfg, "").Ensure(ctx); err != nil {
			return err
		}
		// Relay only has something to listen on when services are configured;
		// haproxy with zero listeners will not start, so skip it (loudly)
		// rather than fail the whole setup.
		if len(cfg.Services) > 0 {
			rmgr, err := newRelayManager(rt, p, cfg)
			if err != nil {
				return err
			}
			if err := rmgr.Ensure(ctx); err != nil {
				return err
			}
		} else {
			p.Info("no services configured — relay not started (add services: to .aibox.yaml, then 'aibox net up' again)")
		}
		p.Good("ready", "aibox network is up — start a session with 'aibox run --egress proxy …'")
		return nil

	case "down":
		if !rt.Available() {
			return fmt.Errorf("podman is not installed")
		}
		// Relay first: it is attached to the networks, so removing the networks
		// while it is up would fail.
		if rmgr, err := newRelayManager(rt, p, cfg); err == nil {
			_ = rmgr.Remove(ctx)
		}
		// Then squid and both networks.
		return newEgressManager(rt, p, cfg, "").Remove(ctx)

	case "status":
		return netStatus(ctx, p, rt, cfg, args)

	default:
		return fmt.Errorf("unknown net subcommand %q (up | down | status)", sub)
	}
}

func netStatus(ctx context.Context, p *output.Printer, rt *runtime.Podman, cfg config.Config, args []string) error {
	proxyIP, _ := egress.ProxyIP(cfg.Egress.Subnet)
	relayIP, _ := relay.RelayIP(cfg.Egress.Subnet)
	services, _ := relay.Resolve(cfg.Services)

	type netInfo struct {
		Name    string `json:"name"`
		Exists  bool   `json:"exists"`
		Purpose string `json:"purpose"`
	}
	type sidecar struct {
		Name  string `json:"name"`
		State string `json:"state"`
		IP    string `json:"ip"`
	}
	st := struct {
		Subnet   string    `json:"subnet"`
		Networks []netInfo `json:"networks"`
		Squid    sidecar   `json:"squid"`
		Relay    sidecar   `json:"relay"`
		Services int       `json:"services"`
	}{Subnet: cfg.Egress.Subnet, Services: len(services)}

	podman := rt.Available()
	exists := func(name string) bool {
		if !podman {
			return false
		}
		ok, _ := rt.NetworkExists(ctx, name)
		return ok
	}
	st.Networks = []netInfo{
		{egress.NetInternal, exists(egress.NetInternal), "workload only; no route out"},
		{egress.NetEgress, exists(egress.NetEgress), "sidecars' way out"},
	}
	state := func(name string) string {
		if !podman {
			return "unknown"
		}
		if running, _ := rt.ContainerRunning(ctx, name); running {
			return "running"
		}
		if ex, _ := rt.ContainerExists(ctx, name); ex {
			return "stopped"
		}
		return "not created"
	}
	st.Squid = sidecar{egress.ProxyName, state(egress.ProxyName), proxyIP}
	st.Relay = sidecar{relay.Name, state(relay.Name), relayIP}

	fs := flag.NewFlagSet("net status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *jsonOut {
		return output.JSON(st)
	}
	fmt.Printf("aibox network (subnet %s)\n", st.Subnet)
	for _, n := range st.Networks {
		mark := "not created"
		if n.Exists {
			mark = "created"
		}
		fmt.Printf("  network  : %-16s %-12s (%s)\n", n.Name, mark, n.Purpose)
	}
	fmt.Printf("  squid    : %-16s %-12s %s\n", st.Squid.Name, st.Squid.State, st.Squid.IP)
	relayNote := st.Relay.State
	if st.Services == 0 {
		relayNote += " (no services configured)"
	}
	fmt.Printf("  relay    : %-16s %-12s %s\n", st.Relay.Name, relayNote, st.Relay.IP)
	return nil
}
