package app

import (
	"context"
	"testing"
	"time"

	"github.com/scuq/aibox/internal/secret"
)

func TestWaitGrantEndsWhenAccessIsRevokedElsewhere(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := secret.Add("token", "value"); err != nil {
		t.Fatal(err)
	}
	if err := secret.AllowAccess(); err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() { done <- waitGrant(context.Background(), 0) }()

	if err := secret.RevokeAccess(); err != nil {
		t.Fatal(err)
	}
	select {
	case closedElsewhere := <-done:
		if !closedElsewhere {
			t.Error("a revoke from another command should be reported as such")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("allow-access kept waiting after the grant was revoked")
	}
}

func TestWaitGrantEndsOnTTL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := secret.Add("token", "value"); err != nil {
		t.Fatal(err)
	}
	if err := secret.AllowAccess(); err != nil {
		t.Fatal(err)
	}
	if waitGrant(context.Background(), 10*time.Millisecond) {
		t.Error("an expired ttl is not a revoke from elsewhere")
	}
}
