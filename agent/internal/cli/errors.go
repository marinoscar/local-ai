package cli

import (
	"fmt"
	"strconv"
)

// APIError is the agent's error envelope (agent.md section 3.7) decoded, plus the HTTP status and
// the response headers aictl cares about. Message and Fix are in the language the CLI asked for
// (Accept-Language). ExitFor maps it by Status.
type APIError struct {
	Status    int
	Code      string
	Message   string
	Fix       string
	Details   map[string]any
	RequestID string // X-Request-Id of the response
	OpID      string // X-LocalAI-Op-Id, when the failed request had created an operation
}

// Error implements error. The text is for logs and tests; human output goes through the problem
// layout, which never prints an HTTP status.
func (e *APIError) Error() string {
	return e.Code + ": " + e.Message + " (HTTP " + strconv.Itoa(e.Status) + ")"
}

// Detail returns details[key], or nil.
func (e *APIError) Detail(key string) any { return e.Details[key] }

// CLIError is an error aictl raises itself, with one of its own doctor codes. It carries its exit
// code. Create it with NewCLIError, or through Call.Err inside a command.
type CLIError struct {
	Code      string
	Message   string
	Fix       string
	Exit      int
	Details   map[string]any
	RequestID string
	Cause     error
}

// NewCLIError builds a CLIError for code in lang. A non-empty message replaces the code's standard
// sentence (for example the name of the unknown option); the fix stays the code's.
func NewCLIError(lang Lang, code, message string) *CLIError {
	info, ok := codeTable[code]
	if !ok {
		return &CLIError{Code: code, Message: message, Exit: ExitFailed}
	}
	t := info.text(lang)
	if message == "" {
		message = t.Message
	}
	return &CLIError{Code: code, Message: message, Fix: t.Fix, Exit: info.Exit}
}

// WithCause attaches the underlying error (never printed in human mode, kept for -v and errors.Is).
func (e *CLIError) WithCause(err error) *CLIError { e.Cause = err; return e }

// WithDetails merges key/value pairs into the details object of the envelope.
func (e *CLIError) WithDetails(kv ...any) *CLIError {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			e.Details[k] = kv[i+1]
		}
	}
	return e
}

// Error implements error.
func (e *CLIError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + ": " + e.Cause.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *CLIError) Unwrap() error { return e.Cause }

// ExitError ends the command with an exit code and prints nothing: the command already printed its
// result. Commands use it for the "finished with warnings" (8) and "finished with errors" (9) codes
// of aictl.md section 3.4.
type ExitError struct{ Code int }

// ExitWith returns an *ExitError for code.
func ExitWith(code int) error { return &ExitError{Code: code} }

// Error implements error.
func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }
