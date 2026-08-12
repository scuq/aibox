// Package container holds the container specification model: mounts, labels
// and the translation of a spec into a podman argv. Nothing in here talks to
// podman; it only builds data for internal/runtime to execute.
package container

import (
	"fmt"
	"strings"
)

// MountType distinguishes bind mounts from named volumes. tmpfs mounts are a
// separate type (TmpfsMount) because podman takes them via --tmpfs, not
// --volume, and they must never receive the SELinux relabel option.
type MountType string

const (
	MountBind   MountType = "bind"
	MountVolume MountType = "volume"
)

// Mount is one --volume argument. Every mount in aibox is built as one of
// these and rendered by Render — the only path from a mount to a podman
// argument anywhere in the codebase.
//
// The reason this exists (ported from bclaude's mount_opts): naively appending
// ",z" to an option-less mount yields "/work,z", which podman reads as part of
// the *destination path*. The mountpoint becomes "/work,z" and /work stays
// empty, silently — the workspace mounts somewhere nobody looks, and for the
// config volume it means the login not persisting. The option list must start
// with ":" when there are no prior options and use "," only between options.
type Mount struct {
	Type    MountType
	Source  string
	Dest    string
	Options []string // e.g. "ro"; never include "z" here — Render adds it
}

// Render returns the value of a --volume argument. selinuxRelabel appends the
// "z" relabel option, needed for mounts on SELinux-enforcing hosts
// (Fedora/RHEL); without it the container gets EACCES on everything mounted.
func (m Mount) Render(selinuxRelabel bool) string {
	opts := make([]string, 0, len(m.Options)+1)
	opts = append(opts, m.Options...)
	if selinuxRelabel {
		opts = append(opts, "z")
	}
	if len(opts) == 0 {
		return fmt.Sprintf("%s:%s", m.Source, m.Dest)
	}
	return fmt.Sprintf("%s:%s:%s", m.Source, m.Dest, strings.Join(opts, ","))
}

// Args returns the argv fragment for this mount.
func (m Mount) Args(selinuxRelabel bool) []string {
	return []string{"--volume", m.Render(selinuxRelabel)}
}

// TmpfsMount is one --tmpfs argument. Kept apart from Mount so a tmpfs can
// never be relabelled or given a source by accident.
type TmpfsMount struct {
	Dest    string
	Options []string // e.g. "rw", "nosuid", "size=512m"
}

// Args returns the argv fragment for this tmpfs.
func (t TmpfsMount) Args() []string {
	if len(t.Options) == 0 {
		return []string{"--tmpfs", t.Dest}
	}
	return []string{"--tmpfs", fmt.Sprintf("%s:%s", t.Dest, strings.Join(t.Options, ","))}
}
