package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Podman drives the podman CLI.
type Podman struct {
	// Executable defaults to "podman".
	Executable string
}

// NewPodman returns a CLI-backed runtime.
func NewPodman() *Podman { return &Podman{Executable: "podman"} }

func (p *Podman) exe() string {
	if p.Executable == "" {
		return "podman"
	}
	return p.Executable
}

// Available reports whether the podman executable exists on PATH.
func (p *Podman) Available() bool {
	_, err := exec.LookPath(p.exe())
	return err == nil
}

// command captures stdout/stderr and returns stdout on success.
func (p *Podman) command(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, p.exe(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("podman %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// silent runs a command purely for its exit code.
func (p *Podman) silent(ctx context.Context, args ...string) bool {
	cmd := exec.CommandContext(ctx, p.exe(), args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// Capture runs an arbitrary podman invocation and returns its stdout — for
// one-shot reads like extracting a file from an image. Not part of the
// Runtime interface: it is a podman-CLI convenience, not engine behaviour.
func (p *Podman) Capture(ctx context.Context, args ...string) (string, error) {
	return p.command(ctx, args...)
}

// Run hands the terminal to podman: interactive sessions need stdin/stdout
// attached, and the child's exit code is the session's exit code.
func (p *Podman) Run(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, p.exe(), argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (p *Podman) ContainerExists(ctx context.Context, name string) (bool, error) {
	return p.silent(ctx, "container", "exists", name), nil
}

func (p *Podman) ContainerRunning(ctx context.Context, name string) (bool, error) {
	out, err := p.command(ctx, "container", "inspect", name, "--format", "{{.State.Running}}")
	if err != nil {
		return false, nil // not existing is not running
	}
	return strings.TrimSpace(out) == "true", nil
}

func (p *Podman) Start(ctx context.Context, name string) error {
	_, err := p.command(ctx, "start", name)
	return err
}

func (p *Podman) Stop(ctx context.Context, name string, opts StopOptions) error {
	args := []string{"stop"}
	if opts.TimeoutSeconds > 0 {
		args = append(args, "--time", strconv.Itoa(opts.TimeoutSeconds))
	}
	args = append(args, name)
	_, err := p.command(ctx, args...)
	return err
}

func (p *Podman) Remove(ctx context.Context, name string, opts RemoveOptions) error {
	args := []string{"rm"}
	if opts.Force {
		args = append(args, "--force")
	}
	if opts.Volumes {
		args = append(args, "--volumes")
	}
	args = append(args, name)
	_, err := p.command(ctx, args...)
	return err
}

func filterArgs(f Filter) []string {
	var args []string
	for k, v := range f.Labels {
		args = append(args, "--filter", fmt.Sprintf("label=%s=%s", k, v))
	}
	if f.Name != "" {
		args = append(args, "--filter", "name="+f.Name)
	}
	return args
}

func (p *Podman) List(ctx context.Context, f Filter) ([]Container, error) {
	args := append([]string{"ps", "--all", "--format", "json"}, filterArgs(f)...)
	out, err := p.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("cannot parse podman ps output: %w", err)
	}
	containers := make([]Container, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = r.Names[0]
		}
		containers = append(containers, Container{
			ID: r.ID, Name: name, Image: r.Image, State: r.State, Labels: r.Labels,
		})
	}
	return containers, nil
}

func (p *Podman) Exec(ctx context.Context, name string, cmdv []string) (ExecResult, error) {
	args := append([]string{"exec", name}, cmdv...)
	cmd := exec.CommandContext(ctx, p.exe(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	res := ExecResult{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

func (p *Podman) Logs(ctx context.Context, name string, tail int) (string, error) {
	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "--tail", strconv.Itoa(tail))
	}
	args = append(args, name)
	// squid logs to stdout, but podman may deliver container output on either
	// stream depending on the log driver; capture both.
	cmd := exec.CommandContext(ctx, p.exe(), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("podman logs %s: %w", name, err)
	}
	return buf.String(), nil
}

func (p *Podman) Kill(ctx context.Context, name string, signal string) error {
	_, err := p.command(ctx, "kill", "--signal", signal, name)
	return err
}

func (p *Podman) ImageExists(ctx context.Context, ref string) (bool, error) {
	return p.silent(ctx, "image", "exists", ref), nil
}

func (p *Podman) ImageLabel(ctx context.Context, ref string, label string) (string, error) {
	out, err := p.command(ctx, "image", "inspect", ref,
		"--format", fmt.Sprintf("{{index .Labels %q}}", label))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ImageBuild streams build output to stderr so the user watches progress
// while stdout stays clean for machine-readable output.
func (p *Podman) ImageBuild(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, p.exe(), argv...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (p *Podman) ImageRemove(ctx context.Context, ref string) error {
	_, err := p.command(ctx, "rmi", "--force", ref)
	return err
}

func (p *Podman) VolumeExists(ctx context.Context, name string) (bool, error) {
	return p.silent(ctx, "volume", "exists", name), nil
}

func (p *Podman) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	args := []string{"volume", "create"}
	for k, v := range labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, name)
	_, err := p.command(ctx, args...)
	return err
}

func (p *Podman) VolumeRemove(ctx context.Context, name string) error {
	_, err := p.command(ctx, "volume", "rm", name)
	return err
}

func (p *Podman) VolumeList(ctx context.Context, f Filter) ([]Volume, error) {
	args := append([]string{"volume", "ls", "--format", "json"}, filterArgs(f)...)
	out, err := p.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("cannot parse podman volume ls output: %w", err)
	}
	vols := make([]Volume, 0, len(raw))
	for _, r := range raw {
		vols = append(vols, Volume{Name: r.Name, Labels: r.Labels})
	}
	return vols, nil
}

func (p *Podman) VolumeMountpoint(ctx context.Context, name string) (string, error) {
	out, err := p.command(ctx, "volume", "inspect", name, "--format", "{{.Mountpoint}}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (p *Podman) NetworkExists(ctx context.Context, name string) (bool, error) {
	return p.silent(ctx, "network", "exists", name), nil
}

// NetworkInternal reports whether a network is `--internal` (no route out).
// aibox-internal must be true (the workload's isolation depends on it) and
// aibox-egress must be false (the sidecars' way out) — doctor checks both.
func (p *Podman) NetworkInternal(ctx context.Context, name string) (bool, error) {
	out, err := p.command(ctx, "network", "inspect", name, "--format", "{{.Internal}}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// ContainerNetworks returns the networks a container is attached to.
func (p *Podman) ContainerNetworks(ctx context.Context, name string) ([]string, error) {
	out, err := p.command(ctx, "container", "inspect", name, "--format",
		"{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (p *Podman) NetworkCreate(ctx context.Context, name string, internal bool, subnet string, labels map[string]string) error {
	args := []string{"network", "create"}
	if internal {
		args = append(args, "--internal")
	}
	if subnet != "" {
		args = append(args, "--subnet", subnet)
	}
	for k, v := range labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, name)
	_, err := p.command(ctx, args...)
	return err
}

func (p *Podman) NetworkRemove(ctx context.Context, name string) error {
	_, err := p.command(ctx, "network", "rm", name)
	return err
}

// Info returns host facts doctor needs, from one `podman info` call —
// the call costs seconds, so nothing on the hot path uses it.
type Info struct {
	Rootless       bool
	CgroupsVersion string
	NetworkBackend string
}

func (p *Podman) Info(ctx context.Context) (Info, error) {
	out, err := p.command(ctx, "info", "--format",
		"{{.Host.Security.Rootless}} {{.Host.CgroupsVersion}} {{.Host.NetworkBackend}}")
	if err != nil {
		return Info{}, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	info := Info{}
	if len(fields) > 0 {
		info.Rootless = fields[0] == "true"
	}
	if len(fields) > 1 {
		info.CgroupsVersion = fields[1]
	}
	if len(fields) > 2 {
		info.NetworkBackend = fields[2]
	}
	return info, nil
}
