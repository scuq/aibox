package image

// Transcribed from the `build arguments (--dry-run)` section of the bclaude
// test suite, plus the recipe-hash semantics.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/assets"
	"github.com/scuq/aibox/internal/config"
)

func TestBuildArgs(t *testing.T) {
	cfg := config.Defaults()
	cfg.Image.AssistantVersions = map[string]string{"claude": "2.1.218"}
	argv := strings.Join(BuildArgs(cfg, "/ctx", false, "0.1.0"), " ")

	for _, want := range []string{
		"--build-arg CLAUDE_VERSION=2.1.218",
		"--build-arg CODEX_VERSION=latest",
		"--label io.aibox.recipe=",
		"--label io.aibox.managed=true",
		"--tag localhost/aibox:latest",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("build argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "--no-cache") {
		t.Error("--no-cache rendered without being asked for")
	}

	argv = strings.Join(BuildArgs(cfg, "/ctx", true, "0.1.0"), " ")
	if !strings.Contains(argv, "--no-cache") {
		t.Error("--no-cache not forwarded")
	}
}

func TestRecipeHashTracksVersions(t *testing.T) {
	a := config.Defaults()
	b := config.Defaults()
	b.Image.AssistantVersions = map[string]string{"claude": "2.1.218"}
	if RecipeHash(a) == RecipeHash(b) {
		t.Error("pinning a different assistant version must change the recipe hash")
	}
	if len(RecipeHash(a)) != 16 {
		t.Errorf("recipe hash should be 16 hex chars, got %q", RecipeHash(a))
	}
	if RecipeHash(a) != RecipeHash(config.Defaults()) {
		t.Error("the recipe hash must be deterministic")
	}
}

func TestContainerfileKeepsItsPins(t *testing.T) {
	// The Containerfile's downloaded tools (Go, oc, PowerShell) must stay
	// pinned with per-arch checksums that fail closed. Losing one of these is
	// exactly the "someone simplifies a flag back out" failure the plan
	// warns about.
	cf := string(assets.Read("Containerfile"))
	for _, want := range []string{
		"GO_SHA256_AMD64", "GO_SHA256_ARM64",
		"OC_SHA256_AMD64", "OC_SHA256_ARM64",
		"PWSH_SHA256_AMD64", "PWSH_SHA256_ARM64",
		"BAO_SHA256_AMD64", "BAO_SHA256_ARM64",
		"YQ_SHA256_AMD64", "YQ_SHA256_ARM64",
		// openssl is explicit, not inherited via ca-certificates
		"openssl",
		// the nc everyone's examples assume, not the virtual package
		"netcat-openbsd",
		"expect",
		"bzip2",
		// custom CA support: the unconditional COPY and the store rebuild,
		// plus the two variables that make the CA visible to node (claude,
		// codex) and python's requests
		"COPY ca-certificates/ /usr/local/share/ca-certificates/aibox/",
		"update-ca-certificates",
		"NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt",
		"REQUESTS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt",
		// the ansible venv is isolated, and galaxy artifacts go to the cache
		"/opt/ansible",
		"ANSIBLE_HOME=/home/aibox/.cache/ansible",
		// lua's versioned binary gets its expected name
		"lua5.4",
		// PowerShell's one system dependency
		"libicu72",
	} {
		if !strings.Contains(cf, want) {
			t.Errorf("Containerfile missing %q", want)
		}
	}
	if got := strings.Count(cf, "sha256sum -c -"); got < 5 {
		t.Errorf("expected at least 5 checksum gates (go, yq, oc, pwsh, bao), found %d", got)
	}
	if strings.Count(cf, "exit 1") < 5 {
		t.Error("unpinned architectures must stop the build for every downloaded tool")
	}
}

func TestWriteContextCACertificates(t *testing.T) {
	// Without a configured path the context still carries an (empty)
	// ca-certificates/ directory — the Containerfile COPYs it unconditionally.
	ctxDir := t.TempDir()
	if err := WriteContext(ctxDir, ""); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(ctxDir, "ca-certificates")); err != nil || !st.IsDir() {
		t.Fatal("ca-certificates/ must always exist in the build context")
	}

	// A directory of certs: both extensions land in the context, normalised
	// to .crt because update-ca-certificates ignores everything else.
	certSrc := t.TempDir()
	for name, content := range map[string]string{
		"corp-root.crt": "AAA", "proxy-ca.pem": "BBB", "notes.txt": "ignored",
	} {
		if err := os.WriteFile(filepath.Join(certSrc, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctxDir = t.TempDir()
	if err := WriteContext(ctxDir, certSrc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"corp-root.crt", "proxy-ca.crt"} {
		if _, err := os.Stat(filepath.Join(ctxDir, "ca-certificates", want)); err != nil {
			t.Errorf("context missing %s: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(ctxDir, "ca-certificates", "notes.txt")); err == nil {
		t.Error("non-certificate files must not enter the trust store")
	}

	// Configured but missing is a loud error, not an image quietly built
	// without the trust the user asked for.
	if err := WriteContext(t.TempDir(), filepath.Join(certSrc, "no-such-dir")); err == nil {
		t.Error("a missing caCertificates path must fail the build")
	}
}

func TestRecipeHashTracksCACertificates(t *testing.T) {
	certDir := t.TempDir()
	cert := filepath.Join(certDir, "corp.crt")
	if err := os.WriteFile(cert, []byte("CERT-V1"), 0o644); err != nil {
		t.Fatal(err)
	}
	plain := config.Defaults()
	withCA := config.Defaults()
	withCA.Image.CACertificates = certDir
	if RecipeHash(plain) == RecipeHash(withCA) {
		t.Error("adding CA certificates must change the recipe hash")
	}
	before := RecipeHash(withCA)
	if err := os.WriteFile(cert, []byte("CERT-V2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if RecipeHash(withCA) == before {
		t.Error("swapping a certificate must call the old image stale")
	}
}

func TestGoToolchainPinForwarded(t *testing.T) {
	cfg := config.Defaults()
	cfg.Image.Toolchains = map[string]string{"go": "1.24.0"}
	argv := strings.Join(BuildArgs(cfg, "/ctx", false, "0.1.0"), " ")
	if !strings.Contains(argv, "--build-arg GO_VERSION=1.24.0") {
		t.Errorf("toolchain pin not forwarded:\n%s", argv)
	}
}
