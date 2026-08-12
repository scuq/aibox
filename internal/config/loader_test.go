package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Transcribed from the `generic env vars are not inherited` section of the
// bclaude test suite. IMAGE, WORKSPACE and VOLUME are ordinary words that
// projects and CI export for their own purposes, and these settings decide
// what gets mounted and what cleanup deletes.
func TestGenericEnvVarsAreNotInherited(t *testing.T) {
	t.Setenv("IMAGE", "evil/img:1")
	t.Setenv("WORKSPACE", "/etc")
	t.Setenv("VOLUME", "evil-vol")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // isolate from any real user config

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Reference == "evil/img:1" {
		t.Error("a stray IMAGE export was inherited")
	}
	if cfg.Runtime.Workspace == "/etc" {
		t.Error("a stray WORKSPACE export was inherited")
	}
}

func TestAiboxEnvVarsAreRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AIBOX_IMAGE", "example.com/img:1")
	t.Setenv("AIBOX_EGRESS", "proxy")
	t.Setenv("AIBOX_RO", "1")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image.Reference != "example.com/img:1" {
		t.Errorf("AIBOX_IMAGE not applied: %q", cfg.Image.Reference)
	}
	if cfg.Egress.Mode != "proxy" {
		t.Errorf("AIBOX_EGRESS not applied: %q", cfg.Egress.Mode)
	}
	if cfg.Runtime.WorkspaceMode != "read-only" {
		t.Errorf("AIBOX_RO not applied: %q", cfg.Runtime.WorkspaceMode)
	}
}

func TestMergeOrder(t *testing.T) {
	userDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userDir)
	if err := os.MkdirAll(filepath.Join(userDir, "aibox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// User config sets memory and egress mode.
	userCfg := "version: 1\nruntime:\n  memory: 8g\negress:\n  mode: proxy\n"
	if err := os.WriteFile(filepath.Join(userDir, "aibox", "config.yaml"), []byte(userCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Project config overrides memory but not egress.
	ws := t.TempDir()
	projCfg := "version: 1\nruntime:\n  memory: 2g\n"
	if err := os.WriteFile(filepath.Join(ws, ProjectConfigName), []byte(projCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Memory != "2g" {
		t.Errorf("project config should override user config: memory = %q", cfg.Runtime.Memory)
	}
	if cfg.Egress.Mode != "proxy" {
		t.Errorf("user config should survive where the project is silent: egress = %q", cfg.Egress.Mode)
	}
	// Defaults survive where both are silent.
	if cfg.Runtime.CPUs != "2" {
		t.Errorf("default cpus lost in the merge: %q", cfg.Runtime.CPUs)
	}
	// Env is the layer after files.
	t.Setenv("AIBOX_MEMORY", "16g")
	cfg, err = Load(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Runtime.Memory != "16g" {
		t.Errorf("env should override files: memory = %q", cfg.Runtime.Memory)
	}
}

func TestUnknownKeysAreErrors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ws := t.TempDir()
	// A typo'd key silently ignored is a policy the user believes is active
	// and is not.
	bad := "version: 1\negress:\n  moode: proxy\n"
	if err := os.WriteFile(filepath.Join(ws, ProjectConfigName), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ws); err == nil {
		t.Error("an unknown config key should be an error")
	}
}

func TestValidate(t *testing.T) {
	base := Defaults()

	cfg := base
	cfg.Egress.Mode = "nftables"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "egress mode") {
		t.Errorf("unknown egress mode should be refused, got %v", err)
	}

	cfg = base
	cfg.Git.Identity = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Errorf("git.identity=true must fail loudly, got %v", err)
	}

	cfg = base
	cfg.Git.History = "full"
	if err := cfg.Validate(); err == nil {
		t.Error("unknown git.history should be refused")
	}

	cfg = base
	cfg.Runtime.Engine = "docker"
	if err := cfg.Validate(); err == nil {
		t.Error("docker engine should be refused in v1")
	}

	if err := base.Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}
}

func TestValidateServices(t *testing.T) {
	base := Defaults()
	base.Egress.Mode = "proxy"

	// Services require proxy mode — the relay lives on the internal network.
	open := Defaults()
	open.Services = []ServiceConfig{{Name: "s", Backend: "h:22"}}
	if err := open.Validate(); err == nil || !strings.Contains(err.Error(), "proxy") {
		t.Errorf("services without proxy mode must fail, got %v", err)
	}

	dupe := base
	dupe.Services = []ServiceConfig{
		{Name: "a", Backend: "h:22"},
		{Name: "b", Backend: "h:23", Aliases: []string{"a"}},
	}
	if err := dupe.Validate(); err == nil {
		t.Error("a name reused as another service's alias must fail")
	}

	noBackend := base
	noBackend.Services = []ServiceConfig{{Name: "a"}}
	if err := noBackend.Validate(); err == nil {
		t.Error("a service without a backend must fail")
	}

	badProto := base
	badProto.Services = []ServiceConfig{{Name: "a", Backend: "h:22", Proto: "sctp"}}
	if err := badProto.Validate(); err == nil {
		t.Error("an unknown proto must fail")
	}

	ok := base
	ok.Services = []ServiceConfig{
		{Name: "a", Backend: "h:22", Aliases: []string{"a1"}},
		{Name: "b", Backend: "h:23", Proto: "udp"},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid services should pass: %v", err)
	}
}
