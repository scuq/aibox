// Package git implements the host side of the git policy: resolving where the
// repository's real gitdir lives and turning that into the read-only mounts
// that make "no git write path inside the container" a kernel fact.
package git

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/scuq/aibox/internal/container"
)

// RepoInfo is what the host learned about the workspace's repository.
type RepoInfo struct {
	// IsRepo is false when the workspace is not inside a git repository (or
	// git itself is unavailable and no .git is present).
	IsRepo bool
	// DotGit is the workspace's own .git path, when one exists directly in
	// the workspace root — a directory for a normal repo, a gitfile for
	// submodules and linked worktrees.
	DotGit string
	// DotGitIsFile marks the gitfile case.
	DotGitIsFile bool
	// CommonDir is the resolved real gitdir (git rev-parse --git-common-dir),
	// absolute. For a normal repo this is <workspace>/.git; for submodules
	// and linked worktrees it lies elsewhere on the host.
	CommonDir string
}

// Resolve inspects the workspace on the host. `.git` may be a file, not a
// directory: submodules and linked worktrees use a gitfile pointing
// elsewhere. Bind-mounting that file read-only would appear to work while the
// real gitdir sits outside the workspace, unmounted, and git fails
// confusingly — so the common dir is resolved here, on the host, before any
// mount is planned.
func Resolve(workspace string) RepoInfo {
	info := RepoInfo{}

	dotGit := filepath.Join(workspace, ".git")
	if st, err := os.Lstat(dotGit); err == nil {
		info.DotGit = dotGit
		info.DotGitIsFile = !st.IsDir()
	}

	if gitPath, err := exec.LookPath("git"); err == nil {
		out, err := exec.Command(gitPath, "-C", workspace, "rev-parse", "--git-common-dir").Output()
		if err == nil {
			common := strings.TrimSpace(string(out))
			if !filepath.IsAbs(common) {
				common = filepath.Join(workspace, common)
			}
			info.CommonDir = filepath.Clean(common)
			info.IsRepo = true
			return info
		}
	}

	// No usable git on the host: fall back to what stat can tell us. A plain
	// .git directory is still mountable; a gitfile without git to resolve it
	// is not, and claiming otherwise would produce the confusing half-mounted
	// state this package exists to prevent.
	if info.DotGit != "" && !info.DotGitIsFile {
		info.IsRepo = true
		info.CommonDir = dotGit
	}
	return info
}

// RepoRoot returns the top-level directory of the workspace's git repository
// (`git rev-parse --show-toplevel`), canonicalised. Empty and a non-nil error
// when the workspace is not inside a repo or git is unavailable — the caller
// decides whether that is fatal.
func RepoRoot(workspace string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is not installed")
	}
	out, err := exec.Command(gitPath, "-C", workspace, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", workspace)
	}
	root := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return root, nil
}

// Remotes returns the workspace's git remotes as name → URL, read-only
// (`git remote -v` never writes). Empty when the workspace is not a repo or
// git is unavailable — the caller treats "no remotes" and "cannot tell" the
// same way. Used by the relay's git-remote conflict check (§8.9).
func Remotes(workspace string) map[string]string {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil
	}
	out, err := exec.Command(gitPath, "-C", workspace, "remote", "-v").Output()
	if err != nil {
		return nil
	}
	remotes := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// "origin\tgit@host:repo.git (fetch)" — first URL per remote is enough.
		if _, ok := remotes[fields[0]]; !ok {
			remotes[fields[0]] = fields[1]
		}
	}
	return remotes
}

// Mounts is the git policy rendered as mount decisions.
type Mounts struct {
	Mounts []container.Mount
	Tmpfs  []container.TmpfsMount
	// Warnings are printed once by the caller; none of them stop the run —
	// history simply being unavailable is a degraded but honest state.
	Warnings []string
}

// Plan turns RepoInfo plus the git.history setting into mounts.
//
// history "read-only": the workspace is mounted read-write elsewhere so the
// agent can edit files; .git is mounted read-only *over* it. `git add` cannot
// write the index, `commit` cannot write objects or refs, `push` cannot
// update remote-tracking refs. This is EROFS, not advice.
//
// history "none": the .git inside the workspace is masked instead, for review
// sessions where the agent should not read branch names or commit messages at
// all. Masking matters: without it the read-write workspace mount would
// expose .git writable, which is strictly worse than the default.
func Plan(info RepoInfo, history string, workspace string) Mounts {
	var m Mounts

	switch history {
	case "none":
		if info.DotGit == "" {
			return m
		}
		if info.DotGitIsFile {
			// A gitfile is masked by bind-mounting /dev/null over it; a tmpfs
			// only works on directories.
			m.Mounts = append(m.Mounts, container.Mount{
				Type: container.MountBind, Source: "/dev/null", Dest: "/work/.git",
				Options: []string{"ro"},
			})
		} else {
			m.Tmpfs = append(m.Tmpfs, container.TmpfsMount{
				Dest: "/work/.git", Options: []string{"ro"},
			})
		}
		return m

	case "read-only":
		if !info.IsRepo {
			if info.DotGit != "" && info.DotGitIsFile {
				m.Warnings = append(m.Warnings, ".git is a gitfile but git is not available on the host to resolve it — history will be unavailable in the container")
				// Mask the dangling pointer rather than expose it writable.
				m.Mounts = append(m.Mounts, container.Mount{
					Type: container.MountBind, Source: "/dev/null", Dest: "/work/.git",
					Options: []string{"ro"},
				})
				return m
			}
			m.Warnings = append(m.Warnings, "workspace is not a git repository — git history will be unavailable in the container")
			return m
		}

		if info.DotGit == "" {
			// The workspace is inside a repo but is not its root: there is no
			// .git here to mount, and git inside the container walks up to
			// nothing. Detect, warn once, continue.
			m.Warnings = append(m.Warnings, fmt.Sprintf("workspace is not the repository root (repository at %s) — git history will be unavailable in the container", filepath.Dir(info.CommonDir)))
			return m
		}

		// The workspace's .git — directory or gitfile — mounted read-only
		// over the read-write workspace mount.
		m.Mounts = append(m.Mounts, container.Mount{
			Type: container.MountBind, Source: info.DotGit, Dest: "/work/.git",
			Options: []string{"ro"},
		})

		// Gitfile case: the real gitdir lies outside the workspace. Mount it
		// read-only at its own host path, so the pointer inside the gitfile
		// resolves to something — and to something unwritable.
		if info.DotGitIsFile && info.CommonDir != "" && !strings.HasPrefix(info.CommonDir, workspace+string(filepath.Separator)) {
			m.Mounts = append(m.Mounts, container.Mount{
				Type: container.MountBind, Source: info.CommonDir, Dest: info.CommonDir,
				Options: []string{"ro"},
			})
		}
		return m
	}
	return m
}
