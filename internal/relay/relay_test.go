package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/config"
)

func svc(name, backend string, listen int, aliases ...string) config.ServiceConfig {
	return config.ServiceConfig{Name: name, Backend: backend, Listen: listen, Aliases: aliases}
}

func TestRelayIP(t *testing.T) {
	// squid is .2, the relay .3 — derived arithmetically, never by name.
	got, err := RelayIP("10.199.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.199.0.3" {
		t.Errorf("RelayIP = %q, want 10.199.0.3", got)
	}
	if _, err := RelayIP("nonsense"); err == nil {
		t.Error("invalid subnet should be refused")
	}
}

func TestPortAllocation(t *testing.T) {
	// Explicit listen wins; the rest allocate from BasePort in declaration
	// order, skipping any port an explicit entry already claimed.
	services, err := Resolve([]config.ServiceConfig{
		svc("a", "h1:22", 0),    // 2200
		svc("b", "h2:22", 2204), // explicit
		svc("c", "h3:22", 0),    // 2201
		svc("d", "h4:22", 0),    // 2202
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"a": 2200, "b": 2204, "c": 2201, "d": 2202}
	for _, s := range services {
		if s.Port != want[s.Name] {
			t.Errorf("service %q port = %d, want %d", s.Name, s.Port, want[s.Name])
		}
	}
}

func TestPortAllocationSkipsClaimed(t *testing.T) {
	// An auto-allocated port must not collide with a later explicit one.
	services, _ := Resolve([]config.ServiceConfig{
		svc("a", "h:22", 0), // wants 2200
		svc("b", "h:22", 0), // wants 2201
		svc("c", "h:22", 2201),
	})
	seen := map[int]bool{}
	for _, s := range services {
		if seen[s.Port] {
			t.Errorf("port %d allocated twice", s.Port)
		}
		seen[s.Port] = true
	}
}

func TestExplicitPortCollision(t *testing.T) {
	_, err := Resolve([]config.ServiceConfig{
		svc("a", "h:22", 2204),
		svc("b", "h:22", 2204),
	})
	if err == nil {
		t.Error("two services on the same explicit port must be an error")
	}
}

func TestHAProxyConfig(t *testing.T) {
	services, _ := Resolve([]config.ServiceConfig{
		{Name: "sw1", Backend: "10.20.4.11:22", Listen: 2204, MaxConns: 4},
		{Name: "ise", Backend: "ise01:9060", MaxConns: 2, IdleTimeout: "300s"},
	})
	cfg := HAProxyConfig(services)
	for _, want := range []string{
		"listen svc_sw1",
		"bind 0.0.0.0:2204",
		"mode tcp",
		"server backend 10.20.4.11:22 check",
		"listen svc_ise",
		"maxconn 2",
		"timeout client 300s",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("haproxy config missing %q\n%s", want, cfg)
		}
	}
}

func TestSSHConfigHostKeyAlias(t *testing.T) {
	// HostKeyAlias is not optional (§8.4): it keys known_hosts to device
	// identity, so host keys survive port churn.
	services, _ := Resolve([]config.ServiceConfig{
		svc("sw-nw0102-o71", "10.20.4.11:22", 2204, "switch1"),
	})
	cfg := SSHConfig(services, "10.199.0.3")
	for _, want := range []string{
		"Host sw-nw0102-o71 switch1",
		"HostName        10.199.0.3", // the derived IP, never a container name
		"Port            2204",
		"HostKeyAlias    sw-nw0102-o71",
		"User            %r",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("ssh config missing %q\n%s", want, cfg)
		}
	}
	if strings.Contains(cfg, "aibox-relay") {
		t.Error("HostName must be the derived IP, not the container name (§8.12)")
	}
}

func TestInventoryHidesBackendByDefault(t *testing.T) {
	services, _ := Resolve([]config.ServiceConfig{
		svc("secret", "10.20.4.11:22", 2204),
		{Name: "open", Backend: "public:443", Listen: 2205, BackendDisclosed: true},
	})
	inv := Inventory(services, "10.199.0.3")
	byName := map[string]ServiceInfo{}
	for _, i := range inv {
		byName[i.Name] = i
	}
	if byName["secret"].Backend != "" || byName["secret"].BackendDisclosed {
		t.Error("backend must be hidden by default — the agent gets a name, not a network map")
	}
	if byName["secret"].Address != "10.199.0.3:2204" {
		t.Errorf("address = %q", byName["secret"].Address)
	}
	if byName["open"].Backend != "public:443" {
		t.Error("a disclosed backend must appear")
	}
}

func TestInventoryJSONIsNewlineDelimited(t *testing.T) {
	services, _ := Resolve([]config.ServiceConfig{svc("a", "h:22", 2200)})
	out, err := InventoryJSON(services, "10.199.0.3")
	if err != nil {
		t.Fatal(err)
	}
	var info ServiceInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &info); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if info.Name != "a" {
		t.Errorf("name = %q", info.Name)
	}
}

func TestFindGitRemoteConflicts(t *testing.T) {
	// The §8.9 regression guard: a service backend matching a git remote host.
	services, _ := Resolve([]config.ServiceConfig{
		svc("gitbox", "git.internal.example:22", 2204),
		svc("switch", "10.20.4.11:22", 2205),
	})
	remotes := map[string]string{
		"origin": "git@git.internal.example:team/repo.git",
	}
	conflicts := FindGitRemoteConflicts(services, remotes)
	if len(conflicts) != 1 {
		t.Fatalf("want one conflict, got %v", conflicts)
	}
	if conflicts[0].Service != "gitbox" || conflicts[0].Remote != "origin" {
		t.Errorf("conflict = %+v", conflicts[0])
	}
	// The switch backend matches nothing.
	if len(FindGitRemoteConflicts(services[1:], remotes)) != 0 {
		t.Error("a non-git backend should not conflict")
	}
}
