package secret

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	for _, v := range []string{"", "hunter2", "a value with spaces & symbols: $x=1", strings.Repeat("z", 5000)} {
		blob, err := Encrypt("some-key", v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Decrypt("some-key", blob)
		if err != nil {
			t.Fatalf("decrypt %q: %v", v, err)
		}
		if got != v {
			t.Errorf("roundtrip mismatch: got %q want %q", got, v)
		}
	}
	// Wrong key must fail, not return garbage silently.
	blob, _ := Encrypt("right", "secret")
	if _, err := Decrypt("wrong", blob); err == nil {
		t.Error("decrypt with the wrong key should fail")
	}
}

// TestOpenSSLCompatible is the contract that lets the in-container helper be
// nothing but `openssl enc -d`. Skips when openssl is not on the test host.
func TestOpenSSLCompatible(t *testing.T) {
	ossl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	passFile := filepath.Join(dir, "pass")
	if err := os.WriteFile(passFile, []byte("the-master-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	blobFile := filepath.Join(dir, "blob")
	blob, _ := Encrypt("the-master-key", "s3cr3t-value")
	if err := os.WriteFile(blobFile, []byte(blob+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Exactly the command the container's `creds` helper runs.
	out, err := exec.Command(ossl, "enc", "-d", "-a", "-aes-256-cbc", "-pbkdf2",
		"-iter", "10000", "-md", "sha256", "-pass", "file:"+passFile, "-in", blobFile).Output()
	if err != nil {
		t.Fatalf("openssl could not decrypt our blob: %v", err)
	}
	if strings.TrimRight(string(out), "\n") != "s3cr3t-value" {
		t.Errorf("openssl decrypted to %q", out)
	}
}

func TestStoreLifecycle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Add("github-token", "ghp_example"); err != nil {
		t.Fatal(err)
	}
	if err := Add("db_password", "pw"); err != nil {
		t.Fatal(err)
	}
	names, _ := List()
	if len(names) != 2 || names[0] != "db_password" || names[1] != "github-token" {
		t.Fatalf("list = %v", names)
	}

	// The master key must NOT live in the exposed (mounted) dir.
	if _, err := os.Stat(filepath.Join(ExposedDir(), "key")); err == nil {
		t.Error("master key must not be in the exposed dir")
	}
	if _, err := os.Stat(keyPath()); err != nil {
		t.Error("master key should exist outside the exposed dir")
	}

	// Access is not granted by default; the passphrase is absent.
	if AccessGranted() {
		t.Error("access must be denied by default")
	}
	if err := AllowAccess(); err != nil {
		t.Fatal(err)
	}
	if !AccessGranted() {
		t.Error("allow-access should expose the passphrase")
	}
	// The exposed passphrase must actually decrypt a stored secret.
	pass, _ := os.ReadFile(passphrasePath())
	enc, _ := os.ReadFile(encPath("github-token"))
	if got, err := Decrypt(strings.TrimSpace(string(pass)), string(enc)); err != nil || got != "ghp_example" {
		t.Errorf("exposed passphrase does not decrypt: got %q err %v", got, err)
	}
	if err := RevokeAccess(); err != nil {
		t.Fatal(err)
	}
	if AccessGranted() {
		t.Error("revoke-access should remove the passphrase")
	}

	if err := Delete("db_password"); err != nil {
		t.Fatal(err)
	}
	if names, _ := List(); len(names) != 1 {
		t.Errorf("after delete, list = %v", names)
	}
}

func TestRotateKeyReEncryptsAndRevokes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Add("tok", "value-1"); err != nil {
		t.Fatal(err)
	}
	oldEnc, _ := os.ReadFile(encPath("tok"))
	_ = AllowAccess()

	n, err := RotateKey()
	if err != nil || n != 1 {
		t.Fatalf("rotate: n=%d err=%v", n, err)
	}
	// Ciphertext changed, the value did not, and any open grant is revoked.
	newEnc, _ := os.ReadFile(encPath("tok"))
	if string(oldEnc) == string(newEnc) {
		t.Error("rotation must re-encrypt")
	}
	if AccessGranted() {
		t.Error("rotation must revoke the stale passphrase")
	}
	key, _ := loadKey()
	if got, _ := Decrypt(key, string(newEnc)); got != "value-1" {
		t.Errorf("value not preserved across rotation: %q", got)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"github-token", "db_password", "api.key", "X1"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q should be valid: %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".hidden", "with space", "a/b", "x$y"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
