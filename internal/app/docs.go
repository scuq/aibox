package app

import (
	"os"
	"path/filepath"

	"github.com/scuq/aibox/internal/assistant"
	"github.com/scuq/aibox/internal/notes"
	"github.com/scuq/aibox/internal/output"
)

// ensureAssistantDocs creates the assistant's repo-root instruction file
// (CLAUDE.md for Claude, AGENTS.md for Codex) referencing the environment
// notes, when it does not already exist. Create-only, never edit: an existing
// file is the user's, and silently appending to it would be a surprise. Any
// write error is a warning, not fatal — a missing convenience file must not
// stop a session.
func ensureAssistantDocs(p *output.Printer, workspace string, assistants []assistant.Assistant) {
	for _, a := range assistants {
		name := a.InstructionFile()
		if name == "" {
			continue
		}
		path := filepath.Join(workspace, name)
		if _, err := os.Stat(path); err == nil {
			continue // exists — leave it alone
		}
		if err := os.WriteFile(path, []byte(notes.AssistantDocReference), 0o644); err != nil {
			p.Warn("could not create %s: %s", name, err)
			continue
		}
		p.Info("created %s referencing the aibox environment notes (edit it freely; aibox won't touch it again)", name)
	}
}
