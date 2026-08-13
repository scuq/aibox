package notes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/assets"
	"github.com/scuq/aibox/internal/config"
)

func TestPolicyLayer(t *testing.T) {
	cfg := config.Defaults()
	open := PolicyLayer(cfg)
	// Open mode must say the allowlist guidance does NOT apply, or the agent
	// treats an ordinary failure as a blocked domain.
	if !strings.Contains(open, "OPEN") || !strings.Contains(open, "does\nnot apply") {
		t.Errorf("open mode not stated clearly:\n%s", open)
	}
	cfg.Egress.Mode = "proxy"
	proxied := PolicyLayer(cfg)
	if !strings.Contains(proxied, "allowlisted proxy") {
		t.Errorf("proxy mode not stated:\n%s", proxied)
	}
	cfg.Git.History = "none"
	if got := PolicyLayer(cfg); !strings.Contains(got, "history: none") {
		t.Errorf("history none not surfaced:\n%s", got)
	}
	// Services are listed in the session status.
	cfg.Services = []config.ServiceConfig{{Name: "switch1", Backend: "h:22"}}
	if got := PolicyLayer(cfg); !strings.Contains(got, "switch1") {
		t.Errorf("configured services not surfaced:\n%s", got)
	}
}

func TestProjectLayerAppendsRepoNotes(t *testing.T) {
	cfg := config.Defaults()
	ws := t.TempDir()
	if got := ProjectLayer(cfg, ws); got != "" {
		t.Errorf("no project notes should render empty, got %q", got)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".aibox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".aibox", "ainotes.md"), []byte("## Conventions\nuse tabs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ProjectLayer(cfg, ws); !strings.Contains(got, "use tabs") {
		t.Errorf("project layer not read: %q", got)
	}
}

func TestBudget(t *testing.T) {
	if err := CheckBudget(strings.Repeat("x", 2048), 2048); err != nil {
		t.Errorf("at budget is fine: %v", err)
	}
	if err := CheckBudget(strings.Repeat("x", 2049), 2048); err == nil {
		t.Error("over budget must fail")
	}
}

func TestImageNotesGeneratorRuns(t *testing.T) {
	// Run the real generator the way the image build does, pointed at a temp
	// dir. Absent tools probe as "unknown" (7 chars), which is as long as or
	// longer than real version strings, so a pass here means the in-image
	// build fits the budget too.
	dir := t.TempDir()
	share := filepath.Join(dir, "share")
	gen := filepath.Join(dir, "generate-image-notes.sh")
	if err := os.WriteFile(gen, assets.Read("ainotes/generate-image-notes.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", gen)
	cmd.Env = append(os.Environ(), "AIBOX_SHARE="+share)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed (over budget?): %v\n%s", err, out)
	}

	manifest, err := os.ReadFile(filepath.Join(share, "image-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v\n%s", err, manifest)
	}
	tools := m["tools"].(map[string]any)
	for _, want := range []string{"oc", "ansible-core", "ripgrep", "openbao", "openssl"} {
		if _, ok := tools[want]; !ok {
			t.Errorf("manifest tools missing %q", want)
		}
	}

	imageNotes, err := os.ReadFile(filepath.Join(share, "ainotes-image.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ansible", "oc ", "lua ", "pwsh ", "bao ", "openssl ", "yq", "expect", "nc "} {
		if !strings.Contains(string(imageNotes), want) {
			t.Errorf("image notes missing %q", want)
		}
	}
}

func TestImageNotesGeneratorGatesTheBudget(t *testing.T) {
	// The generator embedded in the image build must contain the size gate —
	// an over-budget notes file fails the build, not the session.
	gen := string(assets.Read("ainotes/generate-image-notes.sh"))
	if !strings.Contains(gen, "budget 4096") || !strings.Contains(gen, "exit 1") {
		t.Error("the image-notes generator lost its budget gate")
	}
	// And the git policy is the opening section, so the assistant plans
	// around it rather than discovering it by failure.
	idx := strings.Index(gen, "Repository writes — read this first")
	if idx == -1 {
		t.Fatal("the git policy section is missing from the image notes")
	}
	if toolIdx := strings.Index(gen, "Tools (prefer these"); toolIdx != -1 && toolIdx < idx {
		t.Error("the git policy must lead the notes")
	}
	// The escape-hatch guidance the agent most needs: verify-before-giving-up,
	// the host hand-off for pushes and containers, and the allow command.
	for _, want := range []string{
		"curl -sSfI", "aibox egress allow", "No container runtime here",
		"git -C <repo>", "services.json",
		// changelog + semver-tagged releases
		"CHANGELOG.md", "## Unreleased", "semver", "git -C <repo> tag -a vX.Y.Z",
		// the host-shared scratch mount
		"/ephemeral", "aibox ephemeral",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("image notes missing the %q guidance", want)
		}
	}
}

func TestClaudeMDSnippetDegradesOutsideAibox(t *testing.T) {
	if !strings.Contains(ClaudeMDSnippet, "ignore this section") {
		t.Error("the CLAUDE.md snippet must be a no-op outside the container")
	}
	if !strings.Contains(ClaudeMDSnippet, "/run/aibox/ainotes.md") {
		t.Error("the snippet must point at the canonical notes path")
	}
}
