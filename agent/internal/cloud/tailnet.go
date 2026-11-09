package cloud

import (
	"context"
	"time"
)

// TailnetSource gives the node's own Tailscale address and name. *modes.TailscaleManager implements it. Both
// are "" while tailscale is not running or not logged in.
type TailnetSource interface {
	SelfAddress(ctx context.Context) (ip, machine string)
}

const (
	// tailnetRetryEvery is how long the loop waits when the address is not known yet or the cloud did not take it.
	tailnetRetryEvery = 5 * time.Minute
	// tailnetRecheckEvery is how often a reported address is looked at again (a re-login can change it).
	tailnetRecheckEvery = time.Hour
)

type tailnetBody struct {
	IP      string `json:"ip"`
	Machine string `json:"machine,omitempty"`
}

// tailnetLoop tells the cloud the node's own tailnet address and name, so the Add-node wizard never asks a
// person to type them (issue #437, cloud-app.md section 3.6). It runs when a handshake advertises
// "tailnet.v1", and again every hour; an address already sent is not sent twice. The address is
// admin-only data: it goes into the body of this one request and never into a log line.
func (l *Link) tailnetLoop(ctx context.Context) {
	if l.d.Tailnet == nil {
		return
	}
	var sent tailnetBody
	for {
		delay := tailnetRetryEvery
		if l.active() && l.hasFeature(FeatureTailnet) {
			switch body, err := l.pushTailnet(ctx, sent); {
			case err != nil:
				l.log.Warn("the tailnet address could not be sent to the cloud", "error", errText(err))
			case body.IP == "":
				// Tailscale is not up yet: look again soon.
			default:
				sent = body
				delay = tailnetRecheckEvery
			}
		}
		if !l.wait(ctx, delay, l.tailnetWake) {
			return
		}
	}
}

// pushTailnet reads the node's own address and posts it unless it is what was sent last. It returns the
// body that the cloud has (the same as last when nothing was sent), with IP "" when there is no address.
func (l *Link) pushTailnet(ctx context.Context, last tailnetBody) (tailnetBody, error) {
	ip, machine := l.d.Tailnet.SelfAddress(ctx)
	body := tailnetBody{IP: ip, Machine: machine}
	if body.IP == "" {
		return body, nil
	}
	if body == last {
		return body, nil
	}
	path, ok := l.serverPath("/localai/tailnet")
	if !ok {
		return last, ErrNotLinked
	}
	if _, err := l.client().Call(ctx, "POST", path, body, callTimeout); err != nil {
		if ae := asAPIError(err); ae != nil {
			l.noteAuth(err)
			// The cloud's message is not logged: the route only ever answers with field names.
			return last, &APIError{Status: ae.Status, Code: ae.Code, Reason: ae.Reason, RetryAfter: ae.RetryAfter}
		}
		return last, err
	}
	l.log.Info("the tailnet address was sent to the cloud")
	return body, nil
}
