package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/output"
)

// complete sources the script in a real bash and returns the candidates for a
// command line. The last word is the one being completed.
func complete(t *testing.T, env []string, words ...string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	script := filepath.Join(t.TempDir(), "completion.bash")
	if err := os.WriteFile(script, []byte(bashCompletion), 0o644); err != nil {
		t.Fatal(err)
	}
	driver := `source "$1"; shift
COMP_WORDS=("$@"); COMP_CWORD=$(($# - 1))
_aibox
printf '%s\n' "${COMPREPLY[@]}"`
	cmd := exec.Command(bash, append([]string{"--noprofile", "--norc", "-c", driver, "bash", script}, words...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("completion failed: %v\n%s", err, out)
	}
	return strings.Fields(string(out))
}

func hasWord(words []string, want string) bool {
	for _, w := range words {
		if w == want {
			return true
		}
	}
	return false
}

// The script lists commands by hand, so tie it to the usage text.
func TestCompletionCoversEveryCommand(t *testing.T) {
	section := usage[strings.Index(usage, "COMMANDS\n"):strings.Index(usage, "RUN OPTIONS")]
	names := regexp.MustCompile(`(?m)^    ([a-z][a-z-]*)\b`).FindAllStringSubmatch(section, -1)
	if len(names) < 15 {
		t.Fatalf("parsed only %d commands from the usage text", len(names))
	}
	got := complete(t, nil, "aibox", "")
	for _, n := range names {
		if !hasWord(got, n[1]) {
			t.Errorf("command %q is in the usage text but not in the completion", n[1])
		}
	}
}

func TestCompletionCandidates(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		want  string
		not   string
	}{
		{"prefix", []string{"aibox", "st"}, "stop-all", "run"},
		{"subcommand", []string{"aibox", "secret", ""}, "revoke-all", "up"},
		{"run assistant", []string{"aibox", "run", ""}, "claude", "up"},
		{"assistant after a valued flag", []string{"aibox", "run", "--memory", "8g", ""}, "codex", ""},
		{"run flags", []string{"aibox", "run", "--eg"}, "--egress", "--ro"},
		{"flag value", []string{"aibox", "run", "--egress", ""}, "proxy", "claude"},
		{"flag value after equals", []string{"aibox", "run", "--egress", "=", ""}, "proxy", "claude"},
		{"assistant args stay untouched", []string{"aibox", "run", "claude", ""}, "", "--egress"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := complete(t, nil, c.words...)
			if c.want != "" && !hasWord(got, c.want) {
				t.Errorf("want %q in %v", c.want, got)
			}
			if c.not != "" && hasWord(got, c.not) {
				t.Errorf("did not want %q in %v", c.not, got)
			}
		})
	}
}

// A service name is agent-writable data. It must not run, and must not reach
// the prompt, when the user presses TAB on the host.
func TestCompletionDoesNotExpandAgentNames(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	stub := "#!/bin/sh\nprintf '  SERVICE PORT\\n  good-name 1\\n  $(touch " + marker + ") 2\\n  `touch " + marker + "` 3\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "aibox"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	got := complete(t, []string{"PATH=" + dir + ":" + os.Getenv("PATH")}, "aibox", "relay", "test", "")
	if len(got) != 1 || got[0] != "good-name" {
		t.Errorf("got %v, want only good-name", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a service name was executed during completion")
	}
}

func TestCompletionInstall(t *testing.T) {
	data := t.TempDir()
	t.Setenv("BASH_COMPLETION_USER_DIR", "")
	t.Setenv("XDG_DATA_HOME", data)
	p := &output.Printer{W: os.Stderr}
	if err := cmdCompletion(p, []string{"install"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(data, "bash-completion", "completions", "aibox"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != bashCompletion {
		t.Error("the installed file differs from the embedded script")
	}
}
