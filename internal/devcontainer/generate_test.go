package devcontainer

// Transcribed from the `devcontainer` section of the bclaude test suite, plus
// the aibox additions: the git policy mount, the ownership labels, and the
// per-assistant volume selection.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/assistant"
	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/git"
)

func opts(t *testing.T) Options {
	t.Helper()
	cfg := config.Defaults()
	ws := "/home/u/chalk"
	return Options{
		Config:      cfg,
		Workspace:   ws,
		ProjectID:   "a81f728bc9ef",
		ProjectName: "chalk",
		Assistants:  []assistant.Assistant{assistant.Claude{}},
		GitMounts: git.Plan(git.RepoInfo{
			IsRepo: true, DotGit: ws + "/.git", CommonDir: ws + "/.git",
		}, cfg.Git.History, ws),
	}
}

func gen(t *testing.T, o Options) string {
	t.Helper()
	out, err := Generate(o)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// stripComments removes //-comments so the output can be parsed as plain JSON.
var commentLine = regexp.MustCompile(`(?m)^\s*//.*$`)

func TestGeneratedFileIsValidJSON(t *testing.T) {
	out := commentLine.ReplaceAllString(gen(t, opts(t)), "")
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not valid JSON once comments are stripped: %v\n%s", err, out)
	}
}

func TestDevContainerFive(t *testing.T) {
	// All five, together, always. Without them Dev Containers derives its own
	// vsc-<project>-<hash>-uid image purely to rewrite the container user's
	// uid — redundant against keep-id, fighting the volume ownership, and a
	// second cached image between the user and 'aibox image build'.
	out := gen(t, opts(t))
	for _, want := range []string{
		`"overrideCommand": true`,
		`"containerUser": "aibox"`,
		`"remoteUser": "aibox"`,
		`"updateRemoteUserUID": false`,
		`"--userns=keep-id:uid=1000,gid=1000"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("devcontainer.json missing %q", want)
		}
	}
}

func TestGeneratedContent(t *testing.T) {
	out := gen(t, opts(t))
	for _, want := range []string{
		`"image": "localhost/aibox:latest"`,
		`"workspaceFolder": "/work"`,
		"source=${localWorkspaceFolder},target=/work,type=bind",
		// the volumes, per assistant, plus the shared cache
		"source=aibox-config-claude-a81f728bc9ef,target=/home/aibox/.claude,type=volume",
		"source=aibox-auth-claude,target=/home/aibox/.claude-auth,type=volume",
		"source=aibox-cache-shared,target=/home/aibox/.cache,type=volume",
		// the git policy: .git read-only over the writable workspace
		"source=/home/u/chalk/.git,target=/work/.git,type=bind,readonly",
		// hardening
		`"--security-opt=no-new-privileges"`,
		`"--cap-drop=ALL"`,
		`"--pids-limit=2048"`,
		// ownership labels: what lets aibox manage what VS Code created
		`"--label=io.aibox.managed=true"`,
		`"--label=io.aibox.mode=devcontainer"`,
		`"--label=io.aibox.project.id=a81f728bc9ef"`,
		// the assistant extension and the toolchain wiring
		`"anthropic.claude-code"`,
		`"golang.go"`,
		`"go.goroot": "/usr/local/go"`,
		`"go.toolsManagement.autoUpdate": false`,
		`"python.defaultInterpreterPath": "/usr/bin/python3"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("devcontainer.json missing %q\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"HTTPS_PROXY",        // no proxy plumbing without proxy mode
		"initializeCommand",  // no rebuild hook without --fresh
		`"--rm"`,             // not disposable by default
		"aibox-config-codex", // codex not selected
		".gitconfig",         // never the host git identity
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("devcontainer.json should not contain %q", unwanted)
		}
	}
}

func TestEgressProxyMode(t *testing.T) {
	o := opts(t)
	o.Config.Egress.Mode = "proxy"
	out := gen(t, o)
	for _, want := range []string{
		`"--network=aibox-internal"`,
		`"HTTPS_PROXY": "http://10.199.0.2:3128"`,
		`"https_proxy": "http://10.199.0.2:3128"`,
		`"NO_PROXY": "localhost,127.0.0.1,::1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("proxy devcontainer missing %q", want)
		}
	}
}

func TestFresh(t *testing.T) {
	o := opts(t)
	o.Fresh = true
	o.SelfPath = "/home/u/.local/bin/aibox"
	out := gen(t, o)
	if !strings.Contains(out, `"initializeCommand": ["/home/u/.local/bin/aibox", "image", "build", "--image", "localhost/aibox:latest"]`) {
		t.Errorf("--fresh must rebuild the image the file names:\n%s", out)
	}
	if !strings.Contains(out, `"--rm"`) {
		t.Error("--fresh must discard the container on stop")
	}
	if !strings.Contains(out, `"--cap-drop=ALL"`) {
		t.Error("--fresh must keep the hardening")
	}
	// still valid JSON with the extra members
	stripped := commentLine.ReplaceAllString(out, "")
	var v map[string]any
	if err := json.Unmarshal([]byte(stripped), &v); err != nil {
		t.Fatalf("--fresh output is not valid JSON: %v", err)
	}
}

func TestCodexSelection(t *testing.T) {
	o := opts(t)
	o.Assistants = []assistant.Assistant{assistant.Codex{}}
	out := gen(t, o)
	if !strings.Contains(out, "source=aibox-config-codex-a81f728bc9ef,target=/home/aibox/.codex,type=volume") {
		t.Error("codex config volume missing")
	}
	if strings.Contains(out, "aibox-config-claude") || strings.Contains(out, "anthropic.claude-code") {
		t.Error("claude storage/extension present in a codex-only devcontainer")
	}
	if !strings.Contains(out, `"--label=io.aibox.assistant=codex"`) {
		t.Error("assistant label wrong")
	}
}

func TestSELinuxRelabel(t *testing.T) {
	o := opts(t)
	o.SELinux = true
	out := gen(t, o)
	if !strings.Contains(out, "target=/work,type=bind,z") {
		t.Error("workspace mount not relabelled under SELinux")
	}
	// The .git mount is a bind mount too; the relabel appends after readonly.
	if !strings.Contains(out, "target=/work/.git,type=bind,readonly,z") {
		t.Errorf("git mount not relabelled:\n%s", out)
	}
}
