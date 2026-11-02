package router

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes of the preset generator (llama-router.md sections 4.2 and 8). Like the codes of
// codes.go they alias internal/doctor/codes.go, the one file that spells a "LAI-..." literal.
const (
	// CodePresetHandEdited: models.ini was edited by hand; a copy was saved and the file regenerated.
	CodePresetHandEdited Code = doctor.LLM006
	// CodePresetFailed: the preset file (or a part of it) could not be generated: a bad catalog
	// entry, an unsafe path, an unwritable directory.
	CodePresetFailed Code = doctor.LLM007
	// CodePresetNoDefault: no model qualifies as the default (section 4.6).
	CodePresetNoDefault Code = doctor.LLM014
	// CodePresetEnvConflict: two models need different values for the same environment variable and
	// node.yaml says nothing; the variable was left out of the drop-in.
	CodePresetEnvConflict Code = doctor.LLM015
	// CodePresetRestart: the drop-in changed; llama must be restarted to apply it (never automatic).
	CodePresetRestart Code = doctor.AGT016
)
