package devcontainer

// Fake-runtime sequence tests: `devcontainer remove` must list by labels,
// stop matching containers, remove them with their anonymous volumes, and
// preserve every named auth/config/cache volume.

import (
	"context"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/container"
	"github.com/scuq/aibox/internal/runtime"
)

func devcontainerLabels(projectID string) map[string]string {
	l := container.BaseLabels(container.RoleWorkspace)
	l[container.LabelMode] = container.ModeDevcontainer
	l[container.LabelProjectID] = projectID
	return l
}

func fakeWith(t *testing.T) *runtime.Fake {
	t.Helper()
	f := runtime.NewFake()
	f.Containers["aibox-dc-chalk-aaaa11112222"] = &runtime.Container{
		Name: "aibox-dc-chalk-aaaa11112222", State: "running",
		Labels: devcontainerLabels("aaaa11112222"),
	}
	f.Containers["aibox-dc-other-bbbb33334444"] = &runtime.Container{
		Name: "aibox-dc-other-bbbb33334444", State: "exited",
		Labels: devcontainerLabels("bbbb33334444"),
	}
	// An unrelated container that HAPPENS to carry a colliding name fragment;
	// labels are the only thing that may select containers.
	f.Containers["chalk-dev"] = &runtime.Container{
		Name: "chalk-dev", State: "running", Labels: map[string]string{},
	}
	// The named volumes remove must never touch.
	f.Volumes["aibox-auth-claude"] = runtime.Volume{Name: "aibox-auth-claude"}
	f.Volumes["aibox-config-claude-aaaa11112222"] = runtime.Volume{Name: "aibox-config-claude-aaaa11112222"}
	f.Volumes["aibox-cache-shared"] = runtime.Volume{Name: "aibox-cache-shared"}
	return f
}

func TestRemoveSequence(t *testing.T) {
	f := fakeWith(t)
	lc := Lifecycle{RT: f}

	removed, err := lc.Remove(context.Background(), "aaaa11112222")
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "aibox-dc-chalk-aaaa11112222" {
		t.Fatalf("removed = %v", removed)
	}

	// The sequence: list by labels first, then stop, then remove.
	var verbs []string
	for _, c := range f.Calls {
		verbs = append(verbs, strings.Fields(c)[0])
	}
	joined := strings.Join(verbs, " ")
	if !strings.HasPrefix(joined, "list stop remove") {
		t.Errorf("call sequence = %v, want list → stop → remove", verbs)
	}
	// The list was label-scoped, never name-scoped.
	if !strings.Contains(f.Calls[0], container.LabelMode+":"+container.ModeDevcontainer) &&
		!strings.Contains(f.Calls[0], container.ModeDevcontainer) {
		t.Errorf("list not scoped to devcontainer mode: %s", f.Calls[0])
	}
	if !strings.HasSuffix(f.Calls[0], "name=") {
		t.Errorf("remove must not filter by name: %s", f.Calls[0])
	}

	// Anonymous volumes go with the container...
	if !strings.Contains(strings.Join(f.Calls, "\n"), "volumes=true") {
		t.Error("remove should drop the container's anonymous volumes")
	}
	// ...but no named volume was removed, and the other project's container
	// and the unrelated look-alike were never touched.
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "volume-remove") {
			t.Errorf("a named volume was removed: %s", call)
		}
	}
	if _, ok := f.Containers["aibox-dc-other-bbbb33334444"]; !ok {
		t.Error("another project's devcontainer was removed")
	}
	if _, ok := f.Containers["chalk-dev"]; !ok {
		t.Error("an unrelated container was removed — matching by name instead of labels")
	}
	for _, v := range []string{"aibox-auth-claude", "aibox-config-claude-aaaa11112222", "aibox-cache-shared"} {
		if _, ok := f.Volumes[v]; !ok {
			t.Errorf("named volume %s did not survive remove", v)
		}
	}
}

func TestStopOnlyStopsRunning(t *testing.T) {
	f := fakeWith(t)
	f.Containers["aibox-dc-chalk2-aaaa11112222"] = &runtime.Container{
		Name: "aibox-dc-chalk2-aaaa11112222", State: "exited",
		Labels: devcontainerLabels("aaaa11112222"),
	}
	lc := Lifecycle{RT: f}
	stopped, err := lc.Stop(context.Background(), "aaaa11112222")
	if err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0] != "aibox-dc-chalk-aaaa11112222" {
		t.Errorf("stopped = %v, want only the running one", stopped)
	}
	// Nothing was removed by a stop.
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "remove") {
			t.Errorf("stop must not remove: %s", call)
		}
	}
}

func TestListSpansProjects(t *testing.T) {
	f := fakeWith(t)
	lc := Lifecycle{RT: f}
	found, err := lc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Errorf("list should find both projects' devcontainers, got %d", len(found))
	}
	for _, c := range found {
		if c.Name == "chalk-dev" {
			t.Error("an unlabelled container appeared in the aibox listing")
		}
	}
}
