// Package image owns the container image: the embedded recipe, the build
// invocation, and the recipe-hash staleness check.
package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scuq/aibox/assets"
	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/runtime"
)

// DefaultLocalRef is the image a plain local build produces and a plain run
// uses when no reference is configured.
const DefaultLocalRef = "localhost/aibox:latest"

// Ref resolves the image reference for a configuration.
func Ref(cfg config.Config) string {
	if cfg.Image.Reference != "" {
		return cfg.Image.Reference
	}
	return DefaultLocalRef
}

// RecipeHash is sha256 over everything that determines the image contents —
// the embedded recipe files plus the assistant versions baked in — cut to 16
// hex chars. Stamped onto the image as io.aibox.recipe at build time and
// *checked* at run time (a label nobody reads is decoration).
//
// This solves a different problem from .aibox.lock and published digests:
// those solve reproducibility; the recipe hash answers "the binary on your
// PATH changed, your local image did not". Both exist on purpose.
func RecipeHash(cfg config.Config) string {
	h := sha256.New()
	for _, name := range []string{
		"Containerfile", "entrypoint.sh", "git-shim.sh", "gitconfig", "creds",
		"ainotes/generate-image-notes.sh", "ainotes/ainotes",
	} {
		h.Write(assets.Read(name))
	}
	fmt.Fprintf(h, "claude=%s\n", assistantVersion(cfg, "claude"))
	fmt.Fprintf(h, "codex=%s\n", assistantVersion(cfg, "codex"))
	// Custom CA certificates are image content too: swap the certificate and
	// the recipe check must call the old image stale.
	for _, f := range caCertFiles(cfg.Image.CACertificates) {
		if data, err := os.ReadFile(f); err == nil {
			fmt.Fprintf(h, "ca=%s\n", filepath.Base(f))
			h.Write(data)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// caCertFiles resolves the configured CA path (file or directory) to a sorted
// list of certificate files. Sorted, so the recipe hash is deterministic.
func caCertFiles(path string) []string {
	if path == "" {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if !st.IsDir() {
		return []string{path}
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".crt", ".pem":
			out = append(out, filepath.Join(path, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

func assistantVersion(cfg config.Config, name string) string {
	if v := cfg.Image.AssistantVersions[name]; v != "" {
		return v
	}
	return "latest"
}

// WriteContext writes the embedded recipe into dir, ready for podman build.
// caCertsPath optionally names a host file or directory of CA certificates to
// bake into the image's trust store; the ca-certificates/ context directory
// always exists (the Containerfile COPYs it unconditionally) and is empty
// when no certificates are configured.
func WriteContext(dir string, caCertsPath string) error {
	files := map[string]os.FileMode{
		"Containerfile":                   0o644,
		"entrypoint.sh":                   0o755,
		"git-shim.sh":                     0o755,
		"gitconfig":                       0o644,
		"creds":                           0o755,
		"ainotes/generate-image-notes.sh": 0o755,
		"ainotes/ainotes":                 0o755,
	}
	for name, mode := range files {
		dst := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("cannot write build context: %w", err)
		}
		if err := os.WriteFile(dst, assets.Read(name), mode); err != nil {
			return fmt.Errorf("cannot write build context: %w", err)
		}
	}

	certDir := filepath.Join(dir, "ca-certificates")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return fmt.Errorf("cannot write build context: %w", err)
	}
	if caCertsPath == "" {
		return nil
	}
	if _, err := os.Stat(caCertsPath); err != nil {
		// Configured but unreadable is a loud error, not an image quietly
		// built without the trust the user asked for.
		return fmt.Errorf("image.caCertificates: %w", err)
	}
	certs := caCertFiles(caCertsPath)
	if len(certs) == 0 {
		return fmt.Errorf("image.caCertificates: no *.crt or *.pem files at %s", caCertsPath)
	}
	for _, src := range certs {
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("image.caCertificates: %w", err)
		}
		// update-ca-certificates only picks up *.crt — normalise the name so
		// a .pem does not silently fail to enter the trust store.
		name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)) + ".crt"
		if err := os.WriteFile(filepath.Join(certDir, name), data, 0o644); err != nil {
			return fmt.Errorf("cannot write build context: %w", err)
		}
	}
	return nil
}

// BuildArgs returns the podman argv (starting at "build") for building the
// image from a prepared context directory.
func BuildArgs(cfg config.Config, ctxDir string, noCache bool, aiboxVersion string) []string {
	args := []string{
		"build",
		"--tag", Ref(cfg),
		"--build-arg", "CLAUDE_VERSION=" + assistantVersion(cfg, "claude"),
		"--build-arg", "CODEX_VERSION=" + assistantVersion(cfg, "codex"),
		"--label", container.LabelManaged + "=true",
		"--label", container.LabelSchema + "=" + container.SchemaVersion,
		"--label", container.LabelRecipe + "=" + RecipeHash(cfg),
		"--label", "io.aibox.version=" + aiboxVersion,
		"--file", filepath.Join(ctxDir, "Containerfile"),
	}
	if v := cfg.Image.Toolchains["go"]; v != "" {
		// The Go tarball checksums live in the Containerfile next to the
		// default version; overriding only the version would fail the
		// checksum on purpose (fails closed) unless the checksums are
		// overridden too. Surfaced here rather than hidden.
		args = append(args, "--build-arg", "GO_VERSION="+v)
	}
	if noCache {
		args = append(args, "--no-cache")
	}
	args = append(args, ctxDir)
	return args
}

// IsCurrent reports whether the image exists and its recipe label matches
// this binary's recipe.
func IsCurrent(ctx context.Context, rt runtime.Runtime, cfg config.Config) (exists, current bool, err error) {
	ref := Ref(cfg)
	exists, err = rt.ImageExists(ctx, ref)
	if err != nil || !exists {
		return exists, false, err
	}
	label, err := rt.ImageLabel(ctx, ref, container.LabelRecipe)
	if err != nil {
		return true, false, nil // unreadable label = stale
	}
	return true, label == RecipeHash(cfg), nil
}
