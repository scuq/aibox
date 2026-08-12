package devcontainer

import (
	"context"
	"fmt"

	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/runtime"
)

// Lifecycle manages Dev Containers that VS Code created and aibox owns. This
// is the feature missing from plain VS Code and the reason for the rewrite: a
// Dev Container survives VS Code stopping it, and without label-scoped
// management it is silently orphaned.
type Lifecycle struct {
	RT runtime.Runtime
}

// filter selects this project's devcontainers — by labels, never by name, so
// these commands can never reach an unrelated container that happens to be
// called something similar.
func filter(projectID string) runtime.Filter {
	return runtime.Filter{Labels: map[string]string{
		container.LabelManaged:   "true",
		container.LabelMode:      container.ModeDevcontainer,
		container.LabelProjectID: projectID,
	}}
}

// List returns every aibox devcontainer, across all projects.
func (l Lifecycle) List(ctx context.Context) ([]runtime.Container, error) {
	return l.RT.List(ctx, runtime.Filter{Labels: map[string]string{
		container.LabelManaged: "true",
		container.LabelMode:    container.ModeDevcontainer,
	}})
}

// Status returns this project's devcontainers.
func (l Lifecycle) Status(ctx context.Context, projectID string) ([]runtime.Container, error) {
	return l.RT.List(ctx, filter(projectID))
}

// Stop stops this project's running devcontainers and returns their names.
func (l Lifecycle) Stop(ctx context.Context, projectID string) ([]string, error) {
	found, err := l.RT.List(ctx, filter(projectID))
	if err != nil {
		return nil, err
	}
	var stopped []string
	for _, c := range found {
		if c.State != "running" {
			continue
		}
		if err := l.RT.Stop(ctx, c.Name, runtime.StopOptions{TimeoutSeconds: 10}); err != nil {
			return stopped, fmt.Errorf("could not stop %s: %w", c.Name, err)
		}
		stopped = append(stopped, c.Name)
	}
	return stopped, nil
}

// Remove stops and removes this project's devcontainers. Anonymous volumes
// the container created go with it; the named auth/config/cache volumes are
// **never** in scope here — removing a login or a project's sessions is a
// separate, explicit act, and this method must not know how to do it.
func (l Lifecycle) Remove(ctx context.Context, projectID string) ([]string, error) {
	found, err := l.RT.List(ctx, filter(projectID))
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, c := range found {
		if c.State == "running" {
			if err := l.RT.Stop(ctx, c.Name, runtime.StopOptions{TimeoutSeconds: 10}); err != nil {
				return removed, fmt.Errorf("could not stop %s: %w", c.Name, err)
			}
		}
		if err := l.RT.Remove(ctx, c.Name, runtime.RemoveOptions{Force: true, Volumes: true}); err != nil {
			return removed, fmt.Errorf("could not remove %s: %w", c.Name, err)
		}
		removed = append(removed, c.Name)
	}
	return removed, nil
}
