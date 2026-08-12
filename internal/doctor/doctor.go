// Package doctor checks that the host can actually run aibox, with a real
// diagnosis (`podman info`) rather than the cheap kernel reads the hot path
// uses.
package doctor

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// Check is one diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

// Report is the set of checks plus the overall verdict.
type Report struct {
	Checks []Check `json:"checks"`
	OK     bool    `json:"ok"`
}

func (r *Report) add(status, name, detail string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail})
	if status == "fail" {
		r.OK = false
	}
}

// IsRootless is the cheap hot-path check: `podman info` costs seconds, so
// run-shaped commands read the uid instead. Doctor does the full diagnosis.
func IsRootless() bool { return os.Geteuid() != 0 }

// CgroupsV2 reports a unified cgroup hierarchy.
func CgroupsV2() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

// CgroupLimitsAvailable reports whether rootless memory/cpu limits can be
// enforced. Rootless podman without a delegated memory controller cannot
// apply --memory/--cpus; it errors out rather than ignoring them, so callers
// drop the caps instead of failing the run.
func CgroupLimitsAvailable() bool {
	if !CgroupsV2() {
		return false
	}
	if !IsRootless() {
		return true
	}
	path := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/cgroup.controllers", os.Getuid())
	data, err := os.ReadFile(path)
	if err != nil {
		return true // unknown layout: assume it works
	}
	for _, w := range strings.Fields(string(data)) {
		if w == "memory" {
			return true
		}
	}
	return false
}

// SELinuxEnforcing reports whether bind mounts need the :z relabel option.
func SELinuxEnforcing() bool {
	out, err := exec.Command("getenforce").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "Enforcing"
}

// SubIDPresent checks /etc/subuid for the current user (by name or uid). A
// missing entry is what makes rootless podman fail with cryptic newuidmap
// errors, so it gets its own check with the fix in the message.
func SubIDPresent() (bool, error) {
	f, err := os.Open("/etc/subuid")
	if err != nil {
		return true, nil // no file: podman may still work (e.g. single-uid maps)
	}
	defer f.Close()
	names := []string{fmt.Sprintf("%d", os.Getuid())}
	if u, err := user.Current(); err == nil {
		names = append(names, u.Username)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		for _, n := range names {
			if strings.HasPrefix(line, n+":") {
				return true, nil
			}
		}
	}
	return false, sc.Err()
}

// HaveExecutable reports whether name is on PATH.
func HaveExecutable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// PodmanInfo is the subset of `podman info` doctor needs.
type PodmanInfo struct {
	Rootless       bool
	CgroupsVersion string
	NetworkBackend string
}

// Run performs the host checks. podmanInfo is nil when podman is missing or
// broken; the caller reports that separately with the raw error.
func Run(allowRoot bool, egressProxy bool, podmanInfo *PodmanInfo) Report {
	r := Report{OK: true}

	if podmanInfo == nil {
		r.add("fail", "podman", "podman is not installed or not functional")
		return r
	}

	// aibox's whole security model rests on rootless podman: a container
	// escape lands as your unprivileged user, not as root on the host.
	if podmanInfo.Rootless {
		r.add("ok", "rootless", fmt.Sprintf("running rootless (uid %d)", os.Getuid()))
	} else if allowRoot {
		r.add("warn", "rootless", "rootful podman, allowed via --allow-root — isolation is much weaker")
	} else {
		r.add("fail", "rootless", "rootful podman — aibox requires rootless podman so a container escape cannot become host root; run as your normal user, or pass --allow-root to override")
	}

	if CgroupLimitsAvailable() {
		r.add("ok", "cgroups", fmt.Sprintf("cgroups %s — memory/cpu limits enforceable", podmanInfo.CgroupsVersion))
	} else {
		r.add("warn", "cgroups", fmt.Sprintf("cgroups %s without a delegated memory controller — memory/cpu limits will be skipped", podmanInfo.CgroupsVersion))
	}

	if podmanInfo.Rootless {
		if ok, _ := SubIDPresent(); ok {
			r.add("ok", "subid", "subuid/subgid mapping present")
		} else {
			r.add("fail", "subid", fmt.Sprintf("no /etc/subuid entry — run: sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 %s && podman system migrate", os.Getenv("USER")))
		}
	}

	if HaveExecutable("newuidmap") {
		r.add("ok", "newuidmap", "newuidmap/newgidmap available")
	} else {
		r.add("warn", "newuidmap", "newuidmap not found (package: uidmap) — --userns=keep-id may fail")
	}

	if SELinuxEnforcing() {
		r.add("ok", "selinux", "SELinux enforcing — bind mounts get the :z relabel automatically")
	}

	if egressProxy {
		// Internal networks only enforce no-route-out under netavark (podman 4+).
		if podmanInfo.NetworkBackend == "netavark" {
			r.add("ok", "egress", "network backend is netavark — internal networks enforce no-route-out")
		} else {
			r.add("fail", "egress", fmt.Sprintf("egress proxy mode needs the netavark network backend (podman 4+); this host reports %q", podmanInfo.NetworkBackend))
		}
	}

	return r
}
