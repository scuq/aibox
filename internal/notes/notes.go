// Package notes owns the environment briefing (.ainotes): the run-time policy
// layer, the size budget, and the host-side helpers. The image layer is
// generated at image build time (assets/ainotes/generate-image-notes.sh) from
// the image manifest, so versions in the notes cannot drift from versions in
// the image; the layers are concatenated by the entrypoint at container start.
package notes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/scuq/aibox/internal/config"
)

// PolicyLayer renders this session's actual state — mirroring what the
// entrypoint derives from AIBOX_EGRESS_MODE / AIBOX_GIT_HISTORY / the services
// inventory — for the host-side `aibox notes`. The network *model* lives in
// the image layer; this is only status.
func PolicyLayer(cfg config.Config) string {
	var b strings.Builder
	b.WriteString("\n## This session\n")
	if cfg.Egress.Mode == "proxy" {
		b.WriteString("Egress: allowlisted proxy (the Network section above applies).\n")
	} else {
		b.WriteString("Egress: OPEN/unfiltered — the Network allowlist guidance above does\nnot apply this session; outbound requests are not proxied.\n")
	}
	if cfg.Git.History == "none" {
		b.WriteString("Git history: unavailable this session (history: none).\n")
	}
	if len(cfg.Services) > 0 {
		b.WriteString("Relay services (reach by name, the port is fixed per service):\n")
		for _, s := range cfg.Services {
			b.WriteString("  " + s.Name)
			if len(s.Aliases) > 0 {
				b.WriteString(" (" + strings.Join(s.Aliases, ", ") + ")")
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("Relay services: none configured this session.\n")
	}
	return b.String()
}

// ProjectLayer reads the repo's own additions, applied last so a repo can add
// conventions but not delete the git policy.
func ProjectLayer(cfg config.Config, workspace string) string {
	if cfg.Notes.Project == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workspace, cfg.Notes.Project))
	if err != nil {
		return ""
	}
	return "\n" + string(data)
}

// CheckBudget enforces the hard cap. The notes land in the agent's context
// every single session — a recurring token cost, not free documentation.
func CheckBudget(content string, maxBytes int) error {
	if len(content) > maxBytes {
		return fmt.Errorf("notes are %d bytes, over the %d-byte budget — trim them", len(content), maxBytes)
	}
	return nil
}

// ClaudeMDSnippet is the CLAUDE.md include. Conditional in wording and
// degrading to a no-op, because a committed CLAUDE.md referencing an
// aibox-only path is a dangling reference when the repo is opened elsewhere.
const ClaudeMDSnippet = `## Environment
If ` + "`/run/aibox/ainotes.md`" + ` exists, read it before starting work — it describes
the available tools, the network policy, and the repository write constraints
for this container. Outside that container, ignore this section.
`

// AssistantDocReference is written into a freshly created CLAUDE.md / AGENTS.md
// when the repo has none. It only points the assistant at the environment
// notes and degrades to a no-op outside the container (same wording as the
// CLAUDE.md snippet) — deliberately minimal so it never overwrites or presumes
// on a project's real instructions; aibox only ever *creates* it, never edits
// an existing one.
const AssistantDocReference = "<!-- Created by aibox. Add your project's own instructions above or below;" +
	" aibox will not touch this file again once it exists. -->\n\n" + ClaudeMDSnippet

// ProjectScaffold is the starter content for .aibox/ainotes.md.
const ProjectScaffold = `<!-- Project layer of the aibox environment notes. Committed with the repo,
     appended after the image and policy layers. Keep it short: the whole
     notes file has a 2 KB budget. -->

## Project conventions
`
