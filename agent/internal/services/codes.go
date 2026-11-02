package services

// Doctor codes this package emits or maps (agent.md sections 4.3.3 and 8, doctor-codes.md).
//
// They live in this one file on purpose: scripts/check-doctor-codes.sh forbids a "LAI-..." literal in
// any non-test Go file outside internal/doctor/codes.go once that file exists. AGENT-11 replaces these
// constants with the ones from internal/doctor; nothing else in this package spells a code.
const (
	// CodeLlamaNotRunning: the AI model service (localai-llama) is not running (critical).
	CodeLlamaNotRunning = "LAI-LLM-001"
	// CodeLlamaNotAnswering: llama is running but its /health does not answer.
	CodeLlamaNotAnswering = "LAI-LLM-004"
	// CodeLibraryNotRunning: the library service (kiwix) is not running, or runs and does not answer.
	CodeLibraryNotRunning = "LAI-SVC-002"
	// CodeSpeechNotRunning: the speech-recognition service (whisper) is not running, or runs and does
	// not answer.
	CodeSpeechNotRunning = "LAI-SVC-011"
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
