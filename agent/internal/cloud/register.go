package cloud

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// regOutcome is how a register call ended.
type regOutcome int

const (
	// regOK: 200, the node is registered.
	regOK regOutcome = iota
	// regRevoked: 401, the credential is rejected.
	regRevoked
	// regRejected: 400 or 403, the cloud refuses this registration (LAI-CLD-054).
	regRejected
	// regTransient: network, 5xx, 429 or anything else; retry with backoff.
	regTransient
)

type regResult struct {
	outcome    regOutcome
	err        error
	status     int
	retryAfter time.Duration
	reattached bool
}

// registerBody is the body of POST /api/nodes/register (agent.md section 4.8.2).
type registerBody struct {
	Name          string       `json:"name"`
	Hostname      string       `json:"hostname"`
	Platform      string       `json:"platform"`
	CLIVersion    string       `json:"cliVersion"`
	EligibleTypes []string     `json:"eligibleTypes"`
	Concurrency   int          `json:"concurrency"`
	Capabilities  Capabilities `json:"capabilities"`
}

// registerReply is the part of the register answer the agent uses.
type registerReply struct {
	Node struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Hostname        string `json:"hostname"`
		LastHeartbeatAt string `json:"lastHeartbeatAt"`
	} `json:"node"`
	Reattached     bool     `json:"reattached"`
	ServerFeatures []string `json:"serverFeatures"`
}

// registerOnce sends one register call and, on a 200, stores the answer (server node id, features,
// link.json). It does not run the handshake and does not change the link state; the caller decides
// what the outcome means.
func (l *Link) registerOnce(ctx context.Context) regResult {
	cfg := l.cfg()
	st := l.d.Status.Build(ctx, true, statusBuildOptions)
	caps, dropped := BuildCapabilities(st, cfg)
	l.logDropped("capabilities", dropped)
	body := registerBody{
		Name: cfg.Node.ID, Hostname: l.hostname(), Platform: platformString(), CLIVersion: l.cliVersion(),
		EligibleTypes: EligibleTypes(cfg.Cloud.Allow), Concurrency: cfg.Cloud.Concurrency, Capabilities: caps,
	}
	reply, err := l.client().Call(ctx, "POST", "/api/nodes/register", body, registerTimeout)
	if err != nil {
		return l.classifyRegister(err)
	}
	var rr registerReply
	if err := reply.Decode(&rr); err != nil || rr.Node.ID == "" {
		return regResult{outcome: regTransient, err: fmt.Errorf("cloud: the register answer has no node id")}
	}
	now := l.clk.Now()

	l.mu.Lock()
	prev, hadPrev := l.st.rec, l.st.haveRec
	rec := LinkRecord{
		URL: l.st.url, ServerNodeID: rr.Node.ID, Name: cfg.Node.ID, RegisteredAt: now,
		ServerFeatures: append([]string{}, rr.ServerFeatures...),
	}
	if hadPrev && prev.ServerNodeID == rr.Node.ID && !prev.RegisteredAt.IsZero() {
		rec.RegisteredAt = prev.RegisteredAt
	}
	l.st.rec, l.st.haveRec = rec, true
	l.st.features = rec.ServerFeatures
	l.mu.Unlock()
	if hadPrev && prev.ServerNodeID != "" && prev.ServerNodeID != rr.Node.ID {
		l.log.Info("the cloud created a new server row for this node")
	}
	l.checkIDConflict(rr, now)
	l.saveLink()
	return regResult{outcome: regOK, reattached: rr.Reattached}
}

// classifyRegister maps the error of a register call to its outcome (agent.md section 4.8.2): 401 is
// revoked, 400 and 403 are LAI-CLD-054 (the details are logged, never the body), everything else is
// transient.
func (l *Link) classifyRegister(err error) regResult {
	ae := asAPIError(err)
	if ae == nil {
		return regResult{outcome: regTransient, err: err}
	}
	switch {
	case ae.Status == 401:
		return regResult{outcome: regRevoked, err: err, status: 401}
	case ae.Status == 400 || ae.Status == 403:
		l.log.Warn("the cloud refused the registration", "code", doctor.CLD054, "status", ae.Status,
			"cloud_code", ae.Code, "reason", ae.Reason, "message", ae.Message, "paths", ae.Paths)
		return regResult{outcome: regRejected, err: err, status: ae.Status}
	}
	return regResult{outcome: regTransient, err: err, status: ae.Status, retryAfter: ae.RetryAfter}
}

// checkIDConflict raises LAI-CLD-061 when the row we registered into was heartbeating from another
// machine within the last minute: two computers use the same node id (agent.md section 4.8.2).
func (l *Link) checkIDConflict(rr registerReply, now time.Time) {
	conflict := false
	if rr.Node.LastHeartbeatAt != "" && rr.Node.Hostname != "" && rr.Node.Hostname != l.hostname() {
		if t, err := time.Parse(time.RFC3339, rr.Node.LastHeartbeatAt); err == nil && now.Sub(t) <= conflictWindow && t.Sub(now) <= conflictWindow {
			conflict = true
		}
	}
	if conflict {
		l.raise(doctor.CLD061, nil)
		return
	}
	l.clear(doctor.CLD061)
}

// saveLink writes link.json from the in-memory record; a failure is logged only (the link works
// without it).
func (l *Link) saveLink() {
	l.mu.Lock()
	rec := l.st.rec
	rec.URL = l.st.url
	rec.ServerFeatures = append([]string{}, l.st.features...)
	if l.st.haveHS {
		h := l.st.hs.record()
		rec.Handshake = &h
	}
	l.mu.Unlock()
	if err := l.d.LinkFile.Save(rec); err != nil {
		l.log.Warn("link.json could not be written", "error", errText(err))
	}
}

// logDropped logs the fields the scrubber removed from an outgoing document (LAI-CLD-058, warning):
// the paths only, never the values.
func (l *Link) logDropped(what string, paths []string) {
	if len(paths) == 0 {
		return
	}
	l.log.Warn("fields removed from what is sent to the cloud", "code", doctor.CLD058, "document", what, "paths", paths)
}

// pathEscape escapes a path segment.
func pathEscape(s string) string { return url.PathEscape(s) }

// newUUID returns a random version 4 UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on Linux; a zero id would still be a valid (if useless) boot id.
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
