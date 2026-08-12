// Package assistant models the selectable AI assistants. The container image
// is the environment; assistants are components chosen per invocation, each
// with its own credential storage — Claude and Codex never share auth or
// config volumes.
package assistant

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/scuq/aibox/assets"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/project"
)

// AuthProtocol describes how an assistant's credentials move between the
// shared auth volume and the per-project config volume. The protocol itself
// (the fingerprint-and-compare hand-off) is implemented once, in
// assets/entrypoint.sh, parameterised by these paths; two assistants means
// two independent instances of it.
type AuthProtocol struct {
	// ConfigDir is the in-container per-project config directory (the config
	// volume's mountpoint).
	ConfigDir string
	// AuthDir is the in-container mountpoint of the shared login volume.
	AuthDir string
	// CredentialFile is the file the hand-off carries, relative to both dirs.
	CredentialFile string
	// HostCredentials is the default host path --seed-creds copies in.
	HostCredentials string
}

// Assistant is one selectable assistant. Adding a new one must not require
// touching lifecycle code.
type Assistant interface {
	Name() string
	Executable() string

	// InstructionFile is the repo-root document the assistant reads for
	// project instructions (CLAUDE.md for Claude, AGENTS.md for Codex). Empty
	// for assistants that have none (shell). aibox creates it referencing the
	// environment notes when it does not already exist.
	InstructionFile() string

	// ConfigMounts are the assistant's volumes for a project: the per-project
	// config volume and the shared auth volume.
	ConfigMounts(projectID string) []container.Mount
	// Environment is what the entrypoint needs to select this assistant's
	// credential layout, plus any API-key forwarding (by name only — values
	// must never reach argv).
	Environment() []container.EnvVar
	// Arguments turns the user's trailing args into the container command.
	Arguments(args []string) []string

	// RequiredDomains is the assistant's egress allowlist fragment.
	RequiredDomains() []string
	// Auth returns nil for assistants with no credential hand-off (shell).
	Auth() *AuthProtocol
}

// Normalize maps input aliases onto canonical assistant names. "kodex" is
// accepted and normalised to "codex" everywhere.
func Normalize(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "kodex":
		return "codex"
	case "":
		return "claude"
	default:
		return strings.ToLower(strings.TrimSpace(name))
	}
}

// Lookup returns the named assistant.
func Lookup(name string) (Assistant, error) {
	switch Normalize(name) {
	case "claude":
		return Claude{}, nil
	case "codex":
		return Codex{}, nil
	case "shell", "none":
		return Shell{}, nil
	default:
		return nil, fmt.Errorf("unknown assistant %q (use: claude, codex, shell)", name)
	}
}

// domainsFromAllowlist reads the non-comment lines of an embedded allowlist
// fragment, so RequiredDomains and the egress fragment can never drift apart.
func domainsFromAllowlist(asset string) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(assets.Read(asset)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Claude is Claude Code.
type Claude struct{}

func (Claude) Name() string            { return "claude" }
func (Claude) Executable() string      { return "claude" }
func (Claude) InstructionFile() string { return "CLAUDE.md" }

func (Claude) ConfigMounts(projectID string) []container.Mount {
	return []container.Mount{
		{Type: container.MountVolume, Source: project.ConfigVolumeName("claude", projectID), Dest: "/home/aibox/.claude"},
		{Type: container.MountVolume, Source: project.AuthVolumeName("claude"), Dest: "/home/aibox/.claude-auth"},
	}
}

func (Claude) Environment() []container.EnvVar {
	return []container.EnvVar{
		{Name: "AIBOX_ASSISTANT", Value: "claude"},
		// Forwarded by name, so podman imports it from aibox's own
		// environment: the key never reaches podman's argv, where `ps` on a
		// shared host would show it and where --dry-run output would print it
		// for pasting into a bug report.
		{Name: "ANTHROPIC_API_KEY", FromHost: true},
	}
}

func (Claude) Arguments(args []string) []string {
	return append([]string{"claude"}, args...)
}

func (Claude) RequiredDomains() []string {
	return domainsFromAllowlist("allowlists/claude.txt")
}

func (Claude) Auth() *AuthProtocol {
	return &AuthProtocol{
		ConfigDir:       "/home/aibox/.claude",
		AuthDir:         "/home/aibox/.claude-auth",
		CredentialFile:  ".credentials.json",
		HostCredentials: "~/.claude/.credentials.json",
	}
}

// Codex is OpenAI's Codex CLI.
type Codex struct{}

func (Codex) Name() string            { return "codex" }
func (Codex) Executable() string      { return "codex" }
func (Codex) InstructionFile() string { return "AGENTS.md" }

func (Codex) ConfigMounts(projectID string) []container.Mount {
	return []container.Mount{
		{Type: container.MountVolume, Source: project.ConfigVolumeName("codex", projectID), Dest: "/home/aibox/.codex"},
		{Type: container.MountVolume, Source: project.AuthVolumeName("codex"), Dest: "/home/aibox/.codex-auth"},
	}
}

func (Codex) Environment() []container.EnvVar {
	return []container.EnvVar{
		{Name: "AIBOX_ASSISTANT", Value: "codex"},
		// By name only — see Claude.Environment.
		{Name: "OPENAI_API_KEY", FromHost: true},
	}
}

func (Codex) Arguments(args []string) []string {
	return append([]string{"codex"}, args...)
}

func (Codex) RequiredDomains() []string {
	return domainsFromAllowlist("allowlists/codex.txt")
}

func (Codex) Auth() *AuthProtocol {
	return &AuthProtocol{
		ConfigDir:       "/home/aibox/.codex",
		AuthDir:         "/home/aibox/.codex-auth",
		CredentialFile:  "auth.json",
		HostCredentials: "~/.codex/auth.json",
	}
}

// Shell is a plain shell session: no assistant, no credential hand-off, no
// assistant-specific egress needs.
type Shell struct{}

func (Shell) Name() string            { return "shell" }
func (Shell) Executable() string      { return "bash" }
func (Shell) InstructionFile() string { return "" }

func (Shell) ConfigMounts(projectID string) []container.Mount { return nil }

func (Shell) Environment() []container.EnvVar {
	return []container.EnvVar{{Name: "AIBOX_ASSISTANT", Value: "none"}}
}

func (Shell) Arguments(args []string) []string {
	return append([]string{"bash"}, args...)
}

func (Shell) RequiredDomains() []string { return nil }
func (Shell) Auth() *AuthProtocol       { return nil }
