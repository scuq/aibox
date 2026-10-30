package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/project"
	"github.com/scuq/aibox/internal/runtime"
)

func TestCmdPodmanTogglesMarker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ws := t.TempDir()
	p := &output.Printer{W: os.Stderr}
	ctx := context.Background()
	rt := runtime.NewPodman()
	canon, err := project.Canonical(ws)
	if err != nil {
		t.Fatal(err)
	}
	id, err := project.ID(canon)
	if err != nil {
		t.Fatal(err)
	}

	if err := cmdPodman(ctx, p, rt, []string{"enable", "-w", ws}); err != nil {
		t.Fatal(err)
	}
	if !config.NestedPodmanEnabled(id) {
		t.Error("enable should set the marker")
	}
	if err := cmdPodman(ctx, p, rt, []string{"status", "-w", ws}); err != nil {
		t.Fatal(err)
	}
	if err := cmdPodman(ctx, p, rt, []string{"disable", "-w", ws}); err != nil {
		t.Fatal(err)
	}
	if config.NestedPodmanEnabled(id) {
		t.Error("disable should clear the marker")
	}
}

func TestCmdPodmanRejectsUnknownSubcommand(t *testing.T) {
	p := &output.Printer{W: os.Stderr}
	err := cmdPodman(context.Background(), p, runtime.NewPodman(), []string{"on"})
	if err == nil || !strings.Contains(err.Error(), "enable | disable | status") {
		t.Errorf("want a usage error, got %v", err)
	}
}
