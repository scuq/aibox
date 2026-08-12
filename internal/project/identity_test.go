package project

// Transcribed from the `per-project config volumes` section of the bclaude
// test suite: same workspace -> same identity, spelled differently -> still
// the same, same basename in a different place -> a different one.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestIDStableAcrossSpellings(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	id1, err := ID(sub)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ID(filepath.Join(dir, "proj", "..", "proj"))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Errorf("two spellings of the same path produced different IDs: %q vs %q", id1, id2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id1) {
		t.Errorf("ID %q is not 12 hex chars", id1)
	}
}

func TestIDResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlink support")
	}
	id1, _ := ID(real)
	id2, _ := ID(link)
	if id1 != id2 {
		t.Errorf("symlinked path produced a different ID: %q vs %q", id1, id2)
	}
}

func TestSameBasenameDoesNotCollide(t *testing.T) {
	d1 := filepath.Join(t.TempDir(), "proj")
	d2 := filepath.Join(t.TempDir(), "proj")
	for _, d := range []string{d1, d2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	id1, _ := ID(d1)
	id2, _ := ID(d2)
	if id1 == id2 {
		t.Errorf("same basename in two places collided: %q", id1)
	}
}

func TestDerivedNames(t *testing.T) {
	name := ContainerName("devcontainer", "chalk", "a81f728bc9ef")
	if name != "aibox-dc-chalk-a81f728bc9ef" {
		t.Errorf("ContainerName = %q", name)
	}
	// The directory name is there so listings stay readable; hostile
	// characters must not survive into it.
	name = ContainerName("workspace", "my repo (v2)", "a81f728bc9ef")
	if name != "aibox-ws-my-repo-v2-a81f728bc9ef" {
		t.Errorf("ContainerName with slugging = %q", name)
	}
	if got := ConfigVolumeName("claude", "a81f728bc9ef"); got != "aibox-config-claude-a81f728bc9ef" {
		t.Errorf("ConfigVolumeName = %q", got)
	}
	if got := AuthVolumeName("codex"); got != "aibox-auth-codex" {
		t.Errorf("AuthVolumeName = %q", got)
	}
}
