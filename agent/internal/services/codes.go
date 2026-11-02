package services

import "github.com/marinoscar/local-ai/agent/internal/doctor"

// Doctor codes this package emits or maps (agent.md sections 4.3.3 and 8, doctor-codes.md).
//
// They are aliases of the constants of internal/doctor/codes.go, the one file allowed to spell a
// "LAI-..." literal (scripts/check-doctor-codes.sh).
const (
	// CodeLlamaNotRunning: the AI model service (localai-llama) is not running (critical).
	CodeLlamaNotRunning = doctor.LLM001
	// CodeLlamaNotAnswering: llama is running but its /health does not answer.
	CodeLlamaNotAnswering = doctor.LLM004
	// CodeLibraryNotRunning: the library service (kiwix) is not running, or runs and does not answer.
	CodeLibraryNotRunning = doctor.SVC002
	// CodeSpeechNotRunning: the speech-recognition service (whisper) is not running, or runs and does
	// not answer.
	CodeSpeechNotRunning = doctor.SVC011
)

// NotAnsweringCode is the doctor code raised when the service is active but does not answer its
// health check for more than a minute: LAI-LLM-004 for llama, and the service's "isn't running" code
// for kiwix and whisper (the companion-services.md section 8 code table has no finer one).
func NotAnsweringCode(service string) string {
	switch service {
	case "llama":
		return CodeLlamaNotAnswering
	case "kiwix":
		return CodeLibraryNotRunning
	case "whisper":
		return CodeSpeechNotRunning
	}
	return ""
}

// NotRunningCode is the doctor code of a start or restart that left the service not running.
func NotRunningCode(service string) string {
	switch service {
	case "llama":
		return CodeLlamaNotRunning
	case "kiwix":
		return CodeLibraryNotRunning
	case "whisper":
		return CodeSpeechNotRunning
	}
	return ""
}
