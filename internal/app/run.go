package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/scuq/aibox/internal/assistant"
	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/doctor"
	"github.com/scuq/aibox/internal/egress"
	"github.com/scuq/aibox/internal/git"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/relay"
	"github.com/scuq/aibox/internal/runtime"
	"golang.org/x/term"
)

type runOptions struct {
	workspace   string
	imageRef    string
	egressMode  string
	readOnly    bool
	allowPkg    bool
	seedCreds   bool
	seedConfig  bool
	memory      string
	cpus        string
	noLimits    bool
	rebuild     bool
	noCache     bool
	noAutobuild bool
	allowRoot   bool
	dryRun      bool
}

func newRunFlagSet(o *runOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&o.workspace, "w", "", "host directory to mount at /work")
	fs.StringVar(&o.workspace, "workspace", "", "host directory to mount at /work")
	fs.StringVar(&o.imageRef, "i", "", "image to run")
	fs.StringVar(&o.imageRef, "image", "", "image to run")
	fs.StringVar(&o.egressMode, "egress", "", "egress mode: open | proxy")
	fs.BoolVar(&o.readOnly, "ro", false, "mount the workspace read-only")
	fs.BoolVar(&o.allowPkg, "allow-pkg", false, "enable sudo apt-get inside")
	fs.BoolVar(&o.seedCreds, "seed-creds", false, "copy the host login into the auth volume")
	fs.BoolVar(&o.seedConfig, "seed-config", false, "copy host Claude settings in")
	fs.StringVar(&o.memory, "memory", "", `memory cap ("none" disables)`)
	fs.StringVar(&o.cpus, "cpus", "", `cpu cap ("none" disables)`)
	fs.BoolVar(&o.noLimits, "no-limits", false, "disable memory and cpu caps")
	fs.BoolVar(&o.rebuild, "rebuild", false, "rebuild the image before running")
	fs.BoolVar(&o.noCache, "no-cache", false, "ignore the layer cache when building")
	fs.BoolVar(&o.noAutobuild, "no-autobuild", false, "fail instead of building a missing/stale image")
	fs.BoolVar(&o.allowRoot, "allow-root", false, "permit rootful podman")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the podman command instead of running it")
	return fs
}

// applyRunFlags is the final layer of the config merge.
func applyRunFlags(cfg *config.Config, o *runOptions) {
	if o.workspace != "" {
		cfg.Runtime.Workspace = o.workspace
	}
	if o.imageRef != "" {
		cfg.Image.Reference = o.imageRef
	}
	if o.egressMode != "" {
		cfg.Egress.Mode = o.egressMode
	}
	if o.readOnly {
		cfg.Runtime.WorkspaceMode = "read-only"
	}
	if o.allowPkg {
		cfg.Security.AllowPackageInstall = true
	}
	if o.memory != "" {
		cfg.Runtime.Memory = o.memory
	}
	if cfg.Runtime.Memory == "none" {
		cfg.Runtime.Memory = ""
	}
	if o.cpus != "" {
		cfg.Runtime.CPUs = o.cpus
	}
	if cfg.Runtime.CPUs == "none" {
		cfg.Runtime.CPUs = ""
	}
	if o.noLimits {
		cfg.Runtime.Memory = ""
		cfg.Runtime.CPUs = ""
	}
	if o.noAutobuild {
		cfg.Image.Autobuild = false
	}
	if o.allowRoot {
		cfg.Runtime.AllowRoot = true
	}
}

// resolveWorkspace canonicalises and sanity-checks the workspace directory.
func resolveWorkspace(ws string, p *output.Printer) (string, error) {
	if ws == "" || ws == "." {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		ws = wd
	}
	st, err := os.Stat(ws)
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("workspace %q is not a directory", ws)
	}
	canon, err := project.Canonical(ws)
	if err != nil {
		return "", err
	}
	if canon == "/" {
		return "", errors.New("refusing to mount / as the workspace")
	}
	if home, err := os.UserHomeDir(); err == nil && canon == home {
		p.Warn("workspace is your entire home directory — the container will see all of it")
	}
	return canon, nil
}

// requireRootless enforces aibox's core security assumption: containers run
// as your unprivileged user, so a container escape cannot become host root,
// and files written to the workspace stay owned by you. Running rootful
// throws that away, so it has to be asked for explicitly.
func requireRootless(allowRoot bool, p *output.Printer) error {
	if doctor.IsRootless() {
		return nil
	}
	if allowRoot {
		p.Warn("running rootful podman as root — container isolation is much weaker")
		return nil
	}
	return errors.New(`refusing to run as root.

  aibox is designed for rootless podman: containers run as your unprivileged
  user, so a container escape cannot become host root, and files written to
  the workspace stay owned by you.

  Run it as your normal user (no sudo). If you really need rootful podman,
  pass --allow-root (or set AIBOX_ALLOW_ROOT=1)`)
}

// sessionInputs is everything composeSpec needs, resolved and validated. Kept
// as a struct so tests can build specs without a CLI, podman, or a real host.
type sessionInputs struct {
	Config      config.Config
	Assistant   assistant.Assistant
	Workspace   string // canonical
	ProjectID   string
	ProjectName string
	GitMounts   git.Mounts
	Interactive bool
	SeedCreds   string // host credentials file to seed, empty for none
	SeedConfig  []string
	HostHome    string
	TermEnv     string
	ColorTerm   string
	Args        []string // passthrough args for the assistant

	// RelaySSHConfigPath / RelayServicesPath are host files (ssh config and the
	// services.json inventory) bind-mounted read-only for the entrypoint to
	// install into the container. Empty when no services are configured.
	RelaySSHConfigPath string
	RelayServicesPath  string
}

// composeSpec is the pure heart of `aibox run`: resolved inputs go in, the
// exact podman argv comes out (via Spec.Argv). No side effects — that is what
// makes --dry-run exact.
func composeSpec(in sessionInputs) (*container.Spec, error) {
	cfg := in.Config

	wsOptions := []string{}
	if cfg.Runtime.WorkspaceMode == "read-only" {
		// Read-only /work: an analysis or review session cannot touch the
		// project at all. Everything the assistant needs to write during a
		// run (the config volume, /tmp, ~/.cache) is elsewhere, so this is a
		// working setup.
		wsOptions = append(wsOptions, "ro")
	}

	mounts := []container.Mount{
		{Type: container.MountBind, Source: in.Workspace, Dest: "/work", Options: wsOptions},
	}
	mounts = append(mounts, in.Assistant.ConfigMounts(in.ProjectID)...)
	mounts = append(mounts, container.Mount{
		Type: container.MountVolume, Source: project.CacheVolumeName, Dest: "/home/aibox/.cache",
	})
	mounts = append(mounts, in.GitMounts.Mounts...)

	env := []container.EnvVar{
		{Name: "TERM", Value: in.TermEnv},
		{Name: "COLORTERM", Value: in.ColorTerm},
	}
	for _, e := range in.Assistant.Environment() {
		// A host-forwarded variable (an API key) is only named when it is
		// actually set; naming an unset one would be noise at best.
		if e.FromHost && os.Getenv(e.Name) == "" {
			continue
		}
		env = append(env, e)
	}
	if cfg.Git.Shim == "off" {
		// Debugging the shim itself; the read-only mount still enforces.
		env = append(env, container.EnvVar{Name: "AIBOX_GIT_SHIM", Value: "off"})
	}
	// The entrypoint renders this run's actual constraints into the policy
	// layer of the environment notes.
	env = append(env,
		container.EnvVar{Name: "AIBOX_EGRESS_MODE", Value: cfg.Egress.Mode},
		container.EnvVar{Name: "AIBOX_GIT_HISTORY", Value: cfg.Git.History},
	)

	spec := &container.Spec{
		Image:    imageRefFor(cfg),
		Hostname: "aibox",
		Remove:   true, // `aibox run` is the ephemeral class: never silently persistent
		// Map the host user onto the image's aibox user (uid/gid 1000); see
		// Spec.UserNSUID for why plain keep-id is wrong.
		UserNSUID: 1000, UserNSGID: 1000,
		Interactive: in.Interactive,
		TTY:         in.Interactive,
		Workdir:     "/work",
		Mounts:      mounts,
		PidsLimit:   cfg.Security.PidsLimit,
		Memory:      cfg.Runtime.Memory,
		CPUs:        cfg.Runtime.CPUs,
		Command:     in.Assistant.Arguments(in.Args),
	}

	// Fresh nosuid tmpfs /tmp: no dropped setuid binary can be executed from
	// it, and it's wiped each run. exec is kept so build tools can run there.
	spec.Tmpfs = append(spec.Tmpfs, container.TmpfsMount{
		Dest:    "/tmp",
		Options: []string{"rw", "nosuid", "nodev", "exec", "size=" + cfg.Runtime.TmpfsSize},
	})
	// /run/aibox holds the generated environment notes. It is a tmpfs because
	// /run in the container is root-owned; uid/gid make it writable by the
	// aibox user the entrypoint runs as.
	spec.Tmpfs = append(spec.Tmpfs, container.TmpfsMount{
		Dest:    "/run/aibox",
		Options: []string{"rw", "nosuid", "nodev", "size=64k", "uid=1000", "gid=1000"},
	})
	spec.Tmpfs = append(spec.Tmpfs, in.GitMounts.Tmpfs...)

	// Hardening. --allow-pkg relaxes the two flags that block `sudo apt-get`.
	// Installs land in the container's throwaway layer; the userns mapping
	// means /work edits still come out owned by the caller.
	if !cfg.Security.AllowPackageInstall {
		spec.NoNewPrivileges = cfg.Security.NoNewPrivileges
		spec.CapDropAll = cfg.Security.DropCapabilities
	}

	if cfg.Egress.Mode == "proxy" {
		proxyURL, err := egress.ProxyURL(cfg.Egress.Subnet)
		if err != nil {
			return nil, err
		}
		spec.Network = egress.NetInternal
		// Both cases so that curl (upper) and most CLIs (lower) agree.
		env = append(env,
			container.EnvVar{Name: "HTTPS_PROXY", Value: proxyURL},
			container.EnvVar{Name: "https_proxy", Value: proxyURL},
			container.EnvVar{Name: "HTTP_PROXY", Value: proxyURL},
			container.EnvVar{Name: "http_proxy", Value: proxyURL},
			container.EnvVar{Name: "NO_PROXY", Value: egress.NoProxy},
			container.EnvVar{Name: "no_proxy", Value: egress.NoProxy},
		)
	}

	// Credential seeding is opt-in. By default the host's refresh token is
	// never exposed to the container, not even once: you log in inside
	// instead, and that login lives in the auth volume. The seed file is
	// mounted read-only; the entrypoint copies it in — every time it is
	// passed, so it doubles as the fix for a token that went stale.
	if in.SeedCreds != "" {
		auth := in.Assistant.Auth()
		if auth != nil {
			mountsSeed := container.Mount{
				Type: container.MountBind, Source: in.SeedCreds,
				Dest:    "/run/host-aibox/" + auth.CredentialFile,
				Options: []string{"ro"},
			}
			spec.Mounts = append(spec.Mounts, mountsSeed)
		}
	}
	for _, f := range in.SeedConfig {
		spec.Mounts = append(spec.Mounts, container.Mount{
			Type: container.MountBind, Source: f,
			Dest:    "/run/host-aibox/" + filepath.Base(f),
			Options: []string{"ro"},
		})
	}
	if len(in.SeedConfig) > 0 {
		env = append(env, container.EnvVar{Name: "HOST_HOME", Value: in.HostHome})
	}

	// Relay client wiring: the ssh config and services inventory are mounted
	// read-only as seed sources; the entrypoint installs ~/.ssh/config (0400)
	// and /run/aibox/services.json, and folds the services into the notes.
	if in.RelaySSHConfigPath != "" {
		spec.Mounts = append(spec.Mounts, container.Mount{
			Type: container.MountBind, Source: in.RelaySSHConfigPath,
			Dest: "/run/host-aibox/ssh_config", Options: []string{"ro"},
		})
	}
	if in.RelayServicesPath != "" {
		spec.Mounts = append(spec.Mounts, container.Mount{
			Type: container.MountBind, Source: in.RelayServicesPath,
			Dest: "/run/host-aibox/services.json", Options: []string{"ro"},
		})
		env = append(env, container.EnvVar{Name: "AIBOX_HAS_SERVICES", Value: "1"})
	}
	spec.Env = env

	labels := container.BaseLabels(container.RoleWorkspace)
	labels[container.LabelMode] = container.ModeStandalone
	labels[container.LabelProjectID] = in.ProjectID
	labels[container.LabelProjectPath] = in.Workspace
	labels[container.LabelAssistant] = in.Assistant.Name()
	spec.Labels = labels

	return spec, nil
}

func imageRefFor(cfg config.Config) string {
	if cfg.Image.Reference != "" {
		return cfg.Image.Reference
	}
	return "localhost/aibox:latest"
}

func cmdRun(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) int {
	var o runOptions
	fs := newRunFlagSet(&o)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return 1
	}
	rest := fs.Args()
	if len(rest) == 0 {
		p.Error("run needs an assistant: aibox run claude|codex|shell [args...]")
		return 1
	}
	asst, err := assistant.Lookup(rest[0])
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	passthrough := rest[1:]

	ws, err := resolveWorkspace(o.workspace, p)
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	cfg, err := config.Load(ws)
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	applyRunFlags(&cfg, &o)
	if err := cfg.Validate(); err != nil {
		p.Error("%s", err)
		return 1
	}

	projectID, err := project.ID(ws)
	if err != nil {
		p.Error("%s", err)
		return 1
	}

	// Rootless podman without a delegated memory controller cannot apply
	// --memory/--cpus; it errors out rather than ignoring them, so drop the
	// caps instead of failing the run.
	if (cfg.Runtime.Memory != "" || cfg.Runtime.CPUs != "") && !doctor.CgroupLimitsAvailable() {
		p.Warn("this host cannot enforce rootless memory/cpu limits (needs cgroups v2 with a delegated memory controller) — disabling them")
		cfg.Runtime.Memory = ""
		cfg.Runtime.CPUs = ""
	}

	gitInfo := git.Resolve(ws)
	gitMounts := git.Plan(gitInfo, cfg.Git.History, ws)
	for _, w := range gitMounts.Warnings {
		p.Warn("%s", w)
	}

	// Relay client wiring and the git-remote guard (§8.9). Generated on the
	// host, into temp files that live for the container's lifetime.
	var relayTmp string
	relaySSHPath, relaySvcPath, relayTmp, err := prepareRelayFiles(p, cfg, ws)
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	if relayTmp != "" {
		defer os.RemoveAll(relayTmp)
	}

	in := sessionInputs{
		Config:             cfg,
		Assistant:          asst,
		Workspace:          ws,
		ProjectID:          projectID,
		ProjectName:        filepath.Base(ws),
		GitMounts:          gitMounts,
		Interactive:        term.IsTerminal(int(os.Stdin.Fd())),
		TermEnv:            envOr("TERM", "xterm-256color"),
		ColorTerm:          envOr("COLORTERM", "truecolor"),
		Args:               passthrough,
		RelaySSHConfigPath: relaySSHPath,
		RelayServicesPath:  relaySvcPath,
	}

	if o.seedCreds {
		seed := hostCredentialsPath(asst)
		if seed != "" && fileNonEmpty(seed) {
			in.SeedCreds = seed
		} else {
			p.Warn("--seed-creds: no host credentials at %s — you'll log in inside the container", seed)
		}
	}
	if o.seedConfig {
		home, _ := os.UserHomeDir()
		in.HostHome = home
		for _, f := range []string{"settings.json", "statusline-command.sh"} {
			path := filepath.Join(home, ".claude", f)
			if fileNonEmpty(path) {
				in.SeedConfig = append(in.SeedConfig, path)
			}
		}
	}

	spec, err := composeSpec(in)
	if err != nil {
		p.Error("%s", err)
		return 1
	}
	argv := spec.Argv(doctor.SELinuxEnforcing())

	if cfg.Egress.Mode == "proxy" {
		p.Info("egress filtered: allowlisted domains only, via %s — 'aibox egress denied' shows what was blocked", egress.ProxyName)
		p.Info("note: git over SSH cannot leave the internal network; use https remotes")
	}
	if cfg.Runtime.WorkspaceMode == "read-only" {
		p.Info("workspace mounted read-only")
	}

	if o.dryRun {
		fmt.Println("podman " + container.ShellQuote(argv))
		return 0
	}

	if !rt.Available() {
		p.Error("podman is not installed.\n  Debian/Ubuntu:  sudo apt-get install -y podman\n  Fedora/RHEL:    sudo dnf install -y podman\n  Arch:           sudo pacman -S podman\n  macOS:          brew install podman && podman machine init && podman machine start")
		return 1
	}
	if err := requireRootless(cfg.Runtime.AllowRoot, p); err != nil {
		p.Error("%s", err)
		return 1
	}
	if err := ensureImage(ctx, p, rt, cfg, o.rebuild, o.noCache); err != nil {
		p.Error("%s", err)
		return 1
	}
	if err := ensureVolumes(ctx, rt, p, asst, projectID, ws); err != nil {
		p.Error("%s", err)
		return 1
	}
	// Create the assistant's instruction file (CLAUDE.md / AGENTS.md) pointing
	// at the environment notes, if the repo has none. Only when the workspace
	// is writable — a --ro review session must not write to the project.
	if cfg.Runtime.WorkspaceMode != "read-only" {
		ensureAssistantDocs(p, ws, []assistant.Assistant{asst})
	}
	if cfg.Egress.Mode == "proxy" {
		mgr := newEgressManager(rt, p, cfg, projectID)
		if err := mgr.Ensure(ctx); err != nil {
			p.Error("%s", err)
			return 1
		}
	}
	if len(cfg.Services) > 0 {
		rmgr, err := newRelayManager(rt, p, cfg)
		if err != nil {
			p.Error("%s", err)
			return 1
		}
		if err := rmgr.Ensure(ctx); err != nil {
			p.Error("%s", err)
			return 1
		}
	}
	if in.SeedCreds == "" && asst.Auth() != nil && !o.seedCreds {
		maybeFirstLoginHint(ctx, rt, p, asst, passthrough)
	}

	if err := rt.Run(ctx, argv); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		p.Error("%s", err)
		return 1
	}
	return 0
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func fileNonEmpty(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

// hostCredentialsPath expands the assistant's default host credential
// location; AIBOX_HOST_CREDS overrides it.
func hostCredentialsPath(asst assistant.Assistant) string {
	if v := os.Getenv("AIBOX_HOST_CREDS"); v != "" {
		return v
	}
	auth := asst.Auth()
	if auth == nil {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if len(auth.HostCredentials) > 1 && auth.HostCredentials[:2] == "~/" {
		return filepath.Join(home, auth.HostCredentials[2:])
	}
	return auth.HostCredentials
}

// ensureVolumes creates the assistant's config/auth volumes and the shared
// cache volume, labelled so listing and pruning never has to guess from names.
func ensureVolumes(ctx context.Context, rt runtime.Runtime, p *output.Printer, asst assistant.Assistant, projectID, workspace string) error {
	ensure := func(name, kind string, extra map[string]string) error {
		exists, err := rt.VolumeExists(ctx, name)
		if err != nil || exists {
			return err
		}
		labels := container.BaseLabels(container.RoleVolume)
		labels[container.LabelVolumeKind] = kind
		for k, v := range extra {
			labels[k] = v
		}
		if err := rt.VolumeCreate(ctx, name, labels); err != nil {
			return err
		}
		p.Info("created %s volume %s", kind, name)
		return nil
	}
	if auth := asst.Auth(); auth != nil {
		name := asst.Name()
		if err := ensure(project.AuthVolumeName(name), container.VolumeAuth,
			map[string]string{container.LabelAssistant: name}); err != nil {
			return err
		}
		if err := ensure(project.ConfigVolumeName(name, projectID), container.VolumeConfig,
			map[string]string{
				container.LabelAssistant:   name,
				container.LabelProjectID:   projectID,
				container.LabelProjectPath: workspace,
			}); err != nil {
			return err
		}
	}
	return ensure(project.CacheVolumeName, container.VolumeCache, nil)
}

// maybeFirstLoginHint explains the in-container login before the prompt
// appears unexplained — and refuses to pretend a headless run will work.
func maybeFirstLoginHint(ctx context.Context, rt runtime.Runtime, p *output.Printer, asst assistant.Assistant, args []string) {
	auth := asst.Auth()
	authVol := project.AuthVolumeName(asst.Name())
	if exists, _ := rt.VolumeExists(ctx, authVol); exists {
		if mp, err := rt.VolumeMountpoint(ctx, authVol); err == nil && fileNonEmpty(filepath.Join(mp, auth.CredentialFile)) {
			return // logged in
		}
	}
	if apiKeySet(asst) {
		return
	}
	headless := false
	for _, a := range args {
		if a == "-p" || a == "--print" {
			headless = true
			break
		}
	}
	if headless {
		p.Warn("no login in volume '%s' and no API key — a headless run has no way to log in.\n  Run 'aibox run %s' once interactively to log in, or pass --seed-creds to copy your host login into the volume.", authVol, asst.Name())
		return
	}
	p.Info("no login in volume '%s' yet — %s will ask you to log in inside the container", authVol, asst.Name())
	p.Info("that is a one-time step for all your projects; the login is stored there, not on your host")
	if fileNonEmpty(hostCredentialsPath(asst)) {
		p.Info("to reuse your host login instead, re-run with --seed-creds")
	}
}

// prepareRelayFiles resolves the configured services, warns on git-remote
// conflicts (§8.9), and writes the ssh config and services.json to a temp dir
// bind-mounted into the container. Returns empty paths and an empty tmp dir
// when no services are configured.
func prepareRelayFiles(p *output.Printer, cfg config.Config, workspace string) (sshPath, svcPath, tmpDir string, err error) {
	if len(cfg.Services) == 0 {
		return "", "", "", nil
	}
	services, err := relay.Resolve(cfg.Services)
	if err != nil {
		return "", "", "", err
	}
	relayIP, err := relay.RelayIP(cfg.Egress.Subnet)
	if err != nil {
		return "", "", "", err
	}

	// The git-remote guard: a service backend that matches a workspace git
	// remote makes SSH to that host possible, so git push over it would work
	// and the read-only .git mount is the only thing left standing. Loud, never
	// a refusal.
	for _, c := range relay.FindGitRemoteConflicts(services, git.Remotes(workspace)) {
		p.Warn("relay service %q (backend %s) matches git remote %q (%s) — reaching it re-opens a network path git push could use; the read-only .git mount still blocks writes, but this is no longer belt-and-braces", c.Service, c.Backend, c.Remote, c.URL)
	}

	tmpDir, err = os.MkdirTemp("", "aibox-relay.")
	if err != nil {
		return "", "", "", err
	}
	sshPath = filepath.Join(tmpDir, "ssh_config")
	if err := os.WriteFile(sshPath, []byte(relay.SSHConfig(services, relayIP)), 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", "", err
	}
	svcPath = filepath.Join(tmpDir, "services.json")
	inv, err := relay.InventoryJSON(services, relayIP)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", "", "", err
	}
	if err := os.WriteFile(svcPath, inv, 0o644); err != nil {
		os.RemoveAll(tmpDir)
		return "", "", "", err
	}
	return sshPath, svcPath, tmpDir, nil
}

func apiKeySet(asst assistant.Assistant) bool {
	for _, e := range asst.Environment() {
		if e.FromHost && os.Getenv(e.Name) != "" {
			return true
		}
	}
	return false
}
