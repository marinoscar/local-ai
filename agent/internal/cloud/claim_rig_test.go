package cloud

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
)

// recAuditSink records the audit lines and lets a test wait for them instead of sleeping.
type recAuditSink struct {
	mu      sync.Mutex
	evs     []audit.Event
	changed chan struct{}
}

func newRecAudit() *recAuditSink { return &recAuditSink{changed: make(chan struct{})} }

func (a *recAuditSink) Write(ev audit.Event) error {
	a.mu.Lock()
	a.evs = append(a.evs, ev)
	close(a.changed)
	a.changed = make(chan struct{})
	a.mu.Unlock()
	return nil
}

func (a *recAuditSink) events() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Event(nil), a.evs...)
}

// wait blocks until n lines were written, and returns them.
func (a *recAuditSink) wait(t *testing.T, ctx context.Context, n int) []audit.Event {
	t.Helper()
	for {
		a.mu.Lock()
		got, ch := len(a.evs), a.changed
		evs := append([]audit.Event(nil), a.evs...)
		a.mu.Unlock()
		if got >= n {
			return evs
		}
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("waiting for %d audit lines (have %d): %v", n, got, ctx.Err())
		}
	}
}

// testHandler is a Handler whose run is a function of the test; it reports every start.
type testHandler struct {
	name          string
	cancellable   bool
	needsInflight bool
	run           func(ctx context.Context, job Job, params any) (Outcome, error)
	started       chan Job
}

func newTestHandler(name string, run func(ctx context.Context, job Job, params any) (Outcome, error)) *testHandler {
	return &testHandler{name: name, run: run, started: make(chan Job, 16)}
}

type testParams struct {
	N *int `json:"n"`
}

func (h *testHandler) Type() string      { return h.name }
func (h *testHandler) Cancellable() bool { return h.cancellable }
func (h *testHandler) NeedsInflight() bool {
	return h.needsInflight
}
func (h *testHandler) Decode(raw json.RawMessage) (any, error) {
	var p testParams
	if err := DecodeParams(raw, &p); err != nil {
		return nil, err
	}
	return p, nil
}
func (h *testHandler) Run(ctx context.Context, job Job, params any) (Outcome, error) {
	h.started <- job
	return h.run(ctx, job, params)
}

// refreshResult is a valid result of status.refresh.
func refreshResult() map[string]any {
	return map[string]any{"seq": 1, "reported_at": "2026-11-02T15:00:00Z"}
}

// okHandler returns a status.refresh result at once.
func okHandler() *testHandler {
	return newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
		return Outcome{Result: refreshResult()}, nil
	})
}

// blockingHandler waits for release (or its context, when ctx should be honoured).
func blockingHandler(name string, release <-chan struct{}, honourCtx bool) *testHandler {
	h := newTestHandler(name, func(ctx context.Context, _ Job, _ any) (Outcome, error) {
		if honourCtx {
			select {
			case <-release:
			case <-ctx.Done():
				return Outcome{}, ctx.Err()
			}
		} else {
			<-release
		}
		return Outcome{Result: resultFor(name)}, nil
	})
	h.cancellable = honourCtx
	return h
}

// resultFor is a valid result of the type, for the few types the tests run.
func resultFor(name string) any {
	switch name {
	case TypeModelLoad:
		return map[string]any{"model_id": "model-a", "status": "loaded", "load_ms": 5, "unloaded": []string{}, "vram_used_mib": nil, "is_default": false}
	case TypeModeOffline:
		return map[string]any{"previous_mode": "online", "mode": "offline"}
	case TypeLogsTail:
		return map[string]any{"unit": "localai-agent.service", "lines": []string{}, "truncated": false}
	}
	return refreshResult()
}

// claimRig is a rig whose link runs a Claimer over test handlers.
type claimRig struct {
	*rig
	claimer  *Claimer
	audit    *recAuditSink
	inflight string
}

type claimRigOptions struct {
	// noLoop leaves the claimer out of the link: the test drives it through dispatch.
	noLoop  bool
	recover func(ctx context.Context, e InflightEntry) bool
	rig     []rigOpt
}

type claimOpt func(*claimRigOptions)

func withoutLoop() claimOpt { return func(o *claimRigOptions) { o.noLoop = true } }
func withRecover(f func(ctx context.Context, e InflightEntry) bool) claimOpt {
	return func(o *claimRigOptions) { o.recover = f }
}
func withRigOpts(r ...rigOpt) claimOpt {
	return func(o *claimRigOptions) { o.rig = append(o.rig, r...) }
}

// newClaimRig builds the rig and the claimer; it does not enroll. The heartbeat is slow (60 s) so a
// test that steps the clock by seconds moves only the timers of the command plane.
func newClaimRig(t *testing.T, hs []*testHandler, copts ...claimOpt) *claimRig {
	t.Helper()
	var o claimRigOptions
	for _, f := range copts {
		f(&o)
	}
	cr := &claimRig{audit: newRecAudit(), inflight: filepath.Join(t.TempDir(), "cloud", "inflight.json")}
	handlers := make([]Handler, len(hs))
	for i, h := range hs {
		handlers[i] = h
	}
	reg, err := NewRegistry(handlers...)
	if err != nil {
		t.Fatal(err)
	}
	opts := append([]rigOpt{withCredentialFile(), withDeps(func(d *Deps) {
		c, err := NewClaimer(ClaimerDeps{
			Handlers: reg, Inflight: InflightStore{Path: cr.inflight}, Audit: cr.audit, Online: d.Online,
			Clock: d.Clock, Rand: d.Rand, Logger: d.Logger, Recover: o.recover,
		})
		if err != nil {
			t.Fatal(err)
		}
		cr.claimer = c
		if !o.noLoop {
			d.Claim, d.Inflight = c, c.Inflight
		}
	})}, o.rig...)
	cr.rig = newRig(t, opts...)
	cr.srv.Handshake.HeartbeatIntervalS = 60
	cr.claimer.Bind(cr.link)
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = cr.claimer.Stop(c)
	})
	return cr
}

// start enrolls the link; with the loop on, the claimer starts claiming.
func (cr *claimRig) start() {
	cr.t.Helper()
	cr.enroll()
	cr.waitState(StateLinked)
}

// queue adds a job to the fake cloud with a fresh claim token.
func (cr *claimRig) queue(id, typ string, params any) fake.Job {
	cr.t.Helper()
	j := fake.Job{ID: id, Type: typ, Params: params, Token: "tok-" + id}
	cr.srv.QueueJob(j)
	return j
}

// dispatchNow runs a job through dispatch as if a claim had returned it (the loop is not running):
// the job is queued on the fake and marked running so its renew, result and failure are accepted.
func (cr *claimRig) dispatchNow(j fake.Job, renew time.Duration) {
	cr.t.Helper()
	cr.srv.QueueJob(j)
	cr.srv.MarkRunning(j.ID)
	sess, ok := cr.link.Session()
	if !ok {
		cr.t.Fatal("the link has no session")
	}
	raw, err := json.Marshal(j.Params)
	if err != nil {
		cr.t.Fatal(err)
	}
	cr.claimer.dispatch(cr.link, sess, []assignment{{ID: j.ID, Type: j.Type, Params: raw, Token: j.Token, RenewEvery: renew, Attempts: 1}}, 1)
}

// waitStarted blocks until a handler reports a start and returns the job.
func (h *testHandler) waitStarted(t *testing.T, ctx context.Context) Job {
	t.Helper()
	select {
	case j := <-h.started:
		return j
	case <-ctx.Done():
		t.Fatalf("handler %s did not start: %v", h.name, ctx.Err())
		return Job{}
	}
}

// jobDone returns the channel that closes when the claimer has finished everything about a job. Call it
// while the job runs.
func (c *Claimer) jobDone(id string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if j, ok := c.jobs[id]; ok {
		return j.done
	}
	done := make(chan struct{})
	close(done)
	return done
}

func (cr *claimRig) waitDone(done <-chan struct{}) {
	cr.t.Helper()
	select {
	case <-done:
	case <-cr.ctx.Done():
		cr.t.Fatalf("the job did not finish: %v", cr.ctx.Err())
	}
}

// waitJob blocks until the fake cloud has the job in status.
func (cr *claimRig) waitJob(id, status string) fake.JobRecord {
	cr.t.Helper()
	if err := cr.srv.WaitJob(cr.ctx, id, status); err != nil {
		cr.t.Fatal(err)
	}
	rec, _ := cr.srv.Job(id)
	return rec
}

// dueWithin blocks until some timer is due within d: the loop under test has armed its pause (the
// other loops' timers are minutes away). Use it before advancing the clock by less than d.
func (cr *claimRig) dueWithin(d time.Duration) {
	cr.t.Helper()
	if err := cr.clk.WaitDue(cr.ctx, d); err != nil {
		cr.t.Fatalf("no timer due within %v: %v", d, err)
	}
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi
}

// claimBody decodes the body of the n-th claim request.
type claimRequest struct {
	Types  []string `json:"types"`
	Limit  int      `json:"limit"`
	WaitMs *int     `json:"waitMs"`
}

func (cr *claimRig) claimRequest(n int) claimRequest {
	cr.t.Helper()
	reqs := cr.srv.RequestsFor(fake.RouteClaim)
	if len(reqs) <= n {
		cr.t.Fatalf("only %d claim requests", len(reqs))
	}
	var b claimRequest
	if err := reqs[n].JSON(&b); err != nil {
		cr.t.Fatal(err)
	}
	return b
}
