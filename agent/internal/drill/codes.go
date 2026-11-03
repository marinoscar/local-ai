package drill

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package emits (drill.md section 8). They are aliases of the constants of
// internal/doctor/codes.go, the one file allowed to spell a "LAI-..." literal
// (scripts/check-doctor-codes.sh).
const (
	// CodeSyncActive: the drill waited because an ark sync, verify or import run is active (or because
	// another drill is running).
	CodeSyncActive = doctor.DRL002
	// CodeSurvival: the drill does not run in survival mode.
	CodeSurvival = doctor.DRL003
	// CodeBusy: the drill waited because the AI was in use.
	CodeBusy = doctor.DRL004
	// CodeBattery: the drill skipped because the battery is below 50 %.
	CodeBattery = doctor.DRL005
	// CodeRestoreFailed: the drill could not restore the normal brain.
	CodeRestoreFailed = doctor.DRL090
	// CodeFenceDial: something tried to use the internet during the drill.
	CodeFenceDial = doctor.DRL091

	// CodeInternal: a bug in the engine itself (it ended a run).
	CodeInternal = doctor.AGT099
)
