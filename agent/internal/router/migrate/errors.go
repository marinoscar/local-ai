package migrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Codes the migration raises. Each is in the registry (doctor-codes.md); none is new.
const (
	// CodeStopped: the migration stopped before the old service was touched (model not in the catalog,
	// settings that differ, a check that failed).
	CodeStopped = doctor.LLM017
	// CodeInterrupted: the migration did not finish (an interruption, a failure after the first change, or
	// a verification that failed and was rolled back); running it again continues or retries.
	CodeInterrupted = doctor.LLM029
	// CodeCredShape: the old key file is not 64 lowercase hex digits.
	CodeCredShape = doctor.SEC010
	// CodeBuild is the code of a failed build when the build manager gave none.
	CodeBuild = doctor.LLM018
	// CodeOldRunning and CodeOldCredPath are the doctor findings of checks.go.
	CodeOldRunning  = doctor.LLM016
	CodeOldCredPath = doctor.SEC015
)

// Error is a failure of the migration, with the doctor code every visible error carries. Its message and
// details never contain the API key (only its fingerprint).
type Error struct {
	Code string
	// Step is the step that failed ("M3", "R5", ...).
	Step    string
	Message string
	Details map[string]any
	Err     error
	// RolledBack is true when the failure made the migration roll back and the old service serves again.
	RolledBack bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Code)
	if e.Step != "" {
		b.WriteString(" (" + e.Step + ")")
	}
	b.WriteString(": " + e.Message)
	if e.Err != nil {
		b.WriteString(": " + e.Err.Error())
	}
	return b.String()
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the doctor code of err, "" when it has none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}

func fail(code, step, format string, a ...any) *Error {
	return &Error{Code: code, Step: step, Message: fmt.Sprintf(format, a...)}
}

func wrap(code, step string, err error, format string, a ...any) *Error {
	return &Error{Code: code, Step: step, Message: fmt.Sprintf(format, a...), Err: err}
}

// Problem is one reason the preflight refuses to go on. A dry run prints all of them.
type Problem struct {
	Code string `json:"code"`
	// Cause is a short machine word ("unmatched_file", "flags_differ", "port", ...).
	Cause   string         `json:"cause"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (p Problem) String() string { return p.Code + ": " + p.Message }

// problemsError turns the preflight's problems into one Error: the first problem's code, the others in
// the details.
func problemsError(ps []Problem) *Error {
	if len(ps) == 0 {
		return nil
	}
	e := &Error{Code: ps[0].Code, Step: "M0", Message: ps[0].Message, Details: map[string]any{"cause": ps[0].Cause}}
	if len(ps) > 1 {
		var more []string
		for _, p := range ps[1:] {
			more = append(more, p.String())
		}
		e.Details["also"] = more
	}
	for k, v := range ps[0].Details {
		e.Details[k] = v
	}
	return e
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
