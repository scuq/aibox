// Package project owns project identity: the stable ID derived from the
// workspace path, and the deterministic names derived from it.
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// IDLength is the number of hex characters in a project ID.
const IDLength = 12

// ID returns the project identity for a workspace:
// sha256(realpath(workspace))[0:12], e.g. "a81f728bc9ef". Symlinks are
// resolved first so the ID is stable no matter how the path was spelled —
// two spellings of the same directory must always yield the same ID, or the
// per-project volumes and containers silently split in two.
func ID(workspace string) (string, error) {
	canon, err := Canonical(workspace)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canon))
	return hex.EncodeToString(sum[:])[:IDLength], nil
}

// Canonical resolves a workspace path to its canonical absolute form: the
// form recorded in labels and hashed for the ID.
func Canonical(workspace string) (string, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace path %q: %w", workspace, err)
	}
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("cannot resolve workspace path %q: %w", workspace, err)
	}
	return canon, nil
}

var slugStrip = regexp.MustCompile(`[^A-Za-z0-9._]+`)

// Slug converts a project name (usually the workspace basename) into
// something safe in a container or volume name. The name is there so
// `podman ps` stays readable; the project ID is what makes names unique, so
// two repos with the same basename don't collide.
func Slug(name string) string {
	s := slugStrip.ReplaceAllString(name, "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = s[:24]
	}
	return s
}

// ContainerName derives the deterministic container name for a project in a
// given mode, e.g. "aibox-dc-chalk-a81f728bc9ef". Deterministic on purpose:
// discovery still goes through labels, but a human reading `podman ps` should
// be able to tell what a container is without inspecting it.
func ContainerName(mode, projectName, projectID string) string {
	prefix := map[string]string{
		"devcontainer": "aibox-dc",
		"workspace":    "aibox-ws",
		"standalone":   "aibox-run",
	}[mode]
	if prefix == "" {
		prefix = "aibox"
	}
	slug := Slug(projectName)
	if slug == "" {
		return fmt.Sprintf("%s-%s", prefix, projectID)
	}
	return fmt.Sprintf("%s-%s-%s", prefix, slug, projectID)
}

// ConfigVolumeName is the per-project, per-assistant config volume, e.g.
// "aibox-config-claude-a81f728bc9ef". Config is always per project and per
// assistant; only the login (auth volume) and toolchain caches are shared.
func ConfigVolumeName(assistant, projectID string) string {
	return fmt.Sprintf("aibox-config-%s-%s", assistant, projectID)
}

// AuthVolumeName is the per-assistant login volume, shared across projects so
// per-project config volumes don't mean logging in per project. Claude and
// Codex never share auth storage.
func AuthVolumeName(assistant string) string {
	return fmt.Sprintf("aibox-auth-%s", assistant)
}

// CacheVolumeName is the shared toolchain-cache volume (Go modules and build
// cache, pip/uv wheels, npm). One volume for everything: nothing in it is
// precious, and without it every ephemeral run starts by re-downloading the
// world.
const CacheVolumeName = "aibox-cache-shared"
