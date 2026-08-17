package assistant_test

// The credential hand-off protocol — the most subtle logic in the codebase.
// These tests are transcribed from the `login hand-off between overlapping
// sessions` and `shared auth volume` sections of the bclaude test suite
// (tests/run-tests.sh), which is the compatibility specification. They run the
// real embedded entrypoint under bash, on the host, with its two directories
// pointed at temp dirs — no podman needed.
//
// The protocol: AUTH_CREDS_IN is the auth volume's fingerprint *before*
// anything is copied in, and every decision on the way out compares against
// it. An untouched session must not put a spent token back over a newer one;
// a config dir holding the *only* copy of a login must still reach an empty
// auth volume.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/assets"
)

// epRun runs the embedded entrypoint with the given starting tokens and a
// session body. It returns the auth volume's final credential content and the
// entrypoint's output.
//
// The session body writing $CFG/.credentials.json stands for the assistant
// refreshing the token; writing $AUTH/.credentials.json stands for a
// concurrent session refreshing and exiting while this one is still up.
func epRun(t *testing.T, authToken, configToken, session string) (authOut, log string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	auth := filepath.Join(dir, "auth")
	for _, d := range []string{cfg, auth} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if authToken != "" {
		if err := os.WriteFile(filepath.Join(auth, ".credentials.json"), []byte(authToken), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if configToken != "" {
		if err := os.WriteFile(filepath.Join(cfg, ".credentials.json"), []byte(configToken), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ep := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(ep, assets.Read("entrypoint.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", ep, "bash", "-c", session)
	cmd.Env = append(os.Environ(),
		"AIBOX_ASSISTANT=claude",
		"AIBOX_CONFIG_DIR="+cfg,
		"AIBOX_AUTH_DIR="+auth,
		"HOME="+dir,
		"ANTHROPIC_API_KEY=", // never let a real key mask the no-creds paths
	)
	// CFG/AUTH shorthand for the session bodies.
	cmd.Env = append(cmd.Env, "CFG="+cfg, "AUTH="+auth)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(auth, ".credentials.json"))
	return string(data), string(out)
}

func TestLoginHandoff(t *testing.T) {
	refresh := `printf C1 > "$CFG/.credentials.json"`

	tests := []struct {
		name        string
		authToken   string // auth volume at session start
		configToken string // config volume at session start
		session     string
		wantAuth    string // auth volume after exit
	}{
		{"a refreshed token reaches the auth volume", "C0", "", refresh, "C1"},
		{"a first login reaches the auth volume", "", "", refresh, "C1"},
		{"an unchanged session leaves the token alone", "C0", "", "true", "C0"},
		// The bug this protocol exists for: this session never touched the
		// token, so it must not undo the refresh another session made while
		// it was running.
		{
			"an unchanged session does not restore a spent token over a newer one",
			"C0", "", `printf C1 > "$AUTH/.credentials.json"`, "C1",
		},
		// Both refreshed: the one that landed in the volume after we started
		// is newer.
		{
			"a concurrent refresh wins over ours",
			"C0", "",
			`printf C1 > "$CFG/.credentials.json"; printf C2 > "$AUTH/.credentials.json"`,
			"C2",
		},
		// A config volume holding the only copy of a login — one from before
		// the auth volume existed, or an existing config volume paired with a
		// fresh auth volume — has to reach the empty auth volume. Skipping
		// the write because the session "changed nothing" would strand it.
		{"a config-only login migrates into an empty auth volume", "", "C0", "true", "C0"},
		{"a config-only login migrates even after a refresh", "", "C0", refresh, "C1"},
		// But it must not overwrite a login another session established in
		// the meantime.
		{
			"migration yields to a login another session just made",
			"", "C0", `printf C9 > "$AUTH/.credentials.json"`, "C9",
		},
		{"an empty auth volume stays empty with no login at all", "", "", "true", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := epRun(t, tt.authToken, tt.configToken, tt.session)
			if got != tt.wantAuth {
				t.Errorf("auth volume after exit = %q, want %q", got, tt.wantAuth)
			}
		})
	}
}

func TestEntrypointClearsEphemeral(t *testing.T) {
	// The entrypoint wipes /ephemeral (AIBOX_EPHEMERAL_DIR in tests) clean at
	// the start of every session.
	dir := t.TempDir()
	eph := filepath.Join(dir, "ephemeral")
	if err := os.MkdirAll(filepath.Join(eph, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"leftover.txt", ".hidden", "sub/nested"} {
		if err := os.WriteFile(filepath.Join(eph, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ep := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(ep, assets.Read("entrypoint.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", ep, "true")
	cmd.Env = append(os.Environ(), "AIBOX_ASSISTANT=none", "AIBOX_EPHEMERAL_DIR="+eph, "HOME="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", err, out)
	}
	entries, err := os.ReadDir(eph)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("/ephemeral not cleared, still holds: %v", entries)
	}
	// The directory itself must survive (it is the mount point).
	if st, err := os.Stat(eph); err != nil || !st.IsDir() {
		t.Errorf("the ephemeral dir itself must survive the wipe")
	}
}

func TestHandoffYieldingIsReported(t *testing.T) {
	_, log := epRun(t, "C0", "",
		`printf C1 > "$CFG/.credentials.json"; printf C2 > "$AUTH/.credentials.json"`)
	if !strings.Contains(log, "keeping theirs") {
		t.Errorf("yielding to a concurrent refresh should be reported; log:\n%s", log)
	}
}

func TestCredentialSeedAlwaysPublishes(t *testing.T) {
	// An explicit --seed-creds is an instruction: it always publishes, even
	// when the session itself changed nothing.
	dir := t.TempDir()
	cfg := filepath.Join(dir, "cfg")
	auth := filepath.Join(dir, "auth")
	seedDir := filepath.Join(dir, "seed")
	for _, d := range []string{cfg, auth, seedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(auth, ".credentials.json"), []byte("OLD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, ".credentials.json"), []byte("SEEDED"), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := filepath.Join(dir, "entrypoint.sh")
	if err := os.WriteFile(ep, assets.Read("entrypoint.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", ep, "true")
	cmd.Env = append(os.Environ(),
		"AIBOX_ASSISTANT=claude",
		"AIBOX_CONFIG_DIR="+cfg,
		"AIBOX_AUTH_DIR="+auth,
		"AIBOX_SEED_DIR="+seedDir,
		"HOME="+dir,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("entrypoint failed: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(auth, ".credentials.json"))
	if string(got) != "SEEDED" {
		t.Errorf("auth volume after seeded session = %q, want SEEDED", got)
	}
}
