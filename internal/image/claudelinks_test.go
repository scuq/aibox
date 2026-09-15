package image

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/scuq/aibox/assets"
)

// TestClaudeLinks runs the real claude-links.sh against temp dirs, the way
// the entrypoint and postStartCommand run it in the container.
func TestClaudeLinks(t *testing.T) {
	dir := t.TempDir()
	builtin := filepath.Join(dir, "builtin")
	host := filepath.Join(dir, "host")
	cfg := filepath.Join(dir, "config")
	mk := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(builtin, "skills/aibox/SKILL.md"))
	mk(filepath.Join(builtin, "skills/shadowed/SKILL.md"))
	mk(filepath.Join(builtin, "agents/aibox.md"))
	mk(filepath.Join(host, "skills/scraibe/SKILL.md"))
	mk(filepath.Join(host, "skills/shadowed/SKILL.md"))
	mk(filepath.Join(host, "agents/scraibe.md"))
	// A real file in the volume is the user's and wins over both.
	mk(filepath.Join(cfg, "agents/local.md"))
	// A stale link from a previous start must disappear.
	if err := os.Symlink("/nonexistent", filepath.Join(cfg, "agents/gone.md")); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(dir, "claude-links.sh")
	if err := os.WriteFile(script, assets.Read("claude-links.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func() {
		t.Helper()
		cmd := exec.Command("bash", script, cfg)
		cmd.Env = append(os.Environ(), "AIBOX_CLAUDE_BUILTIN="+builtin, "AIBOX_CLAUDE_HOST="+host)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("claude-links.sh failed: %v\n%s", err, out)
		}
	}
	run()
	run() // idempotent: a second start rebuilds the same links

	for link, want := range map[string]string{
		"skills/aibox":      filepath.Join(builtin, "skills/aibox"),
		"skills/scraibe":    filepath.Join(host, "skills/scraibe"),
		"skills/shadowed":   filepath.Join(host, "skills/shadowed"), // host wins
		"agents/aibox.md":   filepath.Join(builtin, "agents/aibox.md"),
		"agents/scraibe.md": filepath.Join(host, "agents/scraibe.md"),
	} {
		got, err := os.Readlink(filepath.Join(cfg, link))
		if err != nil {
			t.Errorf("%s: not a link: %v", link, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %s, want %s", link, got, want)
		}
	}
	if st, err := os.Lstat(filepath.Join(cfg, "agents/local.md")); err != nil || st.Mode()&os.ModeSymlink != 0 {
		t.Error("a real file in the config dir must be left alone")
	}
	if _, err := os.Lstat(filepath.Join(cfg, "agents/gone.md")); err == nil {
		t.Error("stale link from a previous start was not removed")
	}
}

// TestWriteContextCarriesClaudeAssets: the Containerfile COPYs claude/ and
// claude-links.sh, so the build context must hold them.
func TestWriteContextCarriesClaudeAssets(t *testing.T) {
	ctxDir := t.TempDir()
	if err := WriteContext(ctxDir, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"claude-links.sh",
		"claude/skills/aibox/SKILL.md",
		"claude/agents/aibox.md",
	} {
		if _, err := os.Stat(filepath.Join(ctxDir, want)); err != nil {
			t.Errorf("build context missing %s: %v", want, err)
		}
	}
}
