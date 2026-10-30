package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"

	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/runtime"
)

// cmdStopAll stops every running aibox container on this host, across all
// projects. The named volumes and networks stay. The next run or 'aibox net up'
// brings the sidecars back.
func cmdStopAll(ctx context.Context, p *output.Printer, rt *runtime.Podman, args []string) error {
	fs := flag.NewFlagSet("stop-all", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print what would stop instead of stopping it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("stop-all takes no arguments")
	}
	if !rt.Available() {
		return fmt.Errorf("podman is not installed")
	}
	running, err := runningContainers(ctx, rt)
	if err != nil {
		return err
	}
	if len(running) == 0 {
		p.Info("no aibox containers are running")
		return nil
	}
	if *dryRun {
		for _, c := range running {
			fmt.Println(c.Name)
		}
		return nil
	}
	stopped, err := stopContainers(ctx, rt, running)
	for _, name := range stopped {
		p.Good("stopped", "%s", name)
	}
	return err
}

// runningContainers returns the running containers aibox owns, selected by
// label, never by name. Workloads come first, so a session does not lose its
// proxy before it stops.
func runningContainers(ctx context.Context, rt runtime.Runtime) ([]runtime.Container, error) {
	found, err := rt.List(ctx, runtime.Filter{Labels: map[string]string{container.LabelManaged: "true"}})
	if err != nil {
		return nil, err
	}
	var running []runtime.Container
	for _, c := range found {
		if c.State == "running" {
			running = append(running, c)
		}
	}
	sort.SliceStable(running, func(i, j int) bool {
		ri, rj := running[i].Labels[container.LabelRole], running[j].Labels[container.LabelRole]
		if ri != rj {
			return ri == container.RoleWorkspace
		}
		return running[i].Name < running[j].Name
	})
	return running, nil
}

// stopContainers stops each container and returns the names it stopped. One
// failure does not end the loop: the caller wants as much stopped as possible.
// A container that is gone by the time of its stop has exited by itself.
func stopContainers(ctx context.Context, rt runtime.Runtime, containers []runtime.Container) ([]string, error) {
	var stopped []string
	var errs []error
	for _, c := range containers {
		err := rt.Stop(ctx, c.Name, runtime.StopOptions{TimeoutSeconds: 10})
		if err == nil {
			stopped = append(stopped, c.Name)
			continue
		}
		if still, _ := rt.ContainerRunning(ctx, c.Name); still {
			errs = append(errs, fmt.Errorf("could not stop %s: %w", c.Name, err))
		}
	}
	return stopped, errors.Join(errs...)
}
