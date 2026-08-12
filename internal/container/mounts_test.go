package container

// Transcribed from the `SELinux relabelling (--dry-run)` section of the
// bclaude test suite. The bug these guard against: a relabel option
// concatenated onto the destination path instead of into the option list —
// podman reads "/work,z" as the destination, the workspace mounts somewhere
// nobody looks, and the empty /work from the image is what the agent sees.
// Silent and severe; for the config volume it means the login not persisting.

import (
	"strings"
	"testing"
)

func TestMountRender(t *testing.T) {
	tests := []struct {
		name    string
		mount   Mount
		selinux bool
		want    string
	}{
		{
			"bare bind mount",
			Mount{Type: MountBind, Source: "/home/u/proj", Dest: "/work"},
			false, "/home/u/proj:/work",
		},
		{
			"read-only option",
			Mount{Type: MountBind, Source: "/home/u/proj", Dest: "/work", Options: []string{"ro"}},
			false, "/home/u/proj:/work:ro",
		},
		{
			"workspace gets :z when enforcing",
			Mount{Type: MountBind, Source: "/home/u/proj", Dest: "/work"},
			true, "/home/u/proj:/work:z",
		},
		{
			"read-only workspace appends the relabel with a comma",
			Mount{Type: MountBind, Source: "/home/u/proj", Dest: "/work", Options: []string{"ro"}},
			true, "/home/u/proj:/work:ro,z",
		},
		{
			"volume gets :z when enforcing",
			Mount{Type: MountVolume, Source: "aibox-auth-claude", Dest: "/home/aibox/.claude-auth"},
			true, "aibox-auth-claude:/home/aibox/.claude-auth:z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.mount.Render(tt.selinux)
			if got != tt.want {
				t.Errorf("Render(%v) = %q, want %q", tt.selinux, got, tt.want)
			}
			// The destination must never absorb an option.
			if strings.Contains(got, tt.mount.Dest+",") {
				t.Errorf("option concatenated onto the destination path: %q", got)
			}
		})
	}
}

func TestTmpfsRender(t *testing.T) {
	tm := TmpfsMount{Dest: "/tmp", Options: []string{"rw", "nosuid", "nodev", "exec", "size=512m"}}
	got := tm.Args()
	want := []string{"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=512m"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("TmpfsMount.Args() = %v, want %v", got, want)
	}
}
