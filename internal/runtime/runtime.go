// Package runtime abstracts the container engine. v1 drives the podman CLI —
// not the REST API: it matches the proven bclaude behaviour, rootless support
// is straightforward, there is no daemon or socket discovery, and --dry-run
// can print exactly what will run. The REST API can come later behind this
// same interface; Docker support would too, but does not exist yet.
package runtime

import (
	"context"
)

// Container is one engine container as aibox sees it: identity plus labels.
// Labels are the authoritative ownership mechanism — every query that decides
// what aibox may touch goes through them.
type Container struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Image  string            `json:"image"`
	State  string            `json:"state"` // running | exited | created | ...
	Labels map[string]string `json:"labels"`
}

// Volume is one named volume with its labels.
type Volume struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

// Filter selects containers by label equality. Name is available for the few
// places (the proxy sidecar) where a fixed name is the contract; lifecycle
// cleanup must filter on labels, never on names.
type Filter struct {
	Labels map[string]string
	Name   string
}

// StopOptions controls Stop.
type StopOptions struct {
	TimeoutSeconds int
}

// RemoveOptions controls Remove.
type RemoveOptions struct {
	Force bool
	// Volumes removes *anonymous* volumes the container created. Named
	// auth/config/cache volumes are never in scope here; removing those is a
	// separate, explicit act.
	Volumes bool
}

// ExecResult is the outcome of an Exec.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Runtime is the container engine surface aibox needs. Implementations:
// Podman (the real one) and Fake (a recorder for tests that asserts call
// sequences).
type Runtime interface {
	// Run runs a container in the foreground, wired to the caller's terminal.
	Run(ctx context.Context, argv []string) error

	ContainerExists(ctx context.Context, name string) (bool, error)
	ContainerRunning(ctx context.Context, name string) (bool, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, opts StopOptions) error
	Remove(ctx context.Context, name string, opts RemoveOptions) error
	List(ctx context.Context, f Filter) ([]Container, error)
	Exec(ctx context.Context, name string, cmd []string) (ExecResult, error)
	Logs(ctx context.Context, name string, tail int) (string, error)
	Kill(ctx context.Context, name string, signal string) error

	ImageExists(ctx context.Context, ref string) (bool, error)
	ImageLabel(ctx context.Context, ref string, label string) (string, error)
	ImageBuild(ctx context.Context, argv []string) error
	ImageRemove(ctx context.Context, ref string) error

	VolumeExists(ctx context.Context, name string) (bool, error)
	VolumeCreate(ctx context.Context, name string, labels map[string]string) error
	VolumeRemove(ctx context.Context, name string) error
	VolumeList(ctx context.Context, f Filter) ([]Volume, error)
	VolumeMountpoint(ctx context.Context, name string) (string, error)

	NetworkExists(ctx context.Context, name string) (bool, error)
	NetworkCreate(ctx context.Context, name string, internal bool, subnet string, labels map[string]string) error
	NetworkRemove(ctx context.Context, name string) error
}
