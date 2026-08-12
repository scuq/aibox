package container

// Ownership is labels, not names. Every resource aibox creates — containers,
// volumes, networks, images — carries these labels, and every lifecycle query
// filters on them. Cleanup never matches on names, so `aibox devcontainer
// remove` can never reach an unrelated container that happens to be called
// something similar. bclaude labelled volumes but not containers; that gap is
// exactly why VS Code ended up owning half the lifecycle.
const (
	// LabelManaged marks a resource as created by aibox. Value: "true".
	LabelManaged = "io.aibox.managed"
	// LabelSchema versions the label set itself, so a future aibox can tell
	// what an old resource's labels mean. Value: SchemaVersion.
	LabelSchema = "io.aibox.schema"
	// LabelProjectID is the 12-hex project identity (sha256 of the canonical
	// workspace path). See internal/project.
	LabelProjectID = "io.aibox.project.id"
	// LabelProjectPath is the canonical host path of the workspace. Recorded
	// for humans (`aibox volume list`); the ID is what queries use.
	LabelProjectPath = "io.aibox.project.path"
	// LabelRole is what the resource is for: workspace | proxy | network | volume.
	LabelRole = "io.aibox.role"
	// LabelVolumeKind further qualifies role=volume: auth | config | cache.
	LabelVolumeKind = "io.aibox.volume.kind"
	// LabelMode is the lifecycle class: standalone | workspace | devcontainer.
	LabelMode = "io.aibox.mode"
	// LabelAssistant is the assistant the resource belongs to: claude | codex | none.
	LabelAssistant = "io.aibox.assistant"
	// LabelRecipe is the 16-hex recipe hash stamped on images at build time and
	// checked — not merely recorded — at run time. See internal/image.
	LabelRecipe = "io.aibox.recipe"
)

// SchemaVersion is the current value of LabelSchema.
const SchemaVersion = "1"

const (
	RoleWorkspace = "workspace"
	RoleProxy     = "proxy"
	RoleNetwork   = "network"
	RoleVolume    = "volume"
)

const (
	VolumeAuth   = "auth"
	VolumeConfig = "config"
	VolumeCache  = "cache"
)

const (
	ModeStandalone   = "standalone"
	ModeWorkspace    = "workspace"
	ModeDevcontainer = "devcontainer"
)

// BaseLabels returns the labels common to every aibox resource.
func BaseLabels(role string) map[string]string {
	return map[string]string{
		LabelManaged: "true",
		LabelSchema:  SchemaVersion,
		LabelRole:    role,
	}
}
