package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/marinoscar/local-ai/agent/internal/cli/i18n"
)

// maxPINPrompts is how many times one command asks for the PIN after wrong answers.
const maxPINPrompts = 3

// Admin runs fn, an [admin] API call (or several), and handles the admin PIN around it (aictl.md
// section 3.2.3):
//
//  1. fn runs. For root over the socket, or a request that already carries a valid grant, the agent
//     answers normally and Admin returns fn's result: no prompt is ever shown.
//  2. If fn fails with 403 and details.pin_required (an operator without a grant), Admin asks for
//     the PIN, calls POST /pin/verify, keeps the grant in the client and runs fn once more. Every
//     request of the command from then on carries X-LocalAI-Pin-Grant. That retry's result,
//     whatever it is, is returned: it is retried once, never in a loop.
//
// Where the PIN comes from, in order: --pin-stdin reads one line from stdin (a pipe, or a terminal
// without echo); otherwise a terminal prompt "Admin PIN: " without echo. With --json, or when stdin
// is not a terminal, and without --pin-stdin there is nobody to ask: LAI-CLI-008, exit 4. The PIN
// is never taken from a flag or the environment, never printed, never logged (not even with
// LOCALAI_CLI_DEBUG) and never kept after it is sent; the grant lives in memory for this command.
//
// A wrong PIN (LAI-SEC-003) is shown with the tries left and asked again, up to three prompts in
// total; with --pin-stdin on a pipe or with --json there is one try. After the last wrong PIN the
// command ends with the agent's LAI-SEC-003 error (exit 4). A lockout (LAI-SEC-004) ends it at once
// with the wait time from Retry-After shown (exit 4, not the generic rate-limit 6). Ctrl-C at the
// prompt is exit 130.
//
// fn must be safe to run twice: an admin request that the agent refuses with 403 has had no effect.
func (c *Call) Admin(fn func() error) error {
	err := fn()
	if !needsPIN(err) {
		return err
	}
	if gerr := c.obtainGrant(); gerr != nil {
		return gerr
	}
	return fn()
}

// needsPIN reports whether err is the agent asking for the admin PIN.
func needsPIN(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden {
		return false
	}
	v, _ := ae.Details["pin_required"].(bool)
	return v
}

// obtainGrant asks for the PIN until the agent accepts it or the tries are used up, and stores the
// grant in the client.
func (c *Call) obtainGrant() error {
	interactive := !c.Global.JSON && c.Term.Interactive()
	if !c.Global.PinStdin && !interactive {
		return c.Err(CodeNeedPIN, "")
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	tries := 1
	if interactive {
		tries = maxPINPrompts
	}
	for attempt := 1; ; attempt++ {
		pin, err := c.readPIN(interactive)
		if err != nil {
			return err
		}
		grant, verr := c.verifyPIN(cl, pin)
		if verr == nil {
			cl.SetPinGrant(grant)
			return nil
		}
		var ae *APIError
		if !errors.As(verr, &ae) || ae.Code != CodeWrongPIN || attempt >= tries {
			return verr
		}
		// Wrong PIN with tries left: say so, and ask again.
		p := problemOf(verr)
		p.Severity = sevWarning
		c.Out.Problem(p)
		if n, ok := remainingAttempts(ae); ok {
			c.Out.Warn(i18n.T(c.Lang, i18n.KeyTriesLeft, n))
		}
	}
}

// remainingAttempts reads details.remaining_attempts of a wrong-PIN answer.
func remainingAttempts(ae *APIError) (int, bool) {
	switch v := ae.Details["remaining_attempts"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	}
	return 0, false
}

// readPIN reads one PIN. On a terminal it prompts on stderr and reads without echo; otherwise it
// reads one line from stdin and prints nothing. The PIN is returned to the caller and goes nowhere
// else: no log, no error, no argv.
func (c *Call) readPIN(prompt bool) (string, error) {
	line, err := c.readSecret(i18n.T(c.Lang, i18n.KeyPromptPIN), prompt)
	if err != nil {
		return "", err
	}
	if line == "" { // Enter on nothing, or the input ended
		return "", c.Err(CodeNeedPIN, i18n.T(c.Lang, i18n.KeyNoPIN))
	}
	return line, nil
}

// readSecret reads one secret line. When show is set it writes prompt to stderr first and ends the
// prompt line itself afterwards (echo was off). On a terminal the read is without echo; otherwise it
// is one line of stdin. Ctrl-C is exit 130; the end of the input is an empty line, not an error.
func (c *Call) readSecret(prompt string, show bool) (string, error) {
	if show {
		fmt.Fprint(c.Stderr, prompt)
	}
	var line string
	var err error
	if c.Term.Interactive() {
		line, err = c.Term.ReadSecret(c.Ctx)
	} else {
		line, err = c.Term.ReadLine(c.Ctx)
	}
	if show {
		fmt.Fprintln(c.Stderr) // echo was off: end the prompt line ourselves
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "", ExitWith(ExitInterrupted)
	case err != nil && !errors.Is(err, io.EOF):
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// verifyPIN exchanges the PIN for a grant (POST /pin/verify). An agent error comes back as the
// *APIError (LAI-SEC-003 with details.remaining_attempts, LAI-SEC-004 with RetryAfter, LAI-SEC-005
// when no PIN is set); the request and its answer are kept out of the debug log (client.go).
func (c *Call) verifyPIN(cl *Client, pin string) (string, error) {
	var res struct {
		Grant string `json:"grant"`
	}
	if _, err := cl.Do(c.Ctx, http.MethodPost, pinVerifyPath, map[string]string{"pin": pin}, &res); err != nil {
		return "", err
	}
	if res.Grant == "" {
		return "", c.Err(CodeBadResponse, "").WithDetails("reason", "no grant in the answer")
	}
	return res.Grant, nil
}
