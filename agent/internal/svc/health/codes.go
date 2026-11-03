package health

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package emits or maps (companion-services.md section 8). They are aliases of the
// constants of internal/doctor/codes.go, the one file allowed to spell a "LAI-..." literal
// (scripts/check-doctor-codes.sh).
const (
	// CodeLibraryNotRunning: kiwix is not running or does not answer.
	CodeLibraryNotRunning = doctor.SVC002
	// CodeSpeechNotRunning: whisper is not running or does not answer.
	CodeSpeechNotRunning = doctor.SVC011
	// CodeRestarted: the supervisor restarted a service (info).
	CodeRestarted = doctor.SVC050
	// CodeGaveUp: the restart budget is spent and the service was left stopped (error in survival
	// mode, warning otherwise).
	CodeGaveUp = doctor.SVC051
)

// ModeSurvival is the mode name in which CodeGaveUp is an error instead of a warning.
const ModeSurvival = "survival"
