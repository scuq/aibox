package app

// Transcribed from the `run invocation (--dry-run)`, `egress filtering
// (--dry-run)`, `userns mapping`, `secret handling`, `shared auth volume` and
// `shared cache volume` sections of the bclaude test suite. composeSpec is
// pure, so these assert on the exact argv a run would execute.

import (
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/assistant"
	"github.com/scuq/aibox/internal/config"
	"github.com/scuq/aibox/internal/git"
)

func testInputs(t *testing.T) sessionInputs {
	t.Helper()
	cfg := config.Defaults()
	return sessionInputs{
		Config:      cfg,
		Assistant:   assistant.Claude{},
		Workspace:   "/home/u/proj",
		ProjectID:   "abcdef123456",
		ProjectName: "proj",
		TermEnv:     "xterm-256color",
		ColorTerm:   "truecolor",
	}
}

func argvString(t *testing.T, in sessionInputs, selinux bool) string {
	t.Helper()
	spec, err := composeSpec(in)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(spec.Argv(selinux), " ")
}

func TestRunSpecDefaults(t *testing.T) {
	argv := argvString(t, testInputs(t), false)

	for _, want := range []string{
		// the workspace is mounted at /work, writable
		"--volume /home/u/proj:/work ",
		"--workdir /work",
		// hardening on by default
		"--security-opt no-new-privileges",
		"--cap-drop=ALL",
		// resource caps applied, swap growth disabled
		"--memory 4g --memory-swap 4g",
		"--cpus 2",
		// host user mapped onto the container's uid 1000
		"--userns=keep-id:uid=1000,gid=1000",
		// fresh nosuid tmpfs /tmp
		"--tmpfs /tmp:rw,nosuid,nodev,exec,size=512m",
		// per-assistant volumes: config separate from auth
		"--volume aibox-config-claude-abcdef123456:/home/aibox/.claude ",
		"--volume aibox-auth-claude:/home/aibox/.claude-auth ",
		// shared cache volume
		"--volume aibox-cache-shared:/home/aibox/.cache ",
		// ephemeral class: gone on exit
		"run --rm",
		// ownership labels
		"--label io.aibox.managed=true",
		"--label io.aibox.mode=standalone",
		"--label io.aibox.project.id=abcdef123456",
		"--label io.aibox.assistant=claude",
		// the assistant executable is the command
		"localhost/aibox:latest claude",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	for _, unwanted := range []string{
		":/work:ro",         // workspace writable by default
		"--network",         // egress open is the default
		"HTTPS_PROXY",       // no proxy plumbing without proxy mode
		"host-aibox",        // no host creds mounted unless asked
		".gitconfig",        // the host gitconfig is never mounted, ever
		"--userns=keep-id ", // plain keep-id maps the caller to their own uid
	} {
		if strings.Contains(argv, unwanted) {
			t.Errorf("argv should not contain %q:\n%s", unwanted, argv)
		}
	}
}

func TestRunSpecReadOnlyWorkspace(t *testing.T) {
	in := testInputs(t)
	in.Config.Runtime.WorkspaceMode = "read-only"
	argv := argvString(t, in, false)
	if !strings.Contains(argv, ":/work:ro") {
		t.Errorf("read-only workspace not mounted ro:\n%s", argv)
	}
	// --ro leaves the config volume writable.
	if strings.Contains(argv, "/home/aibox/.claude:ro") {
		t.Errorf("config volume must stay writable:\n%s", argv)
	}
}

func TestRunSpecSELinux(t *testing.T) {
	in := testInputs(t)
	argv := argvString(t, in, true)
	for _, want := range []string{
		"/home/u/proj:/work:z",
		"aibox-config-claude-abcdef123456:/home/aibox/.claude:z",
		"aibox-auth-claude:/home/aibox/.claude-auth:z",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q under SELinux:\n%s", want, argv)
		}
	}
	// The bug class: a relabel option concatenated onto the destination path.
	for _, unwanted := range []string{"/work,z", ".claude,z", ".claude-auth,z"} {
		if strings.Contains(argv, unwanted) {
			t.Errorf("relabel landed in a destination path (%q):\n%s", unwanted, argv)
		}
	}
	// Read-only + relabel appends with a comma.
	in.Config.Runtime.WorkspaceMode = "read-only"
	argv = argvString(t, in, true)
	if !strings.Contains(argv, ":/work:ro,z") {
		t.Errorf("ro workspace should relabel as :ro,z:\n%s", argv)
	}
	// And nothing is relabelled on a host that is not enforcing.
	argv = argvString(t, testInputs(t), false)
	if strings.Contains(argv, ":z") {
		t.Errorf("relabel option present without SELinux:\n%s", argv)
	}
}

func TestRunSpecAllowPkgRelaxesHardening(t *testing.T) {
	in := testInputs(t)
	in.Config.Security.AllowPackageInstall = true
	argv := argvString(t, in, false)
	if strings.Contains(argv, "--cap-drop=ALL") || strings.Contains(argv, "no-new-privileges") {
		t.Errorf("--allow-pkg should relax exactly the two hardening flags:\n%s", argv)
	}
	// but nothing else
	if !strings.Contains(argv, "--pids-limit 2048") {
		t.Errorf("pids-limit must survive --allow-pkg:\n%s", argv)
	}
}

func TestRunSpecNoLimits(t *testing.T) {
	in := testInputs(t)
	in.Config.Runtime.Memory = ""
	in.Config.Runtime.CPUs = ""
	argv := argvString(t, in, false)
	if strings.Contains(argv, "--memory") || strings.Contains(argv, "--cpus") {
		t.Errorf("disabled caps still rendered:\n%s", argv)
	}
}

func TestRunSpecEgressProxy(t *testing.T) {
	in := testInputs(t)
	in.Config.Egress.Mode = "proxy"
	argv := argvString(t, in, false)
	for _, want := range []string{
		// the internal network is the enforcement
		"--network aibox-internal",
		// the proxy env vars point at the sidecar's static IP, both cases
		"--env HTTPS_PROXY=http://10.199.0.2:3128",
		"--env https_proxy=http://10.199.0.2:3128",
		"--env HTTP_PROXY=http://10.199.0.2:3128",
		"--env NO_PROXY=localhost,127.0.0.1,::1",
		"--env no_proxy=localhost,127.0.0.1,::1",
		// hardening stays on in proxy mode
		"--cap-drop=ALL",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("proxy argv missing %q:\n%s", want, argv)
		}
	}

	// The subnet is overridable, and the .2 derivation follows it.
	in.Config.Egress.Subnet = "10.42.0.0/24"
	argv = argvString(t, in, false)
	if !strings.Contains(argv, "HTTPS_PROXY=http://10.42.0.2:3128") {
		t.Errorf("proxy address does not follow the subnet:\n%s", argv)
	}
}

func TestRunSpecSecretsNeverInArgv(t *testing.T) {
	// The key is forwarded by name so podman imports it from the environment.
	// With the value inline it would show up in ps output on a shared host,
	// and --dry-run would print it for pasting into a bug report.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-canary")
	argv := argvString(t, testInputs(t), false)
	if !strings.Contains(argv, "--env ANTHROPIC_API_KEY ") &&
		!strings.HasSuffix(argv, "--env ANTHROPIC_API_KEY") &&
		!strings.Contains(argv, "--env ANTHROPIC_API_KEY --") {
		if !strings.Contains(argv, "--env ANTHROPIC_API_KEY") {
			t.Errorf("api key not forwarded by name:\n%s", argv)
		}
	}
	if strings.Contains(argv, "sk-ant-test-canary") {
		t.Errorf("the api key value reached podman's argv:\n%s", argv)
	}
}

func TestRunSpecCodexIsolation(t *testing.T) {
	in := testInputs(t)
	in.Assistant = assistant.Codex{}
	argv := argvString(t, in, false)
	for _, want := range []string{
		"aibox-config-codex-abcdef123456:/home/aibox/.codex ",
		"aibox-auth-codex:/home/aibox/.codex-auth ",
		"--env AIBOX_ASSISTANT=codex",
		"localhost/aibox:latest codex",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("codex argv missing %q:\n%s", want, argv)
		}
	}
	// Claude and Codex never share auth or config storage.
	if strings.Contains(argv, "claude") {
		t.Errorf("codex session references claude storage:\n%s", argv)
	}
}

func TestRunSpecShell(t *testing.T) {
	in := testInputs(t)
	in.Assistant = assistant.Shell{}
	in.Args = []string{"-c", "ls /work"}
	argv := argvString(t, in, false)
	if !strings.Contains(argv, "localhost/aibox:latest bash -c") {
		t.Errorf("shell should run bash with passthrough args:\n%s", argv)
	}
	if strings.Contains(argv, "aibox-auth") {
		t.Errorf("a shell session mounts no auth volume:\n%s", argv)
	}
}

func TestRunSpecGitMounts(t *testing.T) {
	in := testInputs(t)
	in.GitMounts = git.Plan(git.RepoInfo{
		IsRepo: true, DotGit: "/home/u/proj/.git", CommonDir: "/home/u/proj/.git",
	}, "read-only", "/home/u/proj")
	argv := argvString(t, in, false)
	if !strings.Contains(argv, "--volume /home/u/proj/.git:/work/.git:ro") {
		t.Errorf(".git must be mounted read-only over the workspace:\n%s", argv)
	}
}

func TestRunSpecSeedCreds(t *testing.T) {
	in := testInputs(t)
	in.SeedCreds = "/home/u/.claude/.credentials.json"
	argv := argvString(t, in, false)
	if !strings.Contains(argv, "--volume /home/u/.claude/.credentials.json:/run/host-aibox/.credentials.json:ro") {
		t.Errorf("seed must be mounted read-only at the seed path:\n%s", argv)
	}
}

func TestRunSpecRelaySeedMounts(t *testing.T) {
	in := testInputs(t)
	in.RelaySSHConfigPath = "/tmp/aibox-relay.x/ssh_config"
	in.RelayServicesPath = "/tmp/aibox-relay.x/services.json"
	argv := argvString(t, in, false)
	for _, want := range []string{
		"--volume /tmp/aibox-relay.x/ssh_config:/run/host-aibox/ssh_config:ro",
		"--volume /tmp/aibox-relay.x/services.json:/run/host-aibox/services.json:ro",
		"--env AIBOX_HAS_SERVICES=1",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("relay seed argv missing %q:\n%s", want, argv)
		}
	}
	// No relay files → no seed mounts and no marker.
	plain := argvString(t, testInputs(t), false)
	if strings.Contains(plain, "host-aibox/ssh_config") || strings.Contains(plain, "AIBOX_HAS_SERVICES") {
		t.Errorf("relay wiring present without services:\n%s", plain)
	}
}

func TestRunSpecPassthroughArgs(t *testing.T) {
	in := testInputs(t)
	in.Args = []string{"-p", "hello", "--model", "opus"}
	argv := argvString(t, in, false)
	if !strings.Contains(argv, "claude -p hello --model opus") {
		t.Errorf("assistant flags must pass straight through:\n%s", argv)
	}
}
