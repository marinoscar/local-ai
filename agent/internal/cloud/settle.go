package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

// Settling a command (agent.md section 4.8.6, step 6): a result or a failure, once. A command is
// at most once: nothing here retries after the cloud answered, and a 409 (the cloud moved on) drops
// the work for good. Only a request that got no answer, or a 429 or 5xx, is repeated, three times
// after 1 s, 5 s and 15 s, and then dropped.

// Limits of what is sent.
const (
	// MaxFailureChars is the longest failure text (EAB's nodeJobFailureSchema).
	MaxFailureChars = 2000
	// MaxResultBytes bounds a result body; logs.tail, the largest result, truncates itself below it.
	MaxResultBytes = 1 << 20
)

// settleRetries are the waits before the three repeats of a settle that got no answer.
var settleRetries = [3]time.Duration{time.Second, 5 * time.Second, 15 * time.Second}

type resultBody struct {
	Type       string          `json:"type"`
	ClaimToken string          `json:"claimToken,omitempty"`
	Result     json.RawMessage `json:"result"`
}

type failureBody struct {
	ClaimToken string `json:"claimToken,omitempty"`
	Error      string `json:"error"`
}

type renewBody struct {
	ClaimToken string `json:"claimToken,omitempty"`
}

// failureText is "<code>: <message>", scrubbed and cut to 2000 characters. The cloud splits the
// leading code off.
func failureText(code, msg string) string {
	text := logx.ScrubString(code + ": " + strings.TrimSpace(msg))
	r := []rune(text)
	if len(r) > MaxFailureChars {
		return string(r[:MaxFailureChars])
	}
	return text
}

// CodeAndMessage turns an error of a handler into the doctor code and the plain-language message of
// a failure: a *JobError says both, an *api.Error its code with its own message or the code's title,
// a deadline is LAI-AGT-099 with the time limit; anything else is LAI-AGT-099 without its text
// (which may name paths): the agent log has it.
func CodeAndMessage(err error) (code, msg string) {
	var je *JobError
	var ae *api.Error
	switch {
	case errors.As(err, &je):
		return je.Code, je.Message
	case errors.As(err, &ae):
		msg = ae.Message
		if msg == "" {
			msg, _, _ = doctor.Text(ae.Code, "en")
		}
		if msg == "" {
			msg = "The command failed."
		}
		return ae.Code, msg
	case errors.Is(err, context.DeadlineExceeded):
		return doctor.AGT099, "The command did not finish in the time allowed for it."
	case errors.Is(err, context.Canceled):
		return doctor.AGT099, "The command was cancelled."
	}
	return doctor.AGT099, "The command failed with an unexpected error; the agent log has the details."
}

// sendStatus is how a settle or a renew call ended.
type sendStatus int

const (
	// sentOK: the cloud accepted it (2xx).
	sentOK sendStatus = iota
	// sentLost: 409, the cloud moved on; the work is dropped and never retried.
	sentLost
	// sentRefused: the cloud answered 400 (a result its schema refuses).
	sentRefused
	// sentDropped: another answer that will not change (401, 403, 404, 4xx), or no answer after the
	// retries.
	sentDropped
)

// send posts body to the settle path op ("result" or "failure") of a command, with up to attempts
// tries (1 means no repeat). It never sends the claim token anywhere but the body.
func (c *Claimer) send(ctx context.Context, link ClaimLink, plane, jobID, op string, body any, attempts int) sendStatus {
	for try := 0; ; try++ {
		sess, ok := link.Session()
		var err error
		if ok {
			_, err = sess.Client.Call(ctx, "POST", commandPath(sess.ServerNodeID, plane, jobID, op), body, callTimeout)
		} else {
			err = errNoSession
		}
		if err == nil {
			return sentOK
		}
		if ae := asAPIError(err); ae != nil {
			switch {
			case ae.Status == 409:
				c.log.Info("the cloud no longer waits for a command", "code", doctor.CLD059, "job_id", jobID, "op", op)
				return sentLost
			case ae.Status == 401:
				link.NoteCommandAuth(err)
				return sentDropped
			case ae.Status == 400 && op == "result":
				c.log.Warn("the cloud refused a result", "code", doctor.CLD058, "job_id", jobID, "paths", ae.Paths)
				return sentRefused
			case ae.Status != 429 && ae.Status < 500:
				c.log.Warn("the cloud refused a command settle", "job_id", jobID, "op", op, "status", ae.Status)
				return sentDropped
			}
		}
		if ctx.Err() != nil || try >= attempts-1 || try >= len(settleRetries) {
			c.log.Warn("a command settle was dropped", "job_id", jobID, "op", op, "error", errText(err))
			return sentDropped
		}
		if !c.wait(ctx, settleRetries[try]) {
			return sentDropped
		}
	}
}

// sendResult settles a command with its result. A result the cloud refuses (400) is replaced by a
// failure with LAI-CLD-012, once: the command stays running on the cloud otherwise.
func (c *Claimer) sendResult(ctx context.Context, link ClaimLink, plane, jobID, typ, token string, result json.RawMessage, attempts int) sendStatus {
	st := c.send(ctx, link, plane, jobID, "result", resultBody{Type: typ, ClaimToken: token, Result: result}, attempts)
	if st == sentRefused {
		c.sendFailure(ctx, link, plane, jobID, token, failureText(doctor.CLD012, "the cloud did not accept the result of this command"), 1)
		return sentDropped
	}
	return st
}

// sendFailure settles a command with a failure text.
func (c *Claimer) sendFailure(ctx context.Context, link ClaimLink, plane, jobID, token, text string, attempts int) sendStatus {
	return c.send(ctx, link, plane, jobID, "failure", failureBody{ClaimToken: token, Error: text}, attempts)
}

// commandPath is the settle (or renew) path of a command on a plane.
func commandPath(serverNodeID, plane, jobID, op string) string {
	if plane == PlaneEAB {
		return "/api/nodes/" + pathEscape(serverNodeID) + "/jobs/" + pathEscape(jobID) + "/" + op
	}
	return "/api/nodes/" + pathEscape(serverNodeID) + "/localai/commands/" + pathEscape(jobID) + "/" + op
}

// SettleResult settles a command that an earlier process left in inflight.json with its result:
// AGENT-19 uses it after an agent.update restart, with the persisted claim token. It removes the
// entry whatever the cloud answers (a 409 means the cloud moved on, and the entry is useless then).
// It returns ErrNotLinked, leaving the entry, while the link cannot talk; the caller tries again
// when the link is linked.
func (c *Claimer) SettleResult(ctx context.Context, e InflightEntry, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	link := c.getLink()
	if link == nil {
		return ErrNotLinked
	}
	if _, ok := link.Session(); !ok {
		return ErrNotLinked
	}
	c.sendResult(ctx, link, e.Plane, e.JobID, e.Type, e.ClaimToken, raw, len(settleRetries)+1)
	c.removeInflight(e.JobID)
	return nil
}

// SettleFailure is SettleResult for a failure ("LAI-AGT-009: ..."); text is built with the doctor
// code first and is scrubbed and cut like every failure.
func (c *Claimer) SettleFailure(ctx context.Context, e InflightEntry, code, message string) error {
	link := c.getLink()
	if link == nil {
		return ErrNotLinked
	}
	if _, ok := link.Session(); !ok {
		return ErrNotLinked
	}
	c.sendFailure(ctx, link, e.Plane, e.JobID, e.ClaimToken, failureText(code, message), len(settleRetries)+1)
	c.removeInflight(e.JobID)
	return nil
}
