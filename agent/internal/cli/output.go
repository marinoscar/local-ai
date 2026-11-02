package cli

import (
	"errors"
	"syscall"

	"github.com/marinoscar/local-ai/agent/internal/cli/out"
)

// This file connects the framework to the output layer (package out, aictl.md sections 3.3 and
// 3.5): it turns the errors a command returns into out.Problem values and decides what to show when
// a command ends.

// problemOf describes err. An unexpected error (not an *APIError or *CLIError) shows its text with
// no code; commands should return typed errors.
func problemOf(err error) out.Problem {
	var ae *APIError
	if errors.As(err, &ae) {
		return out.Problem{Severity: sevError, Code: ae.Code, Message: ae.Message, Fix: ae.Fix, Details: ae.Details,
			RequestID: ae.RequestID, RetryAfter: ae.RetryAfter, ShowRequestID: ae.Code == CodeBadResponse}
	}
	var ce *CLIError
	if errors.As(err, &ce) {
		sev := sevError
		if info, ok := codeTable[ce.Code]; ok {
			sev = info.Severity
		}
		return out.Problem{Severity: sev, Code: ce.Code, Message: ce.Message, Fix: ce.Fix, Details: ce.Details,
			RequestID: ce.RequestID, ShowRequestID: ce.Code == CodeBadResponse}
	}
	return out.Problem{Severity: sevError, Message: err.Error()}
}

// reporter prints what the framework itself reports (errors a command returned, queued warnings,
// usage mistakes) through the Printer.
type reporter struct {
	p   *out.Printer
	g   Global
	env Env
}

// newPrinter builds the Printer of one run from the global flags and the environment: colour by
// out.ColorEnabled (already folded into g.NoColor), symbols by the locale.
func newPrinter(env Env, g Global) *out.Printer {
	return out.New(out.Options{
		Stdout: env.Stdout, Stderr: env.Stderr, Lang: g.Lang,
		Color: !g.NoColor, UTF8: out.UTF8Enabled(env.Getenv),
		Quiet: g.Quiet, Verbose: g.Verbose, JSON: g.JSON,
	})
}

// fail reports err and returns its exit code. An *ExitError and a broken pipe print nothing. With
// --json the envelope goes to stdout and stderr stays empty; if the command already wrote its
// document, stdout keeps that one document and only the exit code reports the failure.
func (r reporter) fail(err error) int {
	code := ExitFor(err)
	var ee *ExitError
	if errors.As(err, &ee) || errors.Is(err, syscall.EPIPE) {
		return code
	}
	if r.g.JSON {
		// Whatever its severity, a failure in --json mode is the envelope on stdout.
		_ = r.p.JSON().Error(problemOf(err))
		return code
	}
	r.p.Problem(problemOf(err))
	return code
}

// warn prints queued warnings to stderr after a command, unless --quiet, or --json with a failed
// command.
func (r reporter) warn(ws []*CLIError, failed bool) {
	if r.g.Quiet || (r.g.JSON && failed) {
		return
	}
	for _, w := range ws {
		r.p.Problem(problemOf(w))
	}
}
