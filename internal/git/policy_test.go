package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/container"
)

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestResolveNormalRepo(t *testing.T) {
	requireGit(t)
	ws := t.TempDir()
	mustGit(t, ws, "init", "-q")

	info := Resolve(ws)
	if !info.IsRepo {
		t.Fatal("a freshly initialised repo should be detected")
	}
	if info.DotGitIsFile {
		t.Error(".git of a normal repo is a directory")
	}
	// EvalSymlinks because macOS/podman hosts alias temp dirs.
	want, _ := filepath.EvalSymlinks(filepath.Join(ws, ".git"))
	got, _ := filepath.EvalSymlinks(info.CommonDir)
	if got != want {
		t.Errorf("CommonDir = %q, want %q", info.CommonDir, want)
	}
}

func TestResolveLinkedWorktree(t *testing.T) {
	requireGit(t)
	main := t.TempDir()
	mustGit(t, main, "init", "-q")
	if err := os.WriteFile(filepath.Join(main, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, main, "add", "f")
	mustGit(t, main, "commit", "-q", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, main, "worktree", "add", "-q", wt)

	info := Resolve(wt)
	if !info.IsRepo {
		t.Fatal("a linked worktree is a repo")
	}
	if !info.DotGitIsFile {
		t.Error(".git of a linked worktree is a gitfile — mounting it as if it were the repo silently mounts a pointer")
	}
	if strings.HasPrefix(info.CommonDir, wt) {
		t.Errorf("the worktree's common dir lies outside the workspace, got %q", info.CommonDir)
	}
}

func TestResolveNonRepo(t *testing.T) {
	requireGit(t)
	info := Resolve(t.TempDir())
	if info.IsRepo {
		t.Error("an empty directory is not a repo")
	}
}

func TestPlanReadOnlyNormalRepo(t *testing.T) {
	ws := "/home/u/proj"
	info := RepoInfo{IsRepo: true, DotGit: ws + "/.git", CommonDir: ws + "/.git"}
	m := Plan(info, "read-only", ws)
	if len(m.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", m.Warnings)
	}
	if len(m.Mounts) != 1 {
		t.Fatalf("want exactly one mount, got %v", m.Mounts)
	}
	got := m.Mounts[0]
	if got.Source != ws+"/.git" || got.Dest != "/work/.git" {
		t.Errorf("mount = %+v", got)
	}
	// This is EROFS, not advice.
	if len(got.Options) != 1 || got.Options[0] != "ro" {
		t.Errorf(".git must be mounted read-only, got options %v", got.Options)
	}
}

func TestPlanGitfileMountsCommonDirToo(t *testing.T) {
	// Submodules and linked worktrees: bind-mounting just the gitfile appears
	// to work while the real gitdir sits outside the workspace, unmounted,
	// and git fails confusingly. Both must be mounted, both read-only.
	ws := "/home/u/proj"
	info := RepoInfo{IsRepo: true, DotGit: ws + "/.git", DotGitIsFile: true,
		CommonDir: "/home/u/main/.git/worktrees/proj"}
	m := Plan(info, "read-only", ws)
	if len(m.Mounts) != 2 {
		t.Fatalf("want gitfile + common dir mounts, got %v", m.Mounts)
	}
	common := m.Mounts[1]
	if common.Source != info.CommonDir || common.Dest != info.CommonDir {
		t.Errorf("common dir must be mounted at its own path, got %+v", common)
	}
	for _, mt := range m.Mounts {
		if len(mt.Options) != 1 || mt.Options[0] != "ro" {
			t.Errorf("git mount not read-only: %+v", mt)
		}
	}
}

func TestPlanWorkspaceNotRepoRoot(t *testing.T) {
	// Inside a repo but not its root: nothing to mount, warn once, continue —
	// history is simply unavailable.
	info := RepoInfo{IsRepo: true, CommonDir: "/home/u/repo/.git"}
	m := Plan(info, "read-only", "/home/u/repo/subdir")
	if len(m.Mounts) != 0 {
		t.Errorf("no mount expected, got %v", m.Mounts)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "not the repository root") {
		t.Errorf("want the not-root warning, got %v", m.Warnings)
	}
}

func TestPlanNonRepoWarns(t *testing.T) {
	m := Plan(RepoInfo{}, "read-only", "/w")
	if len(m.Warnings) != 1 {
		t.Errorf("want one warning, got %v", m.Warnings)
	}
	if len(m.Mounts) != 0 {
		t.Errorf("no mounts for a non-repo, got %v", m.Mounts)
	}
}

func TestPlanHistoryNoneMasksDotGit(t *testing.T) {
	// history: none must *mask* .git, not merely skip the mount — the
	// read-write workspace mount would otherwise expose it writable, which is
	// strictly worse than the default.
	ws := "/home/u/proj"
	info := RepoInfo{IsRepo: true, DotGit: ws + "/.git", CommonDir: ws + "/.git"}
	m := Plan(info, "none", ws)
	if len(m.Tmpfs) != 1 || m.Tmpfs[0].Dest != "/work/.git" {
		t.Errorf("history none should mask /work/.git with a tmpfs, got %+v", m.Tmpfs)
	}

	// Gitfile variant: a tmpfs only works on directories.
	info.DotGitIsFile = true
	m = Plan(info, "none", ws)
	if len(m.Mounts) != 1 || m.Mounts[0].Source != "/dev/null" {
		t.Errorf("a gitfile is masked with /dev/null, got %+v", m.Mounts)
	}
}

func TestPlanEndToEnd(t *testing.T) {
	requireGit(t)
	ws := t.TempDir()
	mustGit(t, ws, "init", "-q")
	m := Plan(Resolve(ws), "read-only", ws)
	if len(m.Mounts) != 1 || m.Mounts[0].Type != container.MountBind {
		t.Fatalf("unexpected plan: %+v", m)
	}
}
