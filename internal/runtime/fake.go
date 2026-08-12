package runtime

import (
	"context"
	"fmt"
	"strings"
)

// Fake is a recording Runtime for tests. It answers from in-memory state and
// records every call as one line, so tests can assert call *sequences* — e.g.
// that a remove lists by labels, stops matching containers, removes them, and
// never touches named volumes.
type Fake struct {
	Calls []string

	Containers map[string]*Container
	Volumes    map[string]Volume
	Networks   map[string]bool
	Images     map[string]map[string]string // ref -> labels

	// RunError, when set, is returned by Run.
	RunError error
}

// NewFake returns an empty fake runtime.
func NewFake() *Fake {
	return &Fake{
		Containers: map[string]*Container{},
		Volumes:    map[string]Volume{},
		Networks:   map[string]bool{},
		Images:     map[string]map[string]string{},
	}
}

func (f *Fake) record(format string, args ...any) {
	f.Calls = append(f.Calls, fmt.Sprintf(format, args...))
}

// CallsMatching returns the recorded calls whose first word is verb.
func (f *Fake) CallsMatching(verb string) []string {
	var out []string
	for _, c := range f.Calls {
		if strings.HasPrefix(c, verb+" ") || c == verb {
			out = append(out, c)
		}
	}
	return out
}

func (f *Fake) Run(ctx context.Context, argv []string) error {
	f.record("run %s", strings.Join(argv, " "))
	return f.RunError
}

func (f *Fake) ContainerExists(ctx context.Context, name string) (bool, error) {
	f.record("container-exists %s", name)
	_, ok := f.Containers[name]
	return ok, nil
}

func (f *Fake) ContainerRunning(ctx context.Context, name string) (bool, error) {
	f.record("container-running %s", name)
	c, ok := f.Containers[name]
	return ok && c.State == "running", nil
}

func (f *Fake) Start(ctx context.Context, name string) error {
	f.record("start %s", name)
	if c, ok := f.Containers[name]; ok {
		c.State = "running"
		return nil
	}
	return fmt.Errorf("no such container %s", name)
}

func (f *Fake) Stop(ctx context.Context, name string, opts StopOptions) error {
	f.record("stop %s", name)
	if c, ok := f.Containers[name]; ok {
		c.State = "exited"
		return nil
	}
	return fmt.Errorf("no such container %s", name)
}

func (f *Fake) Remove(ctx context.Context, name string, opts RemoveOptions) error {
	f.record("remove %s force=%v volumes=%v", name, opts.Force, opts.Volumes)
	if _, ok := f.Containers[name]; !ok {
		return fmt.Errorf("no such container %s", name)
	}
	delete(f.Containers, name)
	return nil
}

func matches(labels map[string]string, name string, f Filter) bool {
	for k, v := range f.Labels {
		if labels[k] != v {
			return false
		}
	}
	if f.Name != "" && !strings.Contains(name, f.Name) {
		return false
	}
	return true
}

func (f *Fake) List(ctx context.Context, flt Filter) ([]Container, error) {
	f.record("list labels=%v name=%s", flt.Labels, flt.Name)
	var out []Container
	for name, c := range f.Containers {
		if matches(c.Labels, name, flt) {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (f *Fake) Exec(ctx context.Context, name string, cmd []string) (ExecResult, error) {
	f.record("exec %s %s", name, strings.Join(cmd, " "))
	return ExecResult{}, nil
}

func (f *Fake) Logs(ctx context.Context, name string, tail int) (string, error) {
	f.record("logs %s", name)
	return "", nil
}

func (f *Fake) Kill(ctx context.Context, name string, signal string) error {
	f.record("kill %s %s", name, signal)
	return nil
}

func (f *Fake) ImageExists(ctx context.Context, ref string) (bool, error) {
	f.record("image-exists %s", ref)
	_, ok := f.Images[ref]
	return ok, nil
}

func (f *Fake) ImageLabel(ctx context.Context, ref string, label string) (string, error) {
	f.record("image-label %s %s", ref, label)
	labels, ok := f.Images[ref]
	if !ok {
		return "", fmt.Errorf("no such image %s", ref)
	}
	return labels[label], nil
}

func (f *Fake) ImageBuild(ctx context.Context, argv []string) error {
	f.record("image-build %s", strings.Join(argv, " "))
	return nil
}

func (f *Fake) ImageRemove(ctx context.Context, ref string) error {
	f.record("image-remove %s", ref)
	delete(f.Images, ref)
	return nil
}

func (f *Fake) VolumeExists(ctx context.Context, name string) (bool, error) {
	f.record("volume-exists %s", name)
	_, ok := f.Volumes[name]
	return ok, nil
}

func (f *Fake) VolumeCreate(ctx context.Context, name string, labels map[string]string) error {
	f.record("volume-create %s", name)
	f.Volumes[name] = Volume{Name: name, Labels: labels}
	return nil
}

func (f *Fake) VolumeRemove(ctx context.Context, name string) error {
	f.record("volume-remove %s", name)
	delete(f.Volumes, name)
	return nil
}

func (f *Fake) VolumeList(ctx context.Context, flt Filter) ([]Volume, error) {
	f.record("volume-list labels=%v", flt.Labels)
	var out []Volume
	for name, v := range f.Volumes {
		if matches(v.Labels, name, flt) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *Fake) VolumeMountpoint(ctx context.Context, name string) (string, error) {
	f.record("volume-mountpoint %s", name)
	return "/fake/volumes/" + name, nil
}

func (f *Fake) NetworkExists(ctx context.Context, name string) (bool, error) {
	f.record("network-exists %s", name)
	return f.Networks[name], nil
}

func (f *Fake) NetworkCreate(ctx context.Context, name string, internal bool, subnet string, labels map[string]string) error {
	f.record("network-create %s internal=%v subnet=%s", name, internal, subnet)
	f.Networks[name] = true
	return nil
}

func (f *Fake) NetworkRemove(ctx context.Context, name string) error {
	f.record("network-remove %s", name)
	delete(f.Networks, name)
	return nil
}

var _ Runtime = (*Fake)(nil)
var _ Runtime = (*Podman)(nil)
