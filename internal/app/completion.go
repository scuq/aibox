package app

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scuq/aibox/internal/output"
)

//go:embed completion.bash
var bashCompletion string

// cmdCompletion is the bash completion setup:
//
//	aibox completion bash      print the script
//	aibox completion install   write it where bash-completion loads it from
func cmdCompletion(p *output.Printer, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("completion needs a subcommand: bash | install")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
		return nil
	case "install":
		path, err := completionPath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(bashCompletion), 0o644); err != nil {
			return err
		}
		p.Good("wrote", "%s", path)
		p.Info("open a new shell to use it; this needs the bash-completion package")
		p.Info("without that package, add this line to ~/.bashrc: source %s", path)
		return nil
	default:
		return fmt.Errorf("unknown completion subcommand %q (bash | install)", args[0])
	}
}

// completionPath is the per-user directory bash-completion searches, resolved
// the same way bash-completion resolves it.
func completionPath() (string, error) {
	base := os.Getenv("BASH_COMPLETION_USER_DIR")
	if base == "" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			data = filepath.Join(home, ".local", "share")
		}
		base = filepath.Join(data, "bash-completion")
	}
	return filepath.Join(base, "completions", "aibox"), nil
}
