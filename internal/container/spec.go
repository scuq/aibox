package container

import (
	"fmt"
	"sort"
	"strings"
)

// EnvVar is one --env argument. FromHost forwards the variable by name only,
// so podman imports the value from aibox's own environment: the value never
// reaches podman's argv, where `ps` on a shared host would show it and where
// --dry-run output would print it for pasting into a bug report. Secrets are
// always FromHost.
type EnvVar struct {
	Name     string
	Value    string
	FromHost bool
}

// Spec is everything needed to render a `podman run` (or create) argv. It is
// pure data: building one has no side effects, which is what makes --dry-run
// exact — the printed argv is the same slice the real run executes.
type Spec struct {
	Image    string
	Name     string // empty for anonymous (--rm) containers
	Hostname string
	Remove   bool // --rm: the ephemeral class
	Detach   bool

	// UserNSUID/GID map the host user onto the image's aibox user (uid/gid
	// 1000) rather than onto its own numeric id. Plain keep-id maps the
	// caller to the same number inside, which for any host uid other than
	// 1000 leaves the process (still uid 1000, from the image's USER) unable
	// to write a workspace owned by the caller.
	UserNSUID, UserNSGID int

	Interactive bool
	TTY         bool

	Workdir string
	Mounts  []Mount
	Tmpfs   []TmpfsMount
	Env     []EnvVar

	// Resource limits cap the blast radius of a runaway or hostile process;
	// PidsLimit alone only stops fork bombs. Memory sets --memory-swap equal
	// to --memory, disabling swap growth. Empty strings disable a cap.
	PidsLimit int
	Memory    string
	CPUs      string

	// NoNewPrivileges + CapDropAll are the default hardening posture.
	// --allow-pkg relaxes exactly these two: no-new-privileges blocks the
	// sudo setuid escalation, and dpkg needs CHOWN/DAC_OVERRIDE etc.
	NoNewPrivileges bool
	CapDropAll      bool

	// Network is set only in egress proxy mode: the internal network is the
	// enforcement (no route out, no external DNS); the proxy env vars in Env
	// just tell tools where the one door is.
	Network string

	Labels map[string]string

	// Command is what the entrypoint receives.
	Command []string
}

// Argv renders the spec, starting with "run". selinuxRelabel adds the mount
// relabel option on SELinux-enforcing hosts.
func (s *Spec) Argv(selinuxRelabel bool) []string {
	args := []string{"run"}
	if s.Remove {
		args = append(args, "--rm")
	}
	if s.Detach {
		args = append(args, "--detach")
	}
	if s.Name != "" {
		args = append(args, "--name", s.Name)
	}
	if s.Hostname != "" {
		args = append(args, "--hostname", s.Hostname)
	}
	if s.UserNSUID > 0 || s.UserNSGID > 0 {
		args = append(args, fmt.Sprintf("--userns=keep-id:uid=%d,gid=%d", s.UserNSUID, s.UserNSGID))
	}
	if s.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", s.PidsLimit))
	}
	for _, m := range s.Mounts {
		args = append(args, m.Args(selinuxRelabel)...)
	}
	for _, t := range s.Tmpfs {
		args = append(args, t.Args()...)
	}
	if s.Workdir != "" {
		args = append(args, "--workdir", s.Workdir)
	}
	for _, e := range s.Env {
		if e.FromHost {
			args = append(args, "--env", e.Name)
		} else {
			args = append(args, "--env", fmt.Sprintf("%s=%s", e.Name, e.Value))
		}
	}
	if s.Interactive {
		args = append(args, "--interactive")
	}
	if s.TTY {
		args = append(args, "--tty")
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory, "--memory-swap", s.Memory)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	if s.NoNewPrivileges {
		args = append(args, "--security-opt", "no-new-privileges")
	}
	if s.CapDropAll {
		args = append(args, "--cap-drop=ALL")
	}
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	// Sorted so the argv — and therefore --dry-run output and golden tests —
	// is deterministic.
	labels := make([]string, 0, len(s.Labels))
	for k, v := range s.Labels {
		labels = append(labels, fmt.Sprintf("%s=%s", k, v))
	}
	sort.Strings(labels)
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	args = append(args, s.Image)
	args = append(args, s.Command...)
	return args
}

// ShellQuote renders an argv for human eyes (--dry-run output), quoting only
// where needed.
func ShellQuote(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		if a == "" || strings.ContainsAny(a, " \t\n\"'\\$&|;<>(){}[]*?~#") {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}
