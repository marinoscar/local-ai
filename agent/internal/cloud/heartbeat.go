package cloud

import (
	"context"
	"slices"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
)

// heartbeatBody is the body of POST /api/nodes/{id}/heartbeat (agent.md section 4.8.3). Vitals is a
// pointer so the "drop vitals for the life of the process" fallback can leave it out.
type heartbeatBody struct {
	Status       string           `json:"status"`
	Concurrency  int              `json:"concurrency"`
	Capabilities Capabilities     `json:"capabilities"`
	Vitals       *Vitals          `json:"vitals,omitempty"`
	Events       []HeartbeatEvent `json:"events,omitempty"`
}

type hbKind int

const (
	hbOK hbKind = iota
	// hbRelink: a 403 or 404; the supervisor registers again.
	hbRelink
	// hbRevoked: a 401.
	hbRevoked
	hbFail
)

type hbResult struct {
	kind       hbKind
	retryAfter time.Duration
	err        error
}

// heartbeatLoop sends a heartbeat now and then every heartbeat interval (300 s while disabled), with
// the loop's own backoff while it fails. It checks the mode on every iteration: a heartbeat is never
// sent outside online mode.
func (l *Link) heartbeatLoop(ctx context.Context) {
	bo := NewBackoff(l.rnd)
	for {
		delay := l.heartbeatInterval()
		if l.online() {
			res := l.heartbeatOnce(ctx)
			switch res.kind {
			case hbOK:
				bo.Reset()
			case hbFail:
				delay = bo.Fail(res.retryAfter)
			}
			if res.kind == hbFail && res.retryAfter >= hourly {
				delay = res.retryAfter
			}
		}
		if !l.wait(ctx, delay) {
			return
		}
	}
}

// heartbeatOnce sends one heartbeat with the 400 fallbacks and classifies the answer.
func (l *Link) heartbeatOnce(ctx context.Context) hbResult {
	path, ok := l.serverPath("/heartbeat")
	if !ok {
		return hbResult{kind: hbFail, err: ErrNotLinked}
	}
	cfg := l.cfg()
	st := l.d.Status.Build(ctx, true, statusBuildOptions)
	var hw hardware.Snapshot
	if l.d.Hardware != nil {
		hw = l.d.Hardware.Snapshot()
	}
	now := l.clk.Now()
	hostOn := l.hostVitalsOn()
	// Observe on every heartbeat so the baselines are ready when node_host_vitals shows up; the
	// events are queued only for a server that takes them.
	if evs := l.observer.Observe(now, hw, l.heartbeatInterval()); hostOn {
		l.evq.Push(evs...)
	}
	var events []HeartbeatEvent
	if hostOn {
		events = l.evq.Take()
	}
	caps, dropped := BuildCapabilities(st, cfg)
	l.logDropped("capabilities", dropped)
	l.mu.Lock()
	fallback := l.st.stateFallback
	l.mu.Unlock()
	if fallback {
		if b, err := l.snaps.Build(ctx); err == nil {
			red := Reduce(b.Snapshot, defaultModelOf(b.Snapshot))
			caps.Localai.State = &red
		}
	}
	slotsUsed := 0
	if l.d.Inflight != nil {
		slotsUsed, _ = l.d.Inflight()
	}
	var host *HostVitals
	if hostOn {
		h := l.hostSrc.Host()
		host = &h
	}

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		l.mu.Lock()
		noExtra, noVitals := l.st.noExtra, l.st.noVitals
		l.mu.Unlock()
		body := heartbeatBody{Status: "online", Concurrency: cfg.Cloud.Concurrency, Capabilities: caps}
		if !noVitals {
			v := BuildVitals(VitalsInput{
				Now: now, StartedAt: l.d.StartedAt, CLIVersion: l.cliVersion(), HW: hw, Status: st, Counters: l.counters.Wire(),
				SlotsUsed: slotsUsed, SlotsTotal: cfg.Cloud.Concurrency, Host: host, NoExtra: noExtra,
			})
			body.Vitals = &v
		}
		if len(events) > 0 {
			body.Events = events
		}
		var reply *Reply
		reply, err = l.client().Call(ctx, "POST", path, body, callTimeout)
		if err == nil {
			l.heartbeatOK(ctx, reply)
			return hbResult{kind: hbOK}
		}
		ae := asAPIError(err)
		if ae == nil || ae.Status != 400 {
			break
		}
		// 400 with "extra" in the details: send again at once without vitals.extra, and never send it
		// again. Any other 400: send again without vitals at all (agent.md section 4.8.3).
		if !noExtra && !noVitals && ae.Mentions("extra") {
			l.mu.Lock()
			l.st.noExtra = true
			l.mu.Unlock()
			l.log.Info("the cloud does not take vitals.extra; it is not sent any more", "code", doctor.CLD055)
			l.raise(doctor.CLD055, map[string]any{"dropped": "vitals.extra"})
			continue
		}
		if !noVitals {
			l.mu.Lock()
			l.st.noVitals = true
			l.mu.Unlock()
			l.log.Warn("the cloud refused the vitals; they are not sent any more", "code", doctor.CLD055, "paths", ae.Paths)
			l.raise(doctor.CLD055, map[string]any{"dropped": "vitals"}, doctor.WithSeverity(doctor.Warning))
			continue
		}
		break
	}
	l.evq.Requeue(events)
	return l.heartbeatFailed(err)
}

func defaultModelOf(s StateSnapshot) string {
	for _, m := range s.Models {
		if m.Default {
			return m.ID
		}
	}
	return ""
}

// heartbeatOK records a heartbeat the cloud accepted: the server features, the time, and the return
// from backoff.
func (l *Link) heartbeatOK(ctx context.Context, reply *Reply) {
	var hr struct {
		ServerFeatures []string `json:"serverFeatures"`
	}
	_ = reply.Decode(&hr)
	now := l.clk.Now()
	l.mu.Lock()
	changed := hr.ServerFeatures != nil && !slices.Equal(hr.ServerFeatures, l.st.features)
	if changed {
		l.st.features = append([]string(nil), hr.ServerFeatures...)
	}
	l.st.lastHB = now
	l.st.hbFails, l.st.hb403, l.st.failingSince = 0, 0, time.Time{}
	alerted := l.st.failAlert
	l.st.failAlert = false
	state := l.st.state
	l.mu.Unlock()
	if changed {
		l.saveLink()
	}
	if alerted {
		l.clear(doctor.CLD053)
	}
	if state == StateBackoff {
		l.clear(doctor.CLD053)
		l.setState(StateLinked, "")
	}
	if state == StateDisabled && l.d.Claim != nil {
		// The claim is re-checked on each heartbeat while disabled (agent.md section 4.8.1).
		if p, ok := l.d.Claim.(interface{ Probe(context.Context) }); ok {
			p.Probe(ctx)
		}
	}
}

// heartbeatFailed applies the table of agent.md section 4.8.3 to a failed heartbeat: 401 is revoked,
// the first 403 and any 404 register again, everything else counts toward backoff.
func (l *Link) heartbeatFailed(err error) hbResult {
	ae := asAPIError(err)
	l.counters.HeartbeatFailures.Add(1)
	switch {
	case ae != nil && ae.Status == 401:
		l.signalRelink(relinkReq{revoked: true, reason: "the cloud rejected the credential"})
		return hbResult{kind: hbRevoked, err: err}
	case ae != nil && ae.Status == 404:
		l.signalRelink(relinkReq{reason: "the cloud does not know this node"})
		return hbResult{kind: hbRelink, err: err}
	case ae != nil && ae.Status == 403:
		l.mu.Lock()
		l.st.hb403++
		n := l.st.hb403
		l.mu.Unlock()
		if n == 1 {
			l.signalRelink(relinkReq{reason: "the cloud answered 403 to a heartbeat"})
			return hbResult{kind: hbRelink, err: err}
		}
		// A second 403 after registering again: the cloud refuses this node for good until somebody acts.
		l.raise(doctor.CLD054, map[string]any{"status": 403})
		l.mu.Lock()
		l.st.lastErr = "the cloud refuses this node's heartbeats"
		l.mu.Unlock()
		return hbResult{kind: hbFail, retryAfter: hourly, err: err}
	}
	now := l.clk.Now()
	l.mu.Lock()
	l.st.hbFails++
	if l.st.failingSince.IsZero() {
		l.st.failingSince = now
	}
	fails, since, state := l.st.hbFails, l.st.failingSince, l.st.state
	alert := !l.st.failAlert && now.Sub(since) >= failWarnAfter
	if alert {
		l.st.failAlert = true
	}
	l.mu.Unlock()
	msg := errText(err)
	if fails >= hbFailuresToBack && state == StateLinked {
		l.setState(StateBackoff, msg)
	} else if state == StateBackoff {
		l.setState(StateBackoff, msg)
	}
	if alert {
		l.raise(doctor.CLD053, nil)
	}
	res := hbResult{kind: hbFail, err: err}
	if ae != nil {
		res.retryAfter = ae.RetryAfter
	}
	return res
}
