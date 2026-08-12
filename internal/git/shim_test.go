package git_test

// Table test for the git shim's verb parsing — the verb is not always $1
// (`git -C DIR commit`, `git --git-dir=X push`), and a shim that misses those
// forms is a policy with a hole in it. The shim runs here under bash with the
// real git swapped for a recorder.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/assets"
)

type shimResult struct {
	exitCode int
	output   string
	reached  bool // did the real git run?
}

func runShim(t *testing.T, env []string, args ...string) shimResult {
	t.Helper()
	dir := t.TempDir()

	shim := filepath.Join(dir, "git-shim.sh")
	if err := os.WriteFile(shim, assets.Read("git-shim.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "real-git-ran")
	realGit := filepath.Join(dir, "real-git")
	script := "#!/bin/sh\ntouch " + marker + "\necho REALGIT \"$@\"\n"
	if err := os.WriteFile(realGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", append([]string{shim}, args...)...)
	cmd.Env = append(os.Environ(), "AIBOX_REAL_GIT="+realGit)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	res := shimResult{output: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.exitCode = ee.ExitCode()
		} else {
			t.Fatalf("shim did not run: %v", err)
		}
	}
	_, statErr := os.Stat(marker)
	res.reached = statErr == nil
	return res
}

func TestShimVerbTable(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		denied bool
	}{
		// Read-only verbs pass through untouched.
		{"status", []string{"status"}, false},
		{"diff", []string{"diff", "--stat"}, false},
		{"log", []string{"log", "--oneline", "-5"}, false},
		{"show", []string{"show", "HEAD"}, false},
		{"blame", []string{"blame", "file.go"}, false},
		{"grep", []string{"grep", "pattern"}, false},
		{"rev-parse", []string{"rev-parse", "HEAD"}, false},
		{"ls-files", []string{"ls-files"}, false},

		// Write verbs stop loudly.
		{"add", []string{"add", "."}, true},
		{"commit", []string{"commit", "-m", "x"}, true},
		{"push", []string{"push"}, true},
		{"push --force", []string{"push", "--force"}, true},
		{"checkout", []string{"checkout", "main"}, true},
		{"switch", []string{"switch", "-c", "new"}, true},
		{"restore", []string{"restore", "--", "file"}, true},
		{"stash", []string{"stash"}, true},
		{"reset", []string{"reset", "--hard"}, true},
		{"rebase", []string{"rebase", "main"}, true},
		{"merge", []string{"merge", "other"}, true},
		{"cherry-pick", []string{"cherry-pick", "abc"}, true},
		{"tag", []string{"tag", "v1"}, true},
		{"fetch", []string{"fetch"}, true},
		{"pull", []string{"pull"}, true},
		{"clone", []string{"clone", "https://x/y"}, true},
		{"worktree", []string{"worktree", "add", "/x"}, true},
		{"gc", []string{"gc"}, true},
		{"update-ref", []string{"update-ref", "refs/heads/x", "abc"}, true},
		{"submodule", []string{"submodule", "update"}, true},

		// The verb is not always $1: global options and their arguments come
		// first. Table-tested because getting this wrong silently un-denies.
		{"-C dir commit", []string{"-C", "/some/dir", "commit", "-m", "x"}, true},
		{"-C dir status", []string{"-C", "/some/dir", "status"}, false},
		{"--git-dir= push", []string{"--git-dir=/g", "push"}, true},
		{"--git-dir sep push", []string{"--git-dir", "/g", "push", "origin"}, true},
		{"-c opt commit", []string{"-c", "user.name=x", "commit"}, true},
		{"--no-pager log", []string{"--no-pager", "log"}, false},
		{"--no-pager commit", []string{"--no-pager", "commit"}, true},
		{"--work-tree sep add", []string{"--work-tree", "/w", "add", "."}, true},

		// branch: listing is read-only, mutation flags are stopped.
		{"branch list", []string{"branch"}, false},
		{"branch -a", []string{"branch", "-a"}, false},
		{"branch -d", []string{"branch", "-d", "x"}, true},
		{"branch -D", []string{"branch", "-D", "x"}, true},
		{"branch -m", []string{"branch", "-m", "x", "y"}, true},
		{"branch -M", []string{"branch", "-M", "main"}, true},

		// remote: -v/show pass, mutations stop.
		{"remote -v", []string{"remote", "-v"}, false},
		{"remote show", []string{"remote", "show", "origin"}, false},
		{"remote add", []string{"remote", "add", "x", "https://y"}, true},
		{"remote set-url", []string{"remote", "set-url", "origin", "https://y"}, true},
		{"remote remove", []string{"remote", "remove", "x"}, true},

		// config: local reads pass (writes die on EROFS anyway); --global and
		// --system would write files the mount does not protect.
		{"config read", []string{"config", "user.name"}, false},
		{"config --global", []string{"config", "--global", "user.name", "x"}, true},
		{"config --system", []string{"config", "--system", "x", "y"}, true},

		// No verb at all: the real git prints its usage.
		{"bare git", nil, false},
		{"--version", []string{"--version"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runShim(t, nil, tt.args...)
			if tt.denied {
				if res.exitCode == 0 {
					t.Errorf("git %s should be denied, exited 0\n%s", strings.Join(tt.args, " "), res.output)
				}
				if res.reached {
					t.Errorf("git %s was denied but the real git still ran", strings.Join(tt.args, " "))
				}
				// Loud and explained, written for an agent.
				for _, want := range []string{"not available", "read-only", "HANDOFF.md"} {
					if !strings.Contains(res.output, want) {
						t.Errorf("denial message missing %q:\n%s", want, res.output)
					}
				}
			} else {
				if res.exitCode != 0 {
					t.Errorf("git %s should pass through, exited %d\n%s", strings.Join(tt.args, " "), res.exitCode, res.output)
				}
				if !res.reached {
					t.Errorf("git %s should reach the real git", strings.Join(tt.args, " "))
				}
			}
		})
	}
}

func TestShimOffPassesEverything(t *testing.T) {
	// git.shim: off is for debugging the shim itself; the read-only mount
	// still enforces.
	res := runShim(t, []string{"AIBOX_GIT_SHIM=off"}, "commit", "-m", "x")
	if res.exitCode != 0 || !res.reached {
		t.Errorf("with the shim off, commit should reach the real git (exit %d, reached %v)", res.exitCode, res.reached)
	}
}

func TestShimNeverFakesSuccess(t *testing.T) {
	// Never fake success to an agent: it will build a multi-step plan on the
	// lie. A denied verb must exit non-zero.
	res := runShim(t, nil, "commit", "-m", "x")
	if res.exitCode == 0 {
		t.Fatal("denied verb exited 0 — the shim faked success")
	}
}
