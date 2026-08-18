package app

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scuq/aibox/internal/output"
	"github.com/scuq/aibox/internal/secret"
	"golang.org/x/term"
)

// cmdSecret is the host-side control for the encrypted /creds store:
//
//	aibox secret add NAME            encrypt a value (read from a prompt/stdin)
//	aibox secret list                the names (never the values)
//	aibox secret delete NAME
//	aibox secret allow-access [--ttl 10m]   temporarily expose the passphrase
//	aibox secret revoke-access       remove it now
//	aibox secret rotate-key          new key, re-encrypt all (after a leak)
func cmdSecret(ctx context.Context, p *output.Printer, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "add":
		if len(args) != 1 {
			return fmt.Errorf("usage: aibox secret add NAME  (the value is read from a prompt or stdin, never the command line)")
		}
		name := args[0]
		if err := secret.ValidName(name); err != nil {
			return err
		}
		value, err := readSecretValue(p, name)
		if err != nil {
			return err
		}
		if value == "" {
			return fmt.Errorf("empty value — nothing stored")
		}
		if err := secret.Add(name, value); err != nil {
			return err
		}
		p.Good("stored", "%s (encrypted, read-only in the container at /creds/%s.enc)", name, name)
		p.Info("the value stays encrypted until you run 'aibox secret allow-access'")
		return nil

	case "list":
		fs := flag.NewFlagSet("secret list", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args); err != nil {
			return err
		}
		names, err := secret.List()
		if err != nil {
			return err
		}
		if *jsonOut {
			return output.JSON(struct {
				Secrets []string `json:"secrets"`
				Access  bool     `json:"accessGranted"`
			}{names, secret.AccessGranted()})
		}
		if len(names) == 0 {
			p.Info("no secrets stored")
			return nil
		}
		for _, n := range names {
			fmt.Printf("  %s\n", n)
		}
		if secret.AccessGranted() {
			p.Warn("access is currently GRANTED — the container can decrypt these right now")
		}
		return nil

	case "delete", "rm":
		if len(args) != 1 {
			return fmt.Errorf("usage: aibox secret delete NAME")
		}
		if err := secret.Delete(args[0]); err != nil {
			return err
		}
		p.Good("deleted", "%s", args[0])
		return nil

	case "allow-access":
		fs := flag.NewFlagSet("secret allow-access", flag.ContinueOnError)
		ttl := fs.Duration("ttl", 10*time.Minute, "how long access stays open (0 = until Ctrl-C)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		return allowAccess(ctx, p, *ttl)

	case "revoke-access":
		if err := secret.RevokeAccess(); err != nil {
			return err
		}
		p.Good("revoked", "the passphrase is no longer exposed")
		return nil

	case "rotate-key":
		p.Warn("rotating the master key re-encrypts every secret and revokes any open access")
		n, err := secret.RotateKey()
		if err != nil {
			return err
		}
		p.Good("rotated", "new master key; re-encrypted %d secret(s)", n)
		p.Info("any value read before now must be considered stale — update anything that stored it")
		return nil

	default:
		return fmt.Errorf("unknown secret subcommand %q (add | list | delete | allow-access | revoke-access | rotate-key)", sub)
	}
}

// readSecretValue takes the value off a hidden prompt when attached to a
// terminal, or the first line of stdin when piped — never a command-line
// argument, which would land in shell history and the process table.
func readSecretValue(p *output.Printer, name string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "value for %s (input hidden): ", name)
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return strings.TrimRight(string(b), "\r\n"), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	// A value piped without a trailing newline yields the data plus io.EOF;
	// that is success, not failure.
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// allowAccess exposes the passphrase, then holds it open for ttl (or until
// interrupted) and revokes on exit. Foreground and blocking on purpose: the
// grant lives exactly as long as the user keeps this command running, and a
// crash or Ctrl-C closes it.
func allowAccess(ctx context.Context, p *output.Printer, ttl time.Duration) error {
	names, _ := secret.List()
	if len(names) == 0 {
		return fmt.Errorf("no secrets stored — nothing to grant access to")
	}
	if err := secret.AllowAccess(); err != nil {
		return err
	}
	p.Warn("ACCESS GRANTED: the container can now decrypt secrets via /creds/%s", secret.PassphraseName)
	p.Warn("secrets must never be printed, logged, committed, written to a test, or sent in cleartext")
	if ttl > 0 {
		p.Info("access closes automatically in %s — or press Ctrl-C to close it now", ttl)
	} else {
		p.Info("access stays open until you press Ctrl-C")
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var timeout <-chan time.Time
	if ttl > 0 {
		t := time.NewTimer(ttl)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-sigCtx.Done():
	case <-timeout:
	}
	if err := secret.RevokeAccess(); err != nil {
		return err
	}
	p.Good("revoked", "access closed; the passphrase is no longer exposed")
	return nil
}
