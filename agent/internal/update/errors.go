package update

import (
	"errors"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Error is a failed update step with its doctor code and a one-sentence message for the person who reads it
// (agent.md section 4.10; every user-visible error carries a code). Err is the cause for the log; it can name
// paths and is never part of Message.
type Error struct {
	// Code is a doctor code: LAI-AGT-010 (signature or checksum), LAI-AGT-009 (the update failed, nothing was
	// replaced or the old version was restored), LAI-AGT-023 (not allowed now, or no internet), LAI-AGT-021
	// (no such version), LAI-AGT-022 (an update is already pending), LAI-AGT-099 (internal).
	Code    string
	Message string
	// Step is the step that failed: resolve, download, signature, checksum, smoke, install, arm, restart.
	Step string
	Err  error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err != nil {
		return e.Code + ": " + e.Message + ": " + e.Err.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

func fail(code, step, msg string, cause error) *Error {
	return &Error{Code: code, Step: step, Message: msg, Err: cause}
}

// CodeOf returns the doctor code of err: the code of an *Error, LAI-AGT-099 for any other error, "" for nil.
func CodeOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return doctor.AGT099
}

// MessageOf returns the plain-language message of err.
func MessageOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "The update failed with an unexpected error; the agent log has the details."
}
