package cloud

import (
	"context"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// Lease renewal (agent.md section 4.8.6, step 4): while a command runs the agent renews its lease
// every renewIntervalMs (20 s unless the cloud says otherwise). A failed renewal is not fatal: the
// operation goes on and the next tick tries again, and the counters say how often it happened. A 409
// means the cloud has moved on (the lease passed, or the hard cap): renewing stops, the operation
// finishes cleanly and its outcome is offered to the cloud once, which will answer 409 too.

// renewLoop renews the lease of j until ctx ends or a 409 stops it.
func (c *Claimer) renewLoop(ctx context.Context, link ClaimLink, j *jobRun, every time.Duration, lease time.Duration) {
	defer close(j.renewExited)
	for {
		if !c.wait(ctx, every) {
			return
		}
		sess, ok := link.Session()
		if !ok {
			link.Counters().LeaseRenewFailures.Add(1)
			continue
		}
		body := renewBody{ClaimToken: j.token}
		reply, err := sess.Client.Call(ctx, "POST", commandPath(sess.ServerNodeID, j.plane, j.id, "renew"), body, callTimeout)
		if err == nil {
			link.Counters().LeaseRenewals.Add(1)
			var rd struct {
				LeaseExpiresAt string `json:"leaseExpiresAt"`
			}
			_ = reply.Decode(&rd)
			next := c.clk.Now().Add(lease)
			if t, perr := time.Parse(time.RFC3339, rd.LeaseExpiresAt); perr == nil {
				next = t
			}
			c.touchLease(j, next)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		link.Counters().LeaseRenewFailures.Add(1)
		if ae := asAPIError(err); ae != nil {
			switch ae.Status {
			case 409:
				c.log.Info("the cloud no longer waits for a command; the operation finishes anyway",
					"code", doctor.CLD059, "type", j.fullType, "job_id", j.id)
				j.leaseLost.Store(true)
				return
			case 401:
				link.NoteCommandAuth(err)
			}
		}
		c.log.Debug("a lease renewal failed", "job_id", j.id, "error", errText(err))
	}
}

// touchLease records a renewed lease in memory and in inflight.json.
func (c *Claimer) touchLease(j *jobRun, lease time.Time) {
	c.mu.Lock()
	j.lease = lease
	e := j.entryLocked()
	_, tracked := c.jobs[j.id]
	c.mu.Unlock()
	if tracked && !j.isSettled() {
		c.putInflight(e)
	}
}
