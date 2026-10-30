package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/runtime"
)

func fakeWithFleet() *runtime.Fake {
	rt := runtime.NewFake()
	add := func(name, state string, labels map[string]string) {
		rt.Containers[name] = &runtime.Container{Name: name, State: state, Labels: labels}
	}
	add("aibox-squid", "running", container.BaseLabels(container.RoleProxy))
	add("session-b", "running", container.BaseLabels(container.RoleWorkspace))
	add("session-a", "running", container.BaseLabels(container.RoleWorkspace))
	add("session-old", "exited", container.BaseLabels(container.RoleWorkspace))
	add("unrelated", "running", map[string]string{"com.example": "x"})
	return rt
}

func TestRunningContainersIsLabelScopedAndWorkloadsFirst(t *testing.T) {
	rt := fakeWithFleet()
	running, err := runningContainers(context.Background(), rt)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range running {
		names = append(names, c.Name)
	}
	want := []string{"session-a", "session-b", "aibox-squid"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("got %v, want %v", names, want)
	}
}

func TestStopContainersKeepsGoingAndIgnoresVanished(t *testing.T) {
	rt := fakeWithFleet()
	ctx := context.Background()
	running, err := runningContainers(ctx, rt)
	if err != nil {
		t.Fatal(err)
	}
	// A --rm session that exits between the list and the stop.
	delete(rt.Containers, "session-a")

	stopped, err := stopContainers(ctx, rt, running)
	if err != nil {
		t.Errorf("a container that already exited is not a failure: %v", err)
	}
	if want := []string{"session-b", "aibox-squid"}; !reflect.DeepEqual(stopped, want) {
		t.Errorf("stopped = %v, want %v", stopped, want)
	}
	if rt.Containers["unrelated"].State != "running" {
		t.Error("an unlabelled container must never be stopped")
	}
	for _, call := range rt.Calls {
		if strings.HasPrefix(call, "remove ") || strings.HasPrefix(call, "volume-") {
			t.Errorf("stop-all must only stop, got %q", call)
		}
	}
}
