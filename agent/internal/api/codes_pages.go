package api

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes of the pages and proxies of agent.md section 3.11 (AGENT-16). They are aliases of
// the constants of internal/doctor/codes.go, which also holds their English and Spanish texts: the
// JSON envelope and the HTML page of a proxy answer both read them there.
const (
	// CodePortalMissing: the embedded screens (ui/dist) are missing from the program (bad build).
	CodePortalMissing = doctor.SVC100
	// CodeChatDown: the llama server does not answer behind /chat/ (502).
	CodeChatDown = doctor.SVC102
	// CodeLibraryDown: kiwix-serve does not answer behind /library/ (503).
	CodeLibraryDown = doctor.SVC002
)
