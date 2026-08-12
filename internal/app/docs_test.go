package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scuq/aibox/internal/assistant"
	"github.com/scuq/aibox/internal/output"
)

func TestEnsureAssistantDocs(t *testing.T) {
	ws := t.TempDir()
	p := &output.Printer{W: os.Stderr}

	// Claude gets CLAUDE.md, Codex gets AGENTS.md, both referencing the notes.
	ensureAssistantDocs(p, ws, []assistant.Assistant{assistant.Claude{}, assistant.Codex{}})
	for _, f := range []string{"CLAUDE.md", "AGENTS.md"} {
		data, err := os.ReadFile(filepath.Join(ws, f))
		if err != nil {
			t.Fatalf("%s not created: %v", f, err)
		}
		if !strings.Contains(string(data), "/run/aibox/ainotes.md") {
			t.Errorf("%s does not reference the environment notes:\n%s", f, data)
		}
	}

	// Shell has no instruction file — nothing created for it.
	ws2 := t.TempDir()
	ensureAssistantDocs(p, ws2, []assistant.Assistant{assistant.Shell{}})
	if entries, _ := os.ReadDir(ws2); len(entries) != 0 {
		t.Errorf("shell should create no instruction file, found %v", entries)
	}
}

func TestEnsureAssistantDocsNeverOverwrites(t *testing.T) {
	ws := t.TempDir()
	p := &output.Printer{W: os.Stderr}
	existing := "# CLAUDE.md\n\nMy own project rules — do not touch.\n"
	path := filepath.Join(ws, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureAssistantDocs(p, ws, []assistant.Assistant{assistant.Claude{}})
	got, _ := os.ReadFile(path)
	if string(got) != existing {
		t.Errorf("an existing instruction file must be left untouched, got:\n%s", got)
	}
}
