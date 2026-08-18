// Package secret is a simple, host-side encrypted credential store whose
// ciphertext is mounted read-only into the container at /creds, while the
// passphrase that decrypts it is exposed only during an explicit, temporary
// `aibox secret allow-access`. The security property is human-in-the-loop
// access: an autonomous agent has the encrypted blobs at all times but cannot
// read a value unless a human deliberately grants it, and only for as long as
// they hold the grant open.
//
// The encryption is deliberately `openssl enc`-compatible (AES-256-CBC,
// PBKDF2-HMAC-SHA256, the "Salted__" header, base64) so the in-container helper
// is nothing more exotic than the openssl already in the image, and so the
// format is auditable with a one-line command.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pbkdf2Iter matches `openssl enc -pbkdf2`'s default iteration count, so the
// in-container `openssl enc -d -pbkdf2` decrypts our output without surprises.
const pbkdf2Iter = 10000

// Dir is the host secret store, alongside the egress config.
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "aibox", "secrets")
}

// keyPath is the master passphrase. It is NEVER mounted into a container — only
// ExposedDir is — so the container sees the key only via the passphrase file
// that allow-access drops into ExposedDir for the duration of a grant.
func keyPath() string { return filepath.Join(Dir(), "key") }

// ExposedDir holds the encrypted secrets and (only while access is granted) the
// passphrase. This is the directory bind-mounted read-only at /creds.
func ExposedDir() string { return filepath.Join(Dir(), "exposed") }

// PassphraseName is the file inside ExposedDir that, when present, lets the
// container decrypt. Its presence is the grant.
const PassphraseName = "passphrase"

func passphrasePath() string { return filepath.Join(ExposedDir(), PassphraseName) }
func encPath(name string) string {
	return filepath.Join(ExposedDir(), name+".enc")
}

// ValidName guards secret names: they become file names and shell tokens, so
// keep them to a safe, predictable set.
func ValidName(name string) error {
	if name == "" {
		return errors.New("secret name is empty")
	}
	for _, r := range name {
		if !(r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("secret name %q has an invalid character %q (use a-z A-Z 0-9 - _ .)", name, r)
		}
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("secret name %q may not start with a dot", name)
	}
	return nil
}

// EnsureKey returns the master passphrase, generating a long random one on
// first use. 32 random bytes as hex — a 256-bit key, well beyond a UUID.
func EnsureKey() (string, error) {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	data, err := os.ReadFile(keyPath())
	if err == nil {
		key := strings.TrimRight(string(data), "\n")
		if key != "" {
			return key, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	key := hex.EncodeToString(buf)
	// 0600, and never mounted into the container.
	if err := os.WriteFile(keyPath(), []byte(key), 0o600); err != nil {
		return "", err
	}
	return key, nil
}

func loadKey() (string, error) {
	data, err := os.ReadFile(keyPath())
	if err != nil {
		return "", fmt.Errorf("no secret store yet (add one with 'aibox secret add')")
	}
	return strings.TrimRight(string(data), "\n"), nil
}

// Add encrypts value under name and writes it into ExposedDir. Overwriting an
// existing name is how a single secret is rotated.
func Add(name, value string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	key, err := EnsureKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ExposedDir(), 0o700); err != nil {
		return err
	}
	blob, err := Encrypt(key, value)
	if err != nil {
		return err
	}
	return os.WriteFile(encPath(name), []byte(blob+"\n"), 0o600)
}

// List returns the stored secret names, sorted. Never their values.
func List() ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(ExposedDir(), "*.enc"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), ".enc"))
	}
	sort.Strings(names)
	return names, nil
}

// Delete removes a secret.
func Delete(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if err := os.Remove(encPath(name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no secret named %q", name)
		}
		return err
	}
	return nil
}

// AccessGranted reports whether the passphrase is currently exposed.
func AccessGranted() bool {
	_, err := os.Stat(passphrasePath())
	return err == nil
}

// AllowAccess drops the passphrase into ExposedDir so the container can decrypt.
// The mount is live (a bind mount, not a copy), so this takes effect in a
// running container immediately, and RevokeAccess removes it again.
func AllowAccess() error {
	key, err := loadKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ExposedDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(passphrasePath(), []byte(key), 0o600)
}

// RevokeAccess removes the passphrase. Idempotent.
func RevokeAccess() error {
	if err := os.Remove(passphrasePath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RotateKey generates a new master passphrase and re-encrypts every secret
// under it, then revokes any open grant (the old passphrase no longer works).
// This is the response to a leaked secret: rotate, and anything that saw the
// old value is now stale.
func RotateKey() (int, error) {
	oldKey, err := loadKey()
	if err != nil {
		return 0, err
	}
	names, err := List()
	if err != nil {
		return 0, err
	}
	// Decrypt everything with the old key first; abort before writing anything
	// if any secret cannot be read, so a half-rotated store never happens.
	plain := make(map[string]string, len(names))
	for _, n := range names {
		blob, err := os.ReadFile(encPath(n))
		if err != nil {
			return 0, err
		}
		v, err := Decrypt(oldKey, strings.TrimSpace(string(blob)))
		if err != nil {
			return 0, fmt.Errorf("cannot decrypt %q during rotation: %w", n, err)
		}
		plain[n] = v
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return 0, err
	}
	newKey := hex.EncodeToString(buf)
	for n, v := range plain {
		blob, err := Encrypt(newKey, v)
		if err != nil {
			return 0, err
		}
		if err := os.WriteFile(encPath(n), []byte(blob+"\n"), 0o600); err != nil {
			return 0, err
		}
	}
	if err := os.WriteFile(keyPath(), []byte(newKey), 0o600); err != nil {
		return 0, err
	}
	// The exposed passphrase, if any, is now the old key — remove it.
	_ = RevokeAccess()
	return len(names), nil
}

// Encrypt produces `openssl enc -aes-256-cbc -pbkdf2 -a`-compatible output:
// base64( "Salted__" + salt + AES-256-CBC(PKCS7(plaintext)) ), the key and IV
// derived from pass and salt via PBKDF2-HMAC-SHA256.
func Encrypt(pass, plaintext string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	dk := pbkdf2SHA256([]byte(pass), salt, pbkdf2Iter, 48)
	block, err := aes.NewCipher(dk[:32])
	if err != nil {
		return "", err
	}
	pt := pkcs7Pad([]byte(plaintext), block.BlockSize())
	ct := make([]byte, len(pt))
	cipher.NewCBCEncrypter(block, dk[32:48]).CryptBlocks(ct, pt)
	blob := append([]byte("Salted__"), salt...)
	blob = append(blob, ct...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

// Decrypt reverses Encrypt (used in rotation and tests; the container uses
// openssl for the same job).
func Decrypt(pass, b64 string) (string, error) {
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	if len(blob) < 16 || string(blob[:8]) != "Salted__" {
		return "", errors.New("not a Salted__ blob")
	}
	salt, ct := blob[8:16], blob[16:]
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return "", errors.New("ciphertext length invalid")
	}
	dk := pbkdf2SHA256([]byte(pass), salt, pbkdf2Iter, 48)
	block, err := aes.NewCipher(dk[:32])
	if err != nil {
		return "", err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, dk[32:48]).CryptBlocks(pt, ct)
	return pkcs7Unpad(pt)
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("empty plaintext")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(data) {
		return "", errors.New("bad padding")
	}
	// Constant-time-ish check that all pad bytes are equal.
	ref := make([]byte, pad)
	for i := range ref {
		ref[i] = byte(pad)
	}
	if subtle.ConstantTimeCompare(data[len(data)-pad:], ref) != 1 {
		return "", errors.New("bad padding")
	}
	return string(data[:len(data)-pad]), nil
}

// pbkdf2SHA256 is PBKDF2-HMAC-SHA256, implemented inline to avoid a dependency.
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	hLen := sha256.Size
	numBlocks := (keyLen + hLen - 1) / hLen
	var dk []byte
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		mac.Write(buf)
		u := mac.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}
