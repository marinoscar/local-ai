package modes

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package raises or logs (agent.md sections 4.2 and 8, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh).
const (
	// CodeStateUnwritable: the state store cannot write, so the new mode lives in memory only.
	CodeStateUnwritable = doctor.AGT004
	// CodeResumed: a mode change was interrupted and has been resumed (info, logged).
	CodeResumed = doctor.AGT007
	// CodeStepFailed: one step of a mode change failed or was skipped (warning).
	CodeStepFailed = doctor.AGT008
	// CodeTailscaleLogin: Tailscale is not logged in (returned by the TailscaleController).
	CodeTailscaleLogin = doctor.NET003
	// CodeTailscaleServe: Tailscale Serve could not be changed.
	CodeTailscaleServe = doctor.NET004
	// CodeTailscaleStillServing: this node is still published on the tailnet (returned by the
	// TailscaleController when a removal did not stick; the engine retries like for CodeTailscaleServe).
	CodeTailscaleStillServing = doctor.NET006
	// CodeModelFallback: the preferred survival model did not start, or was skipped after a crash.
	CodeModelFallback = doctor.LLM020
	// CodeNoModel: in survival mode no model could be started (critical).
	CodeNoModel = doctor.LLM021
)
