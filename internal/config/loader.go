package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// UserConfigPath is the per-user config file.
func UserConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "aibox", "config.yaml")
}

// ProjectConfigName is the per-project config file, looked up in the workspace.
const ProjectConfigName = ".aibox.yaml"

// Load resolves the configuration for a workspace:
//
//	compiled defaults → ~/.config/aibox/config.yaml → <workspace>/.aibox.yaml → env
//
// Flags are the final layer and are applied by the individual commands.
// Missing files are fine; unparsable or unknown-key files are not — a typo'd
// key silently ignored is a policy the user believes is active and is not.
func Load(workspace string) (Config, error) {
	cfg := Defaults()

	if p := UserConfigPath(); p != "" {
		if err := mergeFile(&cfg, p); err != nil {
			return cfg, err
		}
	}
	if workspace != "" {
		if err := mergeFile(&cfg, filepath.Join(workspace, ProjectConfigName)); err != nil {
			return cfg, err
		}
	}
	applyEnv(&cfg)
	if cfg.Version == 0 {
		cfg.Version = Version
	}
	return cfg, nil
}

func mergeFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	// Decoding over the existing struct gives merge semantics: only keys
	// present in the document are set, everything else keeps its prior value.
	// KnownFields makes an unknown key an error rather than a silently
	// inactive setting.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && err != io.EOF {
		return fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return nil
}

// applyEnv reads only AIBOX_-prefixed variables. IMAGE, WORKSPACE and VOLUME
// are ordinary words that projects and CI already export for their own
// purposes, and these settings decide what gets mounted and what cleanup
// deletes: inheriting a stray `export IMAGE=` would aim removal at somebody
// else's image. Ported from bclaude, where this was a recorded incident class.
func applyEnv(cfg *Config) {
	str := func(name string, dst *string) {
		if v, ok := os.LookupEnv(name); ok && v != "" {
			*dst = v
		}
	}
	boolish := func(name string, dst *bool) {
		if v, ok := os.LookupEnv(name); ok {
			*dst = v == "1" || v == "true"
		}
	}
	str("AIBOX_WORKSPACE", &cfg.Runtime.Workspace)
	str("AIBOX_IMAGE", &cfg.Image.Reference)
	str("AIBOX_CA_CERTS", &cfg.Image.CACertificates)
	str("AIBOX_EGRESS", &cfg.Egress.Mode)
	str("AIBOX_EGRESS_SUBNET", &cfg.Egress.Subnet)
	str("AIBOX_PROXY_IMAGE", &cfg.Egress.ProxyImage)
	str("AIBOX_PROXY_LOG_DRIVER", &cfg.Egress.ProxyLogDriver)
	str("AIBOX_MEMORY", &cfg.Runtime.Memory)
	str("AIBOX_CPUS", &cfg.Runtime.CPUs)
	boolish("AIBOX_ALLOW_ROOT", &cfg.Runtime.AllowRoot)
	boolish("AIBOX_ALLOW_PKG", &cfg.Security.AllowPackageInstall)
	boolish(EnvAllowPodman, &cfg.Security.AllowNestedPodman)
	boolish("AIBOX_AUTOBUILD", &cfg.Image.Autobuild)
	if v, ok := os.LookupEnv("AIBOX_RO"); ok && (v == "1" || v == "true") {
		cfg.Runtime.WorkspaceMode = "read-only"
	}
}

// Render returns the resolved configuration as YAML, for `aibox config show`.
func Render(cfg Config) (string, error) {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// EnvAllowPodman overrides the 'aibox podman enable' marker in either
// direction: =1 turns nested podman on, =0 turns it off.
const EnvAllowPodman = "AIBOX_ALLOW_PODMAN"

// NestedPodmanPath is the host-side marker for 'aibox podman enable'. It lives
// beside the user config and never in the workspace, so nothing inside the
// container can create it.
func NestedPodmanPath(projectID string) string {
	cfgPath := UserConfigPath()
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "projects", projectID, "nested-podman")
}

// NestedPodmanEnabled reports whether the project's marker exists.
func NestedPodmanEnabled(projectID string) bool {
	path := NestedPodmanPath(projectID)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// SetNestedPodman creates or removes the project's marker.
func SetNestedPodman(projectID string, on bool) error {
	path := NestedPodmanPath(projectID)
	if path == "" {
		return errors.New("cannot locate the user config directory")
	}
	if !on {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := "Created by 'aibox podman enable' on " + time.Now().UTC().Format(time.RFC3339) + ".\n"
	return os.WriteFile(path, []byte(content), 0o600)
}

// ApplyNestedPodman folds the marker into a loaded config. Load cannot do this
// itself: the project ID is derived from the workspace after Load returns. It
// only ever turns the setting on, so a --allow-podman flag already applied
// survives, and it stands aside when EnvAllowPodman decided the value.
func ApplyNestedPodman(cfg *Config, projectID string) {
	if _, set := os.LookupEnv(EnvAllowPodman); set {
		return
	}
	if NestedPodmanEnabled(projectID) {
		cfg.Security.AllowNestedPodman = true
	}
}
