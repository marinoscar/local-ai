package cli

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/api"
)

// Exit codes (aictl.md section 3.4). Commands use these constants; nothing else spells a number.
const (
	// ExitOK: success (warnings allowed).
	ExitOK = 0
	// ExitFailed: the operation failed (upstream error, rollback, internal error, undecodable answer).
	ExitFailed = 1
	// ExitUsage: usage error or invalid input.
	ExitUsage = 2
	// ExitUnreachable: the agent is not reachable, or still starting after the retry window. Only that.
	ExitUnreachable = 3
	// ExitDenied: not allowed (not root, not an operator, PIN required, wrong or locked).
	ExitDenied = 4
	// ExitNotFound: model, service, content or drive not found.
	ExitNotFound = 5
	// ExitRefused: refused right now (busy, wrong mode, does not fit, rate limited).
	ExitRefused = 6
	// ExitTimeout: timed out (agent 504 or the client deadline).
	ExitTimeout = 7
	// ExitWarnings: finished with warnings (doctor, drill, ark runs). Commands return it with ExitWith.
	ExitWarnings = 8
	// ExitErrors: finished with errors or critical problems. Commands return it with ExitWith.
	ExitErrors = 9
	// ExitInterrupted: interrupted by Ctrl-C.
	ExitInterrupted = 130
)

// ExitForStatus maps an HTTP status from the agent to an exit code (aictl.md section 3.4, "From
// HTTP"). Statuses the table does not list fall back by class: other 4xx are invalid requests (2),
// everything else that is not 2xx is a failure (1).
func ExitForStatus(status int) int {
	switch {
	case status >= 200 && status < 300:
		return ExitOK
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ExitDenied
	case status == http.StatusNotFound:
		return ExitNotFound
	case status == http.StatusConflict, status == http.StatusUnprocessableEntity, status == http.StatusTooManyRequests:
		return ExitRefused
	case status == http.StatusServiceUnavailable:
		return ExitUnreachable
	case status == http.StatusGatewayTimeout:
		return ExitTimeout
	case status == http.StatusInternalServerError, status == http.StatusBadGateway:
		return ExitFailed
	case status >= 400 && status < 500:
		return ExitUsage
	}
	return ExitFailed
}

// ExitFor maps any error a command returns to its exit code. It is a pure function so that every
// command exits the same way: nil is 0; an *ExitError, *CLIError (its own code) or *APIError (its
// HTTP status) says what it means; an expired deadline is 7; Ctrl-C is 130; a broken pipe on stdout
// is 0; anything else is an internal failure (1).
func ExitFor(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Exit
	}
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.Code == api.CodeRateLimited {
			// A PIN lockout is "not allowed: PIN locked" (4), not the generic "refused right now" (6)
			// that other 429 answers get (aictl.md section 3.4).
			return ExitDenied
		}
		return ExitForStatus(ae.Status)
	}
	switch {
	case errors.Is(err, flag.ErrHelp), errors.Is(err, syscall.EPIPE):
		return ExitOK
	case errors.Is(err, context.DeadlineExceeded):
		return ExitTimeout
	case errors.Is(err, context.Canceled):
		return ExitInterrupted
	}
	return ExitFailed
}
