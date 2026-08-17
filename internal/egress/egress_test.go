package egress

// Transcribed from the `egress filtering (--dry-run)` section of the bclaude
// test suite (the squid/allowlist parts; the container-argv parts live in
// internal/app).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyIPDerivation(t *testing.T) {
	// The sidecar's address is derived arithmetically (.2), never resolved by
	// container name — name resolution on internal networks varies across
	// podman versions.
	tests := []struct {
		subnet string
		want   string
	}{
		{"10.199.0.0/24", "10.199.0.2"},
		{"10.42.0.0/24", "10.42.0.2"},
		{"192.168.100.0/24", "192.168.100.2"},
	}
	for _, tt := range tests {
		got, err := ProxyIP(tt.subnet)
		if err != nil {
			t.Fatalf("ProxyIP(%q): %v", tt.subnet, err)
		}
		if got != tt.want {
			t.Errorf("ProxyIP(%q) = %q, want %q", tt.subnet, got, tt.want)
		}
	}
	if _, err := ProxyIP("not-a-subnet"); err == nil {
		t.Error("invalid subnet should be refused")
	}
}

func TestNetworkNames(t *testing.T) {
	// The topology names are a contract: `aibox net up` creates exactly these,
	// the workload joins only the internal one, and both sidecars bridge to
	// aibox-egress. Renaming either silently strands a running setup.
	if NetInternal != "aibox-internal" {
		t.Errorf("internal network name = %q", NetInternal)
	}
	if NetEgress != "aibox-egress" {
		t.Errorf("egress network name = %q", NetEgress)
	}
}

func TestProxyURL(t *testing.T) {
	got, err := ProxyURL("10.199.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://10.199.0.2:3128" {
		t.Errorf("ProxyURL = %q", got)
	}
}

func TestSquidConf(t *testing.T) {
	conf, err := SquidConf("10.199.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"http_access deny all",                // deny by default
		AllowlistContainerPath,                // reads the allowlist
		"stdio:/dev/stdout",                   // one log line per request -> podman logs
		"acl aibox_clients src 10.199.0.0/24", // only serves the internal subnet
		"cache deny all",                      // never caches
		"http_access allow aibox_clients allowed",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("squid.conf is missing %q", want)
		}
	}
	// The domain allowlist is the enforcement, not the port: allowlisted hosts
	// must work on any port (internal APIs on :8443, registries on :5000, …),
	// so the port-based denies must NOT be present.
	for _, unwanted := range []string{
		"deny !Safe_ports",
		"deny CONNECT !SSL_ports",
	} {
		if strings.Contains(conf, unwanted) {
			t.Errorf("squid.conf still restricts ports (%q) — allowlisted hosts on non-standard ports would be blocked", unwanted)
		}
	}
}

func TestValidateDomain(t *testing.T) {
	for _, ok := range []string{"api.anthropic.com", ".vscode-unpkg.net", "a-b.c"} {
		if err := ValidateDomain(ok); err != nil {
			t.Errorf("ValidateDomain(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "evil domain", "no;semicolons", "sneaky\nnewline"} {
		if err := ValidateDomain(bad); err == nil {
			t.Errorf("ValidateDomain(%q) should fail", bad)
		}
	}
}

func TestBaseAllowlistCoversAnsibleGalaxy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := EnsureFragments(nil); err != nil {
		t.Fatal(err)
	}
	acl, err := Compose(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// ansible-galaxy needs both: the API host and the S3 bucket its artifact
	// downloads redirect to. Losing the second is the sneaky regression —
	// metadata resolves, the fetch 403s.
	for _, want := range []string{
		"galaxy.ansible.com",
		"ansible-galaxy-ng.s3.dualstack.us-east-1.amazonaws.com",
	} {
		if !strings.Contains(acl, want) {
			t.Errorf("base allowlist missing %q — ansible-galaxy installs will fail in proxy mode", want)
		}
	}
}

func TestCompose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := EnsureFragments([]string{"claude"}); err != nil {
		t.Fatal(err)
	}
	// Two project fragments — the shared squid unions BOTH, regardless of which
	// workspace triggers the compose. This is the reboot regression: a domain
	// allowed for one project must not vanish when another context (or `net
	// up`, with no project at all) regenerates the allowlist.
	if err := os.WriteFile(FragmentPath("project-abc123"), []byte("internal.example.com\n# comment\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FragmentPath("project-def456"), []byte("other-project.example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	acl, err := Compose([]string{"claude"}, []string{"cli.example.com", "github.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"api.anthropic.com",         // assistant fragment
		"proxy.golang.org",          // base fragment
		"internal.example.com",      // project abc123 fragment
		"other-project.example.com", // project def456 fragment — unioned in
		"cli.example.com",           // CLI addition
	} {
		if !strings.Contains(acl, want) {
			t.Errorf("composed ACL is missing %q", want)
		}
	}
	// github.com is in base and repeated on the CLI: deduplicated.
	if strings.Count(acl, "\ngithub.com\n") != 1 {
		t.Errorf("github.com should appear exactly once:\n%s", acl)
	}
	// Codex was not enabled, so its domains must not leak in — enabling both
	// assistants is what unions the allowlists, and that is an explicit,
	// warned choice.
	if strings.Contains(acl, "api.openai.com") {
		t.Error("codex domains composed without codex enabled")
	}
}

func TestComposeRejectsMalformedEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Dir(FragmentPath("base")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(FragmentPath("base"), []byte("ok.example.com\nbroken entry with spaces\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Compose(nil, nil); err == nil {
		t.Error("a malformed allowlist entry should fail composition, not reach squid")
	}
}

func TestParseDenied(t *testing.T) {
	logs := strings.Join([]string{
		"1723400000.123    0 10.199.0.10 TCP_DENIED/403 3893 CONNECT evil.example.com:443 - HIER_NONE/- text/html",
		"1723400001.456    0 10.199.0.10 TCP_DENIED/403 3893 CONNECT evil.example.com:443 - HIER_NONE/- text/html",
		"1723400002.789   52 10.199.0.10 TCP_TUNNEL/200 6032 CONNECT api.anthropic.com:443 - HIER_DIRECT/1.2.3.4 -",
		"1723400003.000    0 10.199.0.10 TCP_DENIED/403 3893 GET http://plain.example.com/x - HIER_NONE/- text/html",
	}, "\n")
	counts := ParseDenied(logs)
	if counts["evil.example.com"] != 2 {
		t.Errorf("evil.example.com count = %d, want 2", counts["evil.example.com"])
	}
	if _, ok := counts["api.anthropic.com"]; ok {
		t.Error("an allowed request was counted as denied")
	}
	if counts["http://plain.example.com/x"] != 1 {
		t.Errorf("plain-http denial missing: %v", counts)
	}
}
