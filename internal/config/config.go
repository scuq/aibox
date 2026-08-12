// Package config owns configuration: compiled defaults, the user and project
// YAML files, environment variables and (applied by the commands) flags —
// merged in exactly that order. `aibox config show` prints the resolved
// result.
package config

import (
	"fmt"
)

// Version is the current config schema version.
const Version = 1

// Config is the resolved configuration for one invocation.
type Config struct {
	Version int `yaml:"version"`

	Project      ProjectConfig      `yaml:"project"`
	Image        ImageConfig        `yaml:"image"`
	Assistants   AssistantsConfig   `yaml:"assistants"`
	Runtime      RuntimeConfig      `yaml:"runtime"`
	Git          GitConfig          `yaml:"git"`
	Security     SecurityConfig     `yaml:"security"`
	Egress       EgressConfig       `yaml:"egress"`
	Devcontainer DevcontainerConfig `yaml:"devcontainer"`
	Notes        NotesConfig        `yaml:"notes"`

	// Services are named TCP (or best-effort UDP) endpoints reachable through
	// the relay sidecar (§8). The container is granted named services, not
	// network access — the listener port is the destination, so there is no
	// field the agent can put a different host into. Empty means no relay.
	Services []ServiceConfig `yaml:"services"`
}

// ServiceConfig is one relay listener → backend mapping.
type ServiceConfig struct {
	// Name is how the agent addresses the service (an ssh Host, a services.json
	// entry). Must be unique and usable as an ssh host token.
	Name string `yaml:"name"`
	// Backend is the real "host:port" the relay forwards to. Never disclosed to
	// the container unless BackendDisclosed is set.
	Backend string `yaml:"backend"`
	// Listen is the relay's fixed listener port. Zero auto-allocates from
	// RelayBasePort in declaration order.
	Listen int `yaml:"listen"`
	// Aliases are extra names for the same service (ssh Host tokens too).
	Aliases []string `yaml:"aliases"`
	// Proto is "tcp" (default, haproxy) or "udp" (best-effort socat — see §8.6;
	// no sessions, no TFTP).
	Proto string `yaml:"proto"`
	// MaxConns caps concurrent connections to this listener. Not rate limiting.
	MaxConns int `yaml:"maxConns"`
	// IdleTimeout overrides the default client/server timeout, e.g. "300s".
	IdleTimeout string `yaml:"idleTimeout"`
	// BackendDisclosed puts the real backend address into services.json. Default
	// false: the agent gets a name and a way to reach it, not a map of the
	// network.
	BackendDisclosed bool `yaml:"backendDisclosed"`
}

type ProjectConfig struct {
	// Name defaults to the workspace basename. It appears in derived
	// container/volume names for readability; the project ID is what makes
	// them unique.
	Name string `yaml:"name"`
}

type ImageConfig struct {
	// Reference overrides the image to run outright. Empty means the local
	// default build.
	Reference string `yaml:"reference"`
	Profile   string `yaml:"profile"`
	Release   string `yaml:"release"`
	// Toolchains pins toolchain versions for local builds, e.g. go: "1.26.5".
	Toolchains map[string]string `yaml:"toolchains"`
	// AssistantVersions pins the assistant CLIs baked into a local build,
	// e.g. claude: "2.1.218". Empty means the npm "latest" tag.
	AssistantVersions map[string]string `yaml:"assistantVersions"`
	// Autobuild builds a missing or recipe-stale local image on demand.
	// Disabled it fails instead — for hosts where a surprise multi-minute
	// build is worse than an error.
	Autobuild bool `yaml:"autobuild"`
	// CACertificates is a host path (file or directory) of additional CA
	// certificates (*.crt / *.pem) baked into the image's trust store at
	// build time — a corporate root, a TLS-inspecting proxy's CA. Part of
	// the recipe hash, so changing the certificates rebuilds the image.
	CACertificates string `yaml:"caCertificates"`
}

type AssistantsConfig struct {
	Claude AssistantConfig `yaml:"claude"`
	Codex  AssistantConfig `yaml:"codex"`
}

type AssistantConfig struct {
	Enabled bool `yaml:"enabled"`
}

type RuntimeConfig struct {
	Engine    string `yaml:"engine"`    // podman only in v1
	Workspace string `yaml:"workspace"` // host directory mounted at /work
	// WorkspaceMode read-only mounts /work ro — for review sessions where the
	// agent must not touch the project at all. Everything it needs to write
	// (config volume, /tmp, ~/.cache) is elsewhere, so this is a working setup.
	WorkspaceMode string `yaml:"workspaceMode"` // read-write | read-only
	CPUs          string `yaml:"cpus"`          // empty disables the cpu cap
	Memory        string `yaml:"memory"`        // empty disables the memory cap
	TmpfsSize     string `yaml:"tmpfsSize"`
	// AllowRoot permits rootful podman. Refused by default: rootless is what
	// keeps a container escape from becoming host root.
	AllowRoot bool `yaml:"allowRoot"`
}

type GitConfig struct {
	// History read-only mounts .git read-only over the writable workspace —
	// kernel-enforced: no commit, add, push can write. "none" omits the mount
	// (and masks any .git inside the workspace) for review sessions where the
	// agent should not read branch names or commit messages at all.
	History string `yaml:"history"` // read-only | none
	// Identity is permanently false in aibox: the host ~/.gitconfig is never
	// mounted, no user identity or credential helper reaches the container.
	// The field exists so a config file setting it true fails validation
	// loudly instead of being silently ignored.
	Identity bool `yaml:"identity"`
	// Shim controls the loud git shim on PATH inside the container. "off" is
	// for debugging the shim itself, nothing else — layer 1 (the read-only
	// mount) still enforces.
	Shim string `yaml:"shim"` // loud | off
	// Handoff is the writable directory (relative to /work) where the agent
	// records what it changed, since it cannot commit.
	Handoff string `yaml:"handoff"`
}

type SecurityConfig struct {
	// AllowPackageInstall relaxes the two flags that block `sudo apt-get`:
	// no-new-privileges (blocks the setuid escalation) and cap-drop=ALL (dpkg
	// needs CHOWN/DAC_OVERRIDE etc.).
	AllowPackageInstall bool `yaml:"allowPackageInstall"`
	NoNewPrivileges     bool `yaml:"noNewPrivileges"`
	DropCapabilities    bool `yaml:"dropCapabilities"`
	PidsLimit           int  `yaml:"pidsLimit"`
}

type EgressConfig struct {
	// Mode "open" is podman's default outbound network; "proxy" puts the
	// container on an internal network whose only way out is a squid sidecar
	// enforcing a domain allowlist. The internal network is the enforcement —
	// the proxy env vars only exist so tools work.
	Mode   string `yaml:"mode"` // open | proxy
	Subnet string `yaml:"subnet"`
	// Allowlist entries are added to the project ACL fragment on top of the
	// base and per-assistant lists.
	Allowlist      []string `yaml:"allowlist"`
	ProxyImage     string   `yaml:"proxyImage"`
	ProxyLogDriver string   `yaml:"proxyLogDriver"` // e.g. journald; empty = podman default
	// RelayImage is the haproxy image for the service relay sidecar (§8).
	RelayImage string `yaml:"relayImage"`
}

type DevcontainerConfig struct {
	Name         string             `yaml:"name"`
	RemoveOnStop bool               `yaml:"removeOnStop"`
	VSCode       DevcontainerVSCode `yaml:"vscode"`
}

type DevcontainerVSCode struct {
	Extensions []string `yaml:"extensions"`
}

type NotesConfig struct {
	// Project is the repo's own ainotes layer, concatenated after the image
	// and policy layers so a repo can add conventions but not delete policy.
	Project  string `yaml:"project"`
	MaxBytes int    `yaml:"maxBytes"`
}

// Defaults returns the compiled-in configuration.
func Defaults() Config {
	return Config{
		Version: Version,
		Image: ImageConfig{
			Profile:   "base",
			Autobuild: true,
		},
		Assistants: AssistantsConfig{
			Claude: AssistantConfig{Enabled: true},
		},
		Runtime: RuntimeConfig{
			Engine:        "podman",
			Workspace:     ".",
			WorkspaceMode: "read-write",
			CPUs:          "2",
			Memory:        "4g",
			TmpfsSize:     "512m",
		},
		Git: GitConfig{
			History: "read-only",
			Shim:    "loud",
			Handoff: ".aibox",
		},
		Security: SecurityConfig{
			NoNewPrivileges:  true,
			DropCapabilities: true,
			PidsLimit:        2048,
		},
		Egress: EgressConfig{
			Mode:       "open",
			Subnet:     "10.199.0.0/24",
			ProxyImage: "docker.io/ubuntu/squid:latest",
			RelayImage: "docker.io/library/haproxy:lts-alpine",
		},
		Notes: NotesConfig{
			Project: ".aibox/ainotes.md",
			// 4 KB: the notes carry the tool inventory, the egress/relay
			// model, and the host-hand-off guidance (containers, git push,
			// allowlisting) — the things an agent most often gets wrong here.
			// Still a hard cap enforced at image build; plan open-question #5.
			MaxBytes: 4096,
		},
	}
}

// Validate rejects a resolved configuration that asks for something aibox
// does not do. Loud beats silent: an unknown egress mode or git.identity=true
// must stop the run, not degrade it.
func (c *Config) Validate() error {
	if c.Version != Version {
		return fmt.Errorf("unsupported config version %d (this aibox understands version %d)", c.Version, Version)
	}
	switch c.Egress.Mode {
	case "open", "proxy":
	default:
		return fmt.Errorf("unknown egress mode %q (use: open, proxy)", c.Egress.Mode)
	}
	switch c.Git.History {
	case "read-only", "none":
	default:
		return fmt.Errorf("unknown git.history %q (use: read-only, none)", c.Git.History)
	}
	switch c.Git.Shim {
	case "loud", "off":
	default:
		return fmt.Errorf("unknown git.shim %q (use: loud, off)", c.Git.Shim)
	}
	if c.Git.Identity {
		return fmt.Errorf("git.identity: true is not supported — aibox never mounts the host gitconfig; commits are made by the user on the host")
	}
	switch c.Runtime.WorkspaceMode {
	case "read-write", "read-only":
	default:
		return fmt.Errorf("unknown runtime.workspaceMode %q (use: read-write, read-only)", c.Runtime.WorkspaceMode)
	}
	if c.Runtime.Engine != "podman" {
		return fmt.Errorf("unsupported runtime.engine %q (v1 supports: podman)", c.Runtime.Engine)
	}
	if len(c.Services) > 0 && c.Egress.Mode != "proxy" {
		// The relay lives on the internal network (§8). Without proxy mode the
		// workload has a route out and is not on that network, so the relay is
		// both unreachable and pointless — fail loudly rather than start a
		// sidecar nothing can talk to.
		return fmt.Errorf("services require egress.mode: proxy (the relay lives on the internal network)")
	}
	seen := map[string]bool{}
	for i := range c.Services {
		s := &c.Services[i]
		if s.Name == "" {
			return fmt.Errorf("service %d has no name", i)
		}
		if s.Backend == "" {
			return fmt.Errorf("service %q has no backend", s.Name)
		}
		switch s.Proto {
		case "", "tcp", "udp":
		default:
			return fmt.Errorf("service %q: unknown proto %q (use: tcp, udp)", s.Name, s.Proto)
		}
		for _, name := range append([]string{s.Name}, s.Aliases...) {
			if seen[name] {
				return fmt.Errorf("service name/alias %q is used more than once", name)
			}
			seen[name] = true
		}
	}
	return nil
}
