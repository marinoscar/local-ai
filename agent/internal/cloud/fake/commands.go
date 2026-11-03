package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The command plane of the fake: both planes of agent.md section 4.8.6 over one queue.
//
//	localai  POST /api/nodes/{id}/localai/commands/claim                       types without prefix, limit 1, waitMs
//	         POST /api/nodes/{id}/localai/commands/{cmd}/renew|result|failure
//	eab      POST /api/nodes/{id}/claim                                        types "localai.<name>", limit n
//	         POST /api/nodes/{id}/jobs/{job}/renew|result|failure
//
// A queued job is claimed in FIFO order; the claim answer carries a claim token (or none, for a job
// queued without one: the key is then absent from the assignment, and the settle routes refuse a body
// that has the key at all, as EAB does). The held-claim rule answers 409 to a renew, result or failure
// for a job that is not running with that token, or whose lease the test took away (LoseLease).

// Job is a queued command.
type Job struct {
	ID string
	// Type is the full type, "localai.model.load".
	Type   string
	Params any
	// Token is the claim token the claim answer carries; empty means the assignment has none.
	Token string
	// RenewIntervalMs defaults to 20000.
	RenewIntervalMs int
	// LeaseExpiresAt, when set, is sent as leaseExpiresAt.
	LeaseExpiresAt string
	// Force delivers the job to any claim, whatever types it asked for and however small its limit: a
	// hostile or confused cloud.
	Force bool
}

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// JobRecord is what the cloud knows about a job.
type JobRecord struct {
	Job      Job
	Status   string
	Attempts int
	Renewals int
	// Result is the settled result, Error the settled failure text.
	Result json.RawMessage
	Error  string
	// Lost: the test took the lease away (the cloud "moved on").
	Lost bool
}

type commandState struct {
	records map[string]*JobRecord
	order   []string // queue order of every job
	held    int      // claims being held open
	wake    chan struct{}
	release bool
}

func (c *commandState) init() {
	if c.records == nil {
		c.records = map[string]*JobRecord{}
		c.wake = make(chan struct{})
	}
}

func (c *commandState) notify() {
	c.init()
	close(c.wake)
	c.wake = make(chan struct{})
}

// QueueJob adds a command for the claim routes.
func (s *Server) QueueJob(j Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds.init()
	if j.RenewIntervalMs == 0 {
		j.RenewIntervalMs = 20000
	}
	s.cmds.records[j.ID] = &JobRecord{Job: j, Status: JobQueued}
	s.cmds.order = append(s.cmds.order, j.ID)
	s.cmds.notify()
}

// MarkRunning sets a queued job running without a claim, as if an earlier process had claimed it
// (the agent restarted with the command in inflight.json).
func (s *Server) MarkRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds.init()
	if r, ok := s.cmds.records[id]; ok {
		r.Status = JobRunning
		r.Attempts++
		s.cmds.notify()
	}
}

// Job returns the record of a job.
func (s *Server) Job(id string) (JobRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds.init()
	r, ok := s.cmds.records[id]
	if !ok {
		return JobRecord{}, false
	}
	return *r, true
}

// LoseLease makes the cloud move on from a running job: its renew, result and failure answer 409.
func (s *Server) LoseLease(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cmds.init()
	if r, ok := s.cmds.records[id]; ok {
		r.Lost = true
	}
}

// HoldClaims makes a claim that finds no job wait until one is queued, ReleaseClaims is called or the
// client goes away (a long poll).
func (s *Server) HoldClaims(on bool) {
	s.mu.Lock()
	s.cmds.release = false
	s.cmds.init()
	s.holdClaims = on
	s.mu.Unlock()
}

// ReleaseClaims answers every held claim with an empty list.
func (s *Server) ReleaseClaims() {
	s.mu.Lock()
	s.cmds.init()
	s.cmds.release = true
	s.cmds.notify()
	s.mu.Unlock()
}

// WaitHeld blocks until n claims are being held open, or ctx ends.
func (s *Server) WaitHeld(ctx context.Context, n int) error {
	for {
		s.mu.Lock()
		s.cmds.init()
		ok := s.cmds.held >= n
		ch := s.cmds.wake
		s.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("waiting for %d held claims: %w", n, ctx.Err())
		}
	}
}

// WaitHeldAtMost blocks until at most n claims are held open (a cancelled long poll has been noticed by
// the server), or ctx ends.
func (s *Server) WaitHeldAtMost(ctx context.Context, n int) error {
	for {
		s.mu.Lock()
		s.cmds.init()
		ok := s.cmds.held <= n
		ch := s.cmds.wake
		s.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("waiting for at most %d held claims: %w", n, ctx.Err())
		}
	}
}

// WaitJob blocks until a job has the status, or ctx ends.
func (s *Server) WaitJob(ctx context.Context, id, status string) error {
	for {
		s.mu.Lock()
		s.cmds.init()
		r, ok := s.cmds.records[id]
		done := ok && r.Status == status
		ch := s.cmds.wake
		s.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return fmt.Errorf("waiting for job %s to be %s: %w", id, status, ctx.Err())
		}
	}
}

// cmdRoutes registers the claim, renew, result and failure routes of both planes.
func (s *Server) cmdRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/nodes/{id}/localai/commands/claim", s.route(RouteClaim, s.claim(false)))
	mux.HandleFunc("POST /api/nodes/{id}/claim", s.route(RouteClaim, s.claim(true)))
	for _, op := range []string{"renew", "result", "failure"} {
		name := map[string]string{"renew": RouteRenew, "result": RouteResult, "failure": RouteFailure}[op]
		mux.HandleFunc("POST /api/nodes/{id}/localai/commands/{cmd}/"+op, s.route(name, s.settle(op)))
		mux.HandleFunc("POST /api/nodes/{id}/jobs/{cmd}/"+op, s.route(name, s.settle(op)))
	}
}

func badRequest(reason string) (int, any) {
	return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"reason": reason}}}
}

// strictObject decodes body into a map and reports the keys that are not allowed.
func strictObject(raw []byte, allowed ...string) (map[string]json.RawMessage, string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, "the body is not a JSON object"
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range m {
		if !ok[k] {
			return nil, fmt.Sprintf("unrecognized key %q", k)
		}
	}
	return m, ""
}

func (s *Server) claim(eab bool) handler {
	return func(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
		if _, ok := s.node(r); !ok {
			return 404, errNoNode
		}
		m, problem := strictObject(raw, "types", "limit", "waitMs")
		if problem != "" {
			return badRequest(problem)
		}
		var types []string
		if json.Unmarshal(m["types"], &types) != nil || len(types) == 0 {
			return badRequest("types must be a non-empty array of strings")
		}
		var limit int
		if json.Unmarshal(m["limit"], &limit) != nil || limit < 1 {
			return badRequest("limit must be a positive integer")
		}
		wait := 0
		if w, has := m["waitMs"]; has {
			if eab {
				return badRequest("waitMs belongs to the localai plane")
			}
			if json.Unmarshal(w, &wait) != nil || wait < 0 || wait > 25000 {
				return badRequest("waitMs must be between 0 and 25000")
			}
		}
		want := map[string]bool{}
		for _, t := range types {
			if eab {
				if !strings.HasPrefix(t, "localai.") {
					return badRequest("types must start with localai.")
				}
				want[t] = true
			} else {
				if strings.HasPrefix(t, "localai.") {
					return badRequest("types are sent without the localai. prefix")
				}
				want["localai."+t] = true
			}
		}
		if !eab && limit > 1 {
			limit = 1
		}
		return s.takeJobs(r.Context(), eab, want, limit, wait)
	}
}

// takeJobs claims up to limit queued jobs of the wanted types; when there are none and claims are
// held (a long poll) it waits for one.
func (s *Server) takeJobs(ctx context.Context, eab bool, want map[string]bool, limit, waitMs int) (int, any) {
	held := false
	defer func() {
		if held {
			s.mu.Lock()
			s.cmds.held--
			s.cmds.notify()
			s.mu.Unlock()
		}
	}()
	for {
		s.mu.Lock()
		s.cmds.init()
		if s.Disabled {
			s.mu.Unlock()
			return 403, map[string]any{"error": map[string]any{"message": "Forbidden", "details": map[string]any{"reason": "node_disabled"}}}
		}
		running := false
		for _, r := range s.cmds.records {
			running = running || (r.Status == JobRunning && !r.Lost)
		}
		var picked []any
		if eab || !running {
			for _, id := range s.cmds.order {
				rec := s.cmds.records[id]
				if rec.Status != JobQueued || (!rec.Job.Force && (!want[rec.Job.Type] || len(picked) >= limit)) {
					continue
				}
				rec.Status = JobRunning
				rec.Attempts++
				a := map[string]any{
					"job":    map[string]any{"id": rec.Job.ID, "type": rec.Job.Type, "attempts": rec.Attempts},
					"params": rec.Job.Params, "renewIntervalMs": rec.Job.RenewIntervalMs, "traceparent": nil,
				}
				if rec.Job.Token != "" {
					a["claimToken"] = rec.Job.Token
				}
				if rec.Job.LeaseExpiresAt != "" {
					a["leaseExpiresAt"] = rec.Job.LeaseExpiresAt
				}
				picked = append(picked, a)
			}
		}
		if len(picked) > 0 {
			s.cmds.notify()
			s.mu.Unlock()
			return 200, envelope(map[string]any{"jobs": picked})
		}
		if !s.holdClaims || waitMs == 0 || s.cmds.release {
			s.mu.Unlock()
			return 200, envelope(map[string]any{"jobs": []any{}})
		}
		if !held {
			held = true
			s.cmds.held++
			s.cmds.notify()
		}
		ch := s.cmds.wake
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return 200, envelope(map[string]any{"jobs": []any{}})
		case <-s.done:
			return 200, envelope(map[string]any{"jobs": []any{}})
		}
	}
}

func (s *Server) settle(op string) handler {
	return func(_ http.ResponseWriter, r *http.Request, raw []byte) (int, any) {
		if _, ok := s.node(r); !ok {
			return 404, errNoNode
		}
		id := r.PathValue("cmd")
		allowed := map[string][]string{
			"renew": {"claimToken"}, "result": {"type", "claimToken", "result"}, "failure": {"claimToken", "error", "rateLimited", "retryAfterMs"},
		}[op]
		m, problem := strictObject(raw, allowed...)
		if problem != "" {
			return badRequest(problem)
		}
		tok, hasTok := "", false
		if t, has := m["claimToken"]; has {
			if bytes.Equal(bytes.TrimSpace(t), []byte("null")) || json.Unmarshal(t, &tok) != nil || tok == "" {
				return badRequest("claimToken must be a non-empty string when present")
			}
			hasTok = true
		}

		s.mu.Lock()
		s.cmds.init()
		rec, ok := s.cmds.records[id]
		if !ok {
			s.mu.Unlock()
			return 404, errNoNode
		}
		held := rec.Status == JobRunning && !rec.Lost && tok == rec.Job.Token && hasTok == (rec.Job.Token != "")
		if !held {
			s.mu.Unlock()
			return 409, map[string]any{"error": map[string]any{"message": "Conflict", "details": map[string]any{"reason": "claim_not_held"}}}
		}
		switch op {
		case "renew":
			rec.Renewals++
			s.cmds.notify()
			lease := s.Now().Add(time.Minute).UTC().Format(time.RFC3339)
			s.mu.Unlock()
			return 200, envelope(map[string]any{"leaseExpiresAt": lease})
		case "result":
			var typ string
			if json.Unmarshal(m["type"], &typ) != nil || typ != rec.Job.Type {
				s.mu.Unlock()
				return badRequest("type does not match the command")
			}
			if probs := ValidateResult(typ, m["result"]); len(probs) > 0 {
				s.mu.Unlock()
				issues := make([]any, 0, len(probs))
				for _, p := range probs {
					issues = append(issues, map[string]any{"message": p})
				}
				return 400, map[string]any{"error": map[string]any{"message": "Bad Request", "details": map[string]any{"issues": issues}}}
			}
			rec.Status, rec.Result = JobSucceeded, append(json.RawMessage(nil), m["result"]...)
			s.cmds.notify()
			s.mu.Unlock()
			return 200, envelope(map[string]any{"status": "succeeded"})
		default:
			var text string
			if json.Unmarshal(m["error"], &text) != nil || text == "" || len([]rune(text)) > 2000 {
				s.mu.Unlock()
				return badRequest("error must be a string of 1 to 2000 characters")
			}
			rec.Status, rec.Error = JobFailed, text
			s.cmds.notify()
			s.mu.Unlock()
			return 200, envelope(map[string]any{"status": "failed"})
		}
	}
}
