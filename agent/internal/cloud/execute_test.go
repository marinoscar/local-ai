package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The execute(a) pipeline: policy, strict decode, persist, renew, run, settle, cleanup. Most tests
// drive it through dispatch (the loop is not running), so each job is one deterministic run.

func titleOf(code string) string {
	t, _, _ := doctor.Text(code, "en")
	return t
}

// A type the node does not allow, or does not know, is refused with LAI-CLD-057 and nothing runs; the
// text from the cloud is never echoed raw.
func TestRefusedTypesAreNeverRun(t *testing.T) {
	ran := make(chan string, 4)
	handler := func(name string) *testHandler {
		return newTestHandler(name, func(_ context.Context, j Job, _ any) (Outcome, error) {
			ran <- j.Type
			return Outcome{Result: resultFor(name)}, nil
		})
	}
	cr := newClaimRig(t, []*testHandler{handler(TypeStatusRefresh), handler(TypeModelDelete)}, withoutLoop(),
		withRigOpts(func(o *rigOptions) { o.allow = []string{"status.refresh", "model.load"} }))
	cr.start()

	long := "localai." + strings.Repeat("x", 100) + "\n\x07"
	tests := []struct {
		name, typ, wantText string
	}{
		{"known type, not in cloud.allow", "localai.model.delete", "LAI-CLD-057: this node does not allow localai.model.delete"},
		{"in cloud.allow, no handler in this build", "localai.model.load", "LAI-CLD-057: this node does not allow localai.model.load"},
		{"unknown type", "localai.shell.exec", "LAI-CLD-057: this node does not allow localai.shell.exec"},
		{"no namespace", "status.refresh", "LAI-CLD-057: this node does not allow status.refresh"},
		{"another namespace", "other.status.refresh", "LAI-CLD-057: this node does not allow other.status.refresh"},
		{"the prefix alone", "localai.", "LAI-CLD-057: this node does not allow localai."},
		{"long hostile text is cut and printable", long, "LAI-CLD-057: this node does not allow " + "localai." + strings.Repeat("x", 56)},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("job-p%d", i)
			cr.dispatchNow(fake.Job{ID: id, Type: tc.typ, Params: map[string]any{}, Token: "tok-" + id}, 20*time.Second)
			rec := cr.waitJob(id, fake.JobFailed)
			if rec.Error != tc.wantText {
				t.Errorf("failure = %q, want %q", rec.Error, tc.wantText)
			}
		})
	}
	select {
	case typ := <-ran:
		t.Errorf("%s was executed", typ)
	default:
	}
	evs := cr.audit.wait(t, cr.ctx, len(tests))
	for _, ev := range evs {
		if ev.Action != audit.ActionCloudJob || ev.Result != audit.ResultDenied || ev.Code != doctor.CLD057 || ev.Actor.Kind != audit.KindCloud {
			t.Errorf("audit = %+v", ev)
		}
	}
	if n := cr.link.Counters().Failed.Load(); n != int64(len(tests)) {
		t.Errorf("failed = %d", n)
	}
}

// Params are decoded strictly: an unknown or mis-cased key, a wrong type, a null and an oversize
// object are LAI-CLD-058 with the field path, and the handler never runs.
func TestStrictDecodeFailuresAreCLD058(t *testing.T) {
	h := okHandler()
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	tests := []struct {
		name   string
		params any
		want   string
	}{
		{"unknown field", map[string]any{"x": 1}, "LAI-CLD-058: x: unknown field"},
		{"key in another case", map[string]any{"N": 1}, "LAI-CLD-058: N: unknown field"},
		{"wrong type", map[string]any{"n": "5"}, "LAI-CLD-058: n: must be an integer"},
		{"a float", map[string]any{"n": 1.5}, "LAI-CLD-058: n: must be an integer"},
		{"null", map[string]any{"n": nil}, "LAI-CLD-058: n: must not be null"},
		{"an array", []any{1}, "LAI-CLD-058: params: must be an object"},
		{"too large", map[string]any{"n": 1, "pad": strings.Repeat("x", MaxParamsBytes)}, "LAI-CLD-058: params: larger than 64 KiB"},
		{"hostile key text is cut", map[string]any{strings.Repeat("k", 100) + "\n": 1}, "LAI-CLD-058: " + strings.Repeat("k", 64) + ": unknown field"},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("job-d%d", i)
			cr.dispatchNow(fake.Job{ID: id, Type: "localai.status.refresh", Params: tc.params, Token: "tok-" + id}, 20*time.Second)
			if rec := cr.waitJob(id, fake.JobFailed); rec.Error != tc.want {
				t.Errorf("failure = %q, want %q", rec.Error, tc.want)
			}
		})
	}
	if len(h.started) != 0 {
		t.Error("a handler ran with params that did not decode")
	}
	// Valid params (null params count as {}) do run.
	cr.dispatchNow(fake.Job{ID: "job-ok", Type: "localai.status.refresh", Params: map[string]any{"n": 3}, Token: "tok-ok"}, 20*time.Second)
	cr.waitJob("job-ok", fake.JobSucceeded)
	cr.dispatchNow(fake.Job{ID: "job-null", Type: "localai.status.refresh", Params: nil, Token: "tok-null"}, 20*time.Second)
	cr.waitJob("job-null", fake.JobSucceeded)
}

// An error of an executor is the failure "<code>: <message>": the code of an API error with its own
// message or the code's title, a handler's own code, a plain error as LAI-AGT-099 without its text, a
// panic recovered (and the loop goes on), a text scrubbed and cut to 2000 characters.
func TestExecutorErrorsBecomeFailuresWithACode(t *testing.T) {
	secret := strings.Repeat("ab12", 16)
	cases := []struct {
		name string
		run  func() (Outcome, error)
		want string
		code string
	}{
		{"api error without a message", func() (Outcome, error) { return Outcome{}, api.NewError(doctor.AGT021, 404, nil) },
			"LAI-AGT-021: " + titleOf(doctor.AGT021), doctor.AGT021},
		{"api error with a message", func() (Outcome, error) {
			return Outcome{}, api.NewError(doctor.AGT022, 409, nil).WithMessage("Another model operation is running.", "")
		}, "LAI-AGT-022: Another model operation is running.", doctor.AGT022},
		{"a wrapped api error", func() (Outcome, error) {
			return Outcome{}, fmt.Errorf("while loading: %w", api.NewError(doctor.AGT023, 409, nil))
		}, "LAI-AGT-023: " + titleOf(doctor.AGT023), doctor.AGT023},
		{"the handler's own code", func() (Outcome, error) {
			return Outcome{}, &JobError{Code: doctor.CAT005, Message: "Model does not fit in free VRAM"}
		}, "LAI-CAT-005: Model does not fit in free VRAM", doctor.CAT005},
		{"a plain error keeps its text out of the failure", func() (Outcome, error) {
			return Outcome{}, errors.New("open /home/someone/models/x.gguf: permission denied")
		}, "LAI-AGT-099: The command failed with an unexpected error; the agent log has the details.", doctor.AGT099},
		{"a deadline", func() (Outcome, error) { return Outcome{}, fmt.Errorf("wait: %w", context.DeadlineExceeded) },
			"LAI-AGT-099: The command did not finish in the time allowed for it.", doctor.AGT099},
		{"a panic", func() (Outcome, error) { panic("boom at /home/someone/secret") },
			"LAI-AGT-099: The command failed with an internal error.", doctor.AGT099},
		{"a secret in the message is scrubbed", func() (Outcome, error) {
			return Outcome{}, &JobError{Code: doctor.AGT021, Message: "key " + secret + " is wrong"}
		}, "", doctor.AGT021},
		{"a long message is cut", func() (Outcome, error) {
			return Outcome{}, &JobError{Code: doctor.AGT021, Message: strings.Repeat("é", 3000)}
		}, "", doctor.AGT021},
	}
	h := newTestHandler(TypeStatusRefresh, func(_ context.Context, _ Job, p any) (Outcome, error) {
		return cases[*p.(testParams).N].run()
	})
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("job-e%d", i)
			cr.dispatchNow(fake.Job{ID: id, Type: "localai.status.refresh", Params: map[string]any{"n": i}, Token: "tok-" + id}, 20*time.Second)
			rec := cr.waitJob(id, fake.JobFailed)
			if tc.want != "" && rec.Error != tc.want {
				t.Errorf("failure = %q, want %q", rec.Error, tc.want)
			}
			if !strings.HasPrefix(rec.Error, tc.code+": ") {
				t.Errorf("failure = %q: the code must lead", rec.Error)
			}
			if strings.Contains(rec.Error, secret) || strings.Contains(rec.Error, "/home/someone") {
				t.Errorf("failure %q leaks", rec.Error)
			}
			if n := utf8.RuneCountInString(rec.Error); n > MaxFailureChars {
				t.Errorf("failure has %d characters", n)
			}
		})
	}
	evs := cr.audit.wait(t, cr.ctx, len(cases))
	for _, ev := range evs {
		if ev.Result != audit.ResultFail || ev.Code == "" {
			t.Errorf("audit = %+v", ev)
		}
	}
}

// A handler that panics fails its command and nothing else: the next command runs.
func TestAPanickingHandlerDoesNotStopTheLoop(t *testing.T) {
	calls := 0
	h := newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
		calls++
		if calls == 1 {
			panic("first call")
		}
		return Outcome{Result: refreshResult()}, nil
	})
	cr := newClaimRig(t, []*testHandler{h})
	cr.queue("job-1", "localai.status.refresh", map[string]any{})
	cr.queue("job-2", "localai.status.refresh", map[string]any{})
	cr.start()
	cr.waitJob("job-1", fake.JobFailed)
	cr.waitJob("job-2", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-2"))
	if entries, _ := cr.claimer.d.Inflight.Load(); len(entries) != 0 {
		t.Errorf("inflight = %v", entries)
	}
}

// ---------------------------------------------------------------------------------------------------
// Settle rules

// A 409 on the result is final: the work is dropped and the result is never sent again, however long
// the clock runs.
func TestResultAnswered409IsDroppedAndNeverRetried(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
	cr.start()
	cr.srv.Script(fake.RouteResult, fake.Err(409, "", ""))
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	cr.waitCount(fake.RouteResult, 1)
	cr.clk.Advance(time.Second)
	cr.clk.Advance(5 * time.Second)
	cr.clk.Advance(15 * time.Second)
	// A later job settles after any stray retry of the first would have gone out.
	cr.dispatchNow(fake.Job{ID: "job-2", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-2"}, 20*time.Second)
	cr.waitJob("job-2", fake.JobSucceeded)
	var paths []string
	for _, r := range cr.srv.RequestsFor(fake.RouteResult) {
		paths = append(paths, r.Path[strings.LastIndex(r.Path, "commands/")+9:])
	}
	if strings.Join(paths, " ") != "job-1/result job-2/result" {
		t.Errorf("result requests = %v", paths)
	}
}

func TestFailureAnswered409IsDroppedToo(t *testing.T) {
	h := newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
		return Outcome{}, &JobError{Code: doctor.AGT021, Message: "gone"}
	})
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.srv.Script(fake.RouteFailure, fake.Err(409, "", ""))
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	cr.waitCount(fake.RouteFailure, 1)
	cr.waitDone(cr.claimer.jobDone("job-1"))
	cr.clk.Advance(time.Minute)
	if n := cr.srv.Count(fake.RouteFailure); n != 1 {
		t.Errorf("failure requests = %d", n)
	}
}

// A settle that got no answer is repeated three times, after 1 s, 5 s and 15 s, and then dropped.
func TestSettleWithoutAnAnswerRetriesAtOneFiveFifteenSeconds(t *testing.T) {
	t.Run("the third repeat gets through", func(t *testing.T) {
		cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
		cr.start()
		cr.srv.Script(fake.RouteResult, fake.Reply{Drop: true}, fake.Reply{Drop: true}, fake.Reply{Drop: true})
		cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
		cr.waitCount(fake.RouteResult, 1)
		cr.step(time.Second)
		cr.waitCount(fake.RouteResult, 2)
		cr.step(5 * time.Second)
		cr.waitCount(fake.RouteResult, 3)
		cr.step(15 * time.Second)
		cr.waitJob("job-1", fake.JobSucceeded)
		if n := cr.srv.Count(fake.RouteResult); n != 4 {
			t.Errorf("result requests = %d, want 4", n)
		}
	})
	t.Run("after the third repeat it is dropped", func(t *testing.T) {
		cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
		cr.start()
		cr.srv.Forever(fake.RouteResult, fake.Reply{Drop: true})
		cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
		done := cr.claimer.jobDone("job-1")
		cr.waitCount(fake.RouteResult, 1)
		cr.step(time.Second)
		cr.waitCount(fake.RouteResult, 2)
		cr.step(5 * time.Second)
		cr.waitCount(fake.RouteResult, 3)
		cr.step(15 * time.Second)
		cr.waitDone(done)
		if n := cr.srv.Count(fake.RouteResult); n != 4 {
			t.Errorf("result requests = %d, want 4", n)
		}
		if entries, _ := cr.claimer.d.Inflight.Load(); len(entries) != 0 {
			t.Errorf("inflight = %v: a dropped settle still ends the command", entries)
		}
	})
	t.Run("a 503 is repeated like a lost answer", func(t *testing.T) {
		cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
		cr.start()
		cr.srv.Script(fake.RouteResult, fake.Reply{Status: 503})
		cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
		cr.waitCount(fake.RouteResult, 1)
		cr.step(time.Second)
		cr.waitJob("job-1", fake.JobSucceeded)
	})
}

// A result the cloud refuses (400) is replaced by a failure with LAI-CLD-012, once.
func TestResultRefusedBy400BecomesAFailure(t *testing.T) {
	bad := newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
		return Outcome{Result: map[string]any{"seq": "one"}}, nil // not the schema's shape
	})
	cr := newClaimRig(t, []*testHandler{bad}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	rec := cr.waitJob("job-1", fake.JobFailed)
	if !strings.HasPrefix(rec.Error, "LAI-CLD-012: ") {
		t.Errorf("failure = %q", rec.Error)
	}
	if cr.srv.Count(fake.RouteResult) != 1 || cr.srv.Count(fake.RouteFailure) != 1 {
		t.Errorf("results %d failures %d", cr.srv.Count(fake.RouteResult), cr.srv.Count(fake.RouteFailure))
	}
}

func TestResultThatCannotBeSentFailsTheCommand(t *testing.T) {
	cases := map[string]any{
		"too large":   strings.Repeat("x", MaxResultBytes+1),
		"not encoded": make(chan int),
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			h := newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
				return Outcome{Result: result}, nil
			})
			cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
			cr.start()
			cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
			rec := cr.waitJob("job-1", fake.JobFailed)
			if !strings.HasPrefix(rec.Error, "LAI-AGT-099: ") {
				t.Errorf("failure = %q", rec.Error)
			}
			if cr.srv.Count(fake.RouteResult) != 0 {
				t.Error("a result went out")
			}
		})
	}
}

func TestA401OnTheResultRevokesTheNode(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
	cr.start()
	cr.srv.Script(fake.RouteResult, fake.Reply{Status: 401})
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	cr.waitState(StateRevoked)
}

// ---------------------------------------------------------------------------------------------------
// Renewal

func TestRenewalRunsWhileTheOperationRunsAndStopsAt409(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 7*time.Second)
	h.waitStarted(t, cr.ctx)
	cr.claimer.mu.Lock()
	j := cr.claimer.jobs["job-1"]
	cr.claimer.mu.Unlock()

	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 1)
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 2)
	for _, r := range cr.srv.RequestsFor(fake.RouteRenew) {
		if string(r.Body) != `{"claimToken":"tok-1"}` {
			t.Errorf("renew body = %s", r.Body)
		}
	}
	// The renewed lease is on disk for a later process (this also waits for the loop to count it).
	cr.waitInflightLease(cr.clk.Now().Add(time.Minute))
	if c := cr.link.Counters(); c.LeaseRenewals.Load() != 2 {
		t.Errorf("renewals = %d", c.LeaseRenewals.Load())
	}

	// The cloud moves on: the next renewal is a 409, which stops the renewing; the operation goes on.
	cr.srv.LoseLease("job-1")
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 3)
	cr.waitDone(j.renewExited)
	if !j.leaseLost.Load() {
		t.Error("a 409 on a renewal did not mark the lease lost")
	}
	cr.clk.Advance(time.Minute)
	if n := cr.srv.Count(fake.RouteRenew); n != 3 {
		t.Errorf("renewals after the 409 = %d, want 3", n)
	}
	if c := cr.link.Counters(); c.LeaseRenewFailures.Load() != 1 {
		t.Errorf("renew failures = %d", c.LeaseRenewFailures.Load())
	}
	// The operation finishes cleanly and its result is offered once; the cloud answers 409; no retry.
	done := cr.claimer.jobDone("job-1")
	close(release)
	cr.waitDone(done)
	if n := cr.srv.Count(fake.RouteResult); n != 1 {
		t.Errorf("result requests = %d, want 1", n)
	}
	if rec, _ := cr.srv.Job("job-1"); rec.Status != fake.JobRunning {
		t.Errorf("the cloud has the job %s", rec.Status)
	}
}

// waitInflightLease checks that inflight.json holds the renewed lease. The renewal loop writes it
// before it arms the timer of the next renewal, so once that timer is armed the write is done.
func (cr *claimRig) waitInflightLease(want time.Time) {
	cr.t.Helper()
	cr.dueWithin(8 * time.Second)
	entries, err := cr.claimer.d.Inflight.Load()
	if err != nil || len(entries) != 1 || !entries[0].LeaseExpiresAt.Equal(want) {
		cr.t.Errorf("inflight = %+v %v, want lease %v", entries, err, want)
	}
}

func TestFailedRenewalsAreNotFatal(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.srv.Script(fake.RouteRenew, fake.Reply{Status: 503}, fake.Reply{Drop: true})
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 7*time.Second)
	h.waitStarted(t, cr.ctx)
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 1)
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 2)
	cr.step(7 * time.Second)
	cr.waitCount(fake.RouteRenew, 3)
	cr.dueWithin(8 * time.Second) // the loop has handled the answer and armed the next renewal
	done := cr.claimer.jobDone("job-1")
	close(release)
	cr.waitJob("job-1", fake.JobSucceeded)
	cr.waitDone(done)
	c := cr.link.Counters()
	if c.LeaseRenewFailures.Load() != 2 || c.LeaseRenewals.Load() != 1 {
		t.Errorf("failures %d renewals %d", c.LeaseRenewFailures.Load(), c.LeaseRenewals.Load())
	}
}

func TestA401OnARenewalRevokesTheNode(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.srv.Script(fake.RouteRenew, fake.Reply{Status: 401})
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 7*time.Second)
	h.waitStarted(t, cr.ctx)
	cr.step(7 * time.Second)
	cr.waitState(StateRevoked)
	// With the node revoked there is no session to settle on: the result is repeated at 1, 5 and 15 s
	// and then dropped, and the operation still ends cleanly.
	done := cr.claimer.jobDone("job-1")
	close(release)
	cr.step(time.Second)
	cr.step(5 * time.Second)
	cr.step(15 * time.Second)
	cr.waitDone(done)
}

// The renew interval comes from the cloud and is bounded: a hostile 1 ms is 5 s, a lazy hour is 60 s.
func TestRenewIntervalFromTheCloudIsBounded(t *testing.T) {
	cr := newClaimRig(t, []*testHandler{okHandler()}, withoutLoop())
	cr.start()
	sess, _ := cr.link.Session()
	sess.Plane = PlaneEAB // the plain poll hands out a job per claim whatever is running
	for i, tc := range []struct {
		ms   int
		want time.Duration
	}{{1, 5 * time.Second}, {20000, 20 * time.Second}, {3_600_000, 60 * time.Second}, {0, 20 * time.Second}} {
		id := fmt.Sprintf("job-%d", i)
		cr.srv.QueueJob(fake.Job{ID: id, Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok", RenewIntervalMs: tc.ms})
		got, _, err := cr.claimer.claim(cr.ctx, sess, []string{"status.refresh"}, 1)
		if err != nil || len(got) != 1 {
			t.Fatalf("claim = %v %v", got, err)
		}
		if got[0].RenewEvery != tc.want {
			t.Errorf("renewIntervalMs %d: interval %v, want %v", tc.ms, got[0].RenewEvery, tc.want)
		}
		if got[0].Token != "tok" || got[0].ID != id {
			t.Errorf("assignment = %+v", got[0])
		}
	}
}

// ---------------------------------------------------------------------------------------------------
// Guards of the loop

// A command delivered twice is run once: commands are at most once.
func TestADuplicateDeliveryIsIgnored(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	j := fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}
	cr.srv.QueueJob(j)
	cr.srv.MarkRunning("job-1")
	sess, _ := cr.link.Session()
	a := assignment{ID: "job-1", Type: j.Type, Params: json.RawMessage(`{}`), Token: j.Token, RenewEvery: 20 * time.Second}
	cr.claimer.dispatch(cr.link, sess, []assignment{a, a}, 2)
	h.waitStarted(t, cr.ctx)
	if n, _ := cr.claimer.Inflight(); n != 1 {
		t.Errorf("in flight = %d, want 1", n)
	}
	close(release)
	cr.waitJob("job-1", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-1"))
	if len(h.started) != 0 {
		t.Error("the handler ran twice")
	}
	if n := cr.srv.Count(fake.RouteResult); n != 1 {
		t.Errorf("results = %d", n)
	}
}

// A cloud that sends more commands than the node has slots gets the extra ones failed, not left to
// time out.
func TestCommandsBeyondTheFreeSlotsAreRefused(t *testing.T) {
	release := make(chan struct{})
	h := blockingHandler(TypeStatusRefresh, release, false)
	cr := newClaimRig(t, []*testHandler{h})
	cr.srv.QueueJob(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1", Force: true})
	cr.srv.QueueJob(fake.Job{ID: "job-2", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-2", Force: true})
	cr.start()
	h.waitStarted(t, cr.ctx)
	rec := cr.waitJob("job-2", fake.JobFailed)
	if rec.Error != "LAI-CLD-058: more commands than requested" {
		t.Errorf("failure = %q", rec.Error)
	}
	close(release)
	cr.waitJob("job-1", fake.JobSucceeded)
	cr.waitDone(cr.claimer.jobDone("job-1"))
}

// An assignment whose id cannot be put in a path is dropped without a word to the cloud.
func TestAnAssignmentWithAnUnsafeIDIsDropped(t *testing.T) {
	h := okHandler()
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	sess, _ := cr.link.Session()
	for _, id := range []string{"", "../x", "a/b", "id with space", strings.Repeat("a", 129)} {
		cr.claimer.dispatch(cr.link, sess, []assignment{{ID: id, Type: "localai.status.refresh", Params: json.RawMessage(`{}`)}}, 1)
	}
	if n, _ := cr.claimer.Inflight(); n != 0 {
		t.Errorf("in flight = %d", n)
	}
	if len(h.started) != 0 || cr.srv.Count(fake.RouteResult) != 0 || cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("something ran or was settled for a bad id")
	}
}

// A command whose type needs inflight.json is refused when the file cannot be written; any other type
// runs without it.
func TestInflightWriteFailure(t *testing.T) {
	for _, needs := range []bool{false, true} {
		t.Run(fmt.Sprintf("needs=%v", needs), func(t *testing.T) {
			h := okHandler()
			h.needsInflight = needs
			cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
			cr.start()
			// A directory where the file should be makes every write fail.
			cr.claimer.d.Inflight = InflightStore{Path: t.TempDir()}
			cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
			if needs {
				if rec := cr.waitJob("job-1", fake.JobFailed); !strings.HasPrefix(rec.Error, "LAI-AGT-099: ") {
					t.Errorf("failure = %q", rec.Error)
				}
				if len(h.started) != 0 {
					t.Error("the handler ran without its record")
				}
				return
			}
			cr.waitJob("job-1", fake.JobSucceeded)
		})
	}
}

// The deferred outcome of agent.update: no result, no failure, the entry stays for the next process.
func TestDeferredOutcomeLeavesTheEntryAndSettlesNothing(t *testing.T) {
	h := newTestHandler(TypeStatusRefresh, func(context.Context, Job, any) (Outcome, error) {
		return Outcome{Defer: true}, nil
	})
	cr := newClaimRig(t, []*testHandler{h}, withoutLoop())
	cr.start()
	cr.dispatchNow(fake.Job{ID: "job-1", Type: "localai.status.refresh", Params: map[string]any{}, Token: "tok-1"}, 20*time.Second)
	h.waitStarted(t, cr.ctx)
	cr.waitDone(cr.claimer.jobDone("job-1"))
	if cr.srv.Count(fake.RouteResult)+cr.srv.Count(fake.RouteFailure) != 0 {
		t.Error("a deferred command was settled")
	}
	entries, _ := cr.claimer.d.Inflight.Load()
	if len(entries) != 1 || entries[0].JobID != "job-1" || entries[0].ClaimToken != "tok-1" {
		t.Errorf("inflight = %+v", entries)
	}
	evs := cr.audit.wait(t, cr.ctx, 1)
	if evs[0].Result != "deferred" {
		t.Errorf("audit = %+v", evs[0])
	}
}
