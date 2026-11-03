package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The claim loop (agent.md section 4.8.6): the agent pulls cloud commands, it never listens. One
// Claimer serves both planes: the interim localai plane (a long poll that returns when a command is
// queued) and the eab plane (a plain poll every cloud.poll.claim_seconds). Everything it does
// degrades into a backoff: a cloud that is down, slow or hostile never blocks a local feature, and
// the loop never spins (every pass waits on a timer or on the network).

// Timings of the claim loop.
const (
	// longPollGrace is added to waitMs for the HTTP timeout of a long poll.
	longPollGrace = 10 * time.Second
	// eabClaimTimeout is the HTTP timeout of a plain poll.
	eabClaimTimeout = 10 * time.Second
	// minReclaim is the shortest time between two long polls: an empty answer that came back at once
	// (the cloud answered without holding the request) is not repeated in a tight loop.
	minReclaim = 250 * time.Millisecond
	// idleWait is how long the loop waits when there is nothing to claim with (no session, no allowed
	// type, the node not online) before it looks again.
	idleWait = 5 * time.Second
	// recheckWait is how long it waits while the link is not in a state that claims (disabled, a
	// register in progress) or the slots are full, as a fallback for the wake-up.
	recheckWait = 30 * time.Second
	// maxClaimWaitMs is the longest long poll (agent.md section 4.8.6).
	maxClaimWaitMs = 25000
	// goingOfflineWait is how long an in-flight command may take to finish once the claim loop stops
	// (agent.md section 4.8.8, step 2).
	goingOfflineWait = 10 * time.Second
	// goingOfflineCall is the timeout of the failure that tells the cloud the node is going offline.
	goingOfflineCall = 3 * time.Second
	// recoverCall is the timeout of reporting a command an earlier process left behind.
	recoverCall = 5 * time.Second
	// maxJobIDLen bounds an id the cloud sends; it is put into a URL path.
	maxJobIDLen = 128
)

var jobIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// AuditSink is the audit log: *audit.Log implements it.
type AuditSink interface {
	Write(ev audit.Event) error
}

// ClaimLink is what the claim loop needs from the cloud link: *Link implements it.
type ClaimLink interface {
	// Session is the client and settings of the command plane; false while the link cannot talk.
	Session() (Session, bool)
	// State is the link state; the loop claims only while it is linked or backing off.
	State() State
	// ClaimResult tells the state machine how a claim ended (nil is a 200).
	ClaimResult(err error)
	// NoteCommandAuth reports a 401 seen on renew, result or failure.
	NoteCommandAuth(err error)
	// Counters are the heartbeat counters the loop increments.
	Counters() *Counters
	// PushNow sends a fresh state snapshot (step 7 of execute).
	PushNow(ctx context.Context) (StateRefresh, error)
}

var _ ClaimLink = (*Link)(nil)

// ClaimerDeps are what the claim loop needs. Handlers is required.
type ClaimerDeps struct {
	// Handlers are the job types the node can run (internal/cloud/jobs builds them).
	Handlers *Registry
	// Inflight is inflight.json.
	Inflight InflightStore
	// Audit receives one cloud.job line per command; nil writes none.
	Audit AuditSink
	// Online says whether the node is online: *modes.Engine.Online. Nil means online.
	Online func() bool
	// Clock drives every wait; nil is the wall clock.
	Clock Clock
	// Rand is the jitter of the claim backoff; nil is random.
	Rand RandDuration
	// Recover is called at the first start for each entry of inflight.json that an earlier process
	// left behind, before the default handling; it returns true when it took the entry over (it then
	// settles and removes it itself). AGENT-19 passes one for agent.update. Without it an
	// agent.update entry is left in the file untouched.
	Recover func(ctx context.Context, e InflightEntry) bool
	// Logger defaults to slog.Default; component=cloud is added.
	Logger *slog.Logger
}

// Claimer is the command plane: the claim loop, the execution of each command, the lease renewal and
// the settle. *Claimer is the ClaimLoop of the link (Start, Stop, Probe) and provides the link's
// Deps.Inflight. Create it with NewClaimer, create the link with Deps.Claim set to it, then call
// Bind with the link: the two need each other, so one of them is told about the other after the fact.
type Claimer struct {
	d   ClaimerDeps
	clk Clock
	log *slog.Logger
	rnd RandDuration

	slotCh chan struct{}
	wakeCh chan struct{}

	mu        sync.Mutex
	link      ClaimLink
	running   *loopRun
	jobs      map[string]*jobRun
	recovered bool

	probeMu sync.Mutex

	// fileMu guards the in-memory copy of inflight.json.
	fileMu      sync.Mutex
	fileLoaded  bool
	fileEntries map[string]InflightEntry
}

type loopRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

var _ ClaimLoop = (*Claimer)(nil)

// NewClaimer returns a Claimer that is not running.
func NewClaimer(d ClaimerDeps) (*Claimer, error) {
	if d.Handlers == nil {
		return nil, errors.New("cloud: the claim loop needs a registry of job handlers")
	}
	c := &Claimer{d: d, clk: d.Clock, rnd: d.Rand, log: d.Logger, jobs: map[string]*jobRun{},
		slotCh: make(chan struct{}, 1), wakeCh: make(chan struct{}, 1)}
	if c.clk == nil {
		c.clk = RealClock{}
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	c.log = c.log.With("component", "cloud")
	return c, nil
}

// Bind tells the claim loop which link it serves. It must be called before the link starts the loop
// (the loop does nothing without a link); calling it again replaces the link.
func (c *Claimer) Bind(l ClaimLink) {
	c.mu.Lock()
	c.link = l
	c.mu.Unlock()
}

func (c *Claimer) getLink() ClaimLink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link
}

func (c *Claimer) online() bool { return c.d.Online == nil || c.d.Online() }

// kick wakes the loop from an idle wait.
func (c *Claimer) kick() {
	select {
	case c.wakeCh <- struct{}{}:
	default:
	}
}

func (c *Claimer) freeSlotSignal() {
	select {
	case c.slotCh <- struct{}{}:
	default:
	}
}

// wait blocks for d on the clock, until ctx ends (false) or, when wake is set, until it fires.
func (c *Claimer) wait(ctx context.Context, d time.Duration, wake ...<-chan struct{}) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := c.clk.NewTimer(d)
	defer t.Stop()
	var w1, w2 <-chan struct{}
	if len(wake) > 0 {
		w1 = wake[0]
	}
	if len(wake) > 1 {
		w2 = wake[1]
	}
	select {
	case <-ctx.Done():
		return false
	case <-t.C():
		return true
	case <-w1:
		return true
	case <-w2:
		return true
	}
}

// ---------------------------------------------------------------------------------------------------
// ClaimLoop

// Start runs the claim loop until ctx ends or Stop is called. It returns at once; calling it while
// the loop runs does nothing, and it may be called again after Stop.
func (c *Claimer) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running != nil {
		select {
		case <-c.running.done:
			// The previous loop ended by itself (its context was cancelled): start a new one.
		default:
			return
		}
	}
	lctx, cancel := context.WithCancel(ctx)
	run := &loopRun{cancel: cancel, done: make(chan struct{})}
	c.running = run
	link := c.link
	go func() {
		defer close(run.done)
		defer cancel()
		c.loop(lctx, link)
	}()
}

// Stop is the claim part of the link stop (agent.md section 4.8.8): it cancels the loop (an
// in-progress long poll is cancelled, no new claims) and waits for it; then it gives a command that
// is still running up to 10 s to finish, and settles one that is not with failure
// "LAI-CLD-063: the node is going offline" (3 s), cancelling it when it can be cancelled (a model
// load carries on locally). A command that has already settled (mode.offline) is not waited for.
// Stop is idempotent. When ctx ends first, Stop returns its error and leaves the running commands
// alone: a link that only registers again keeps them and settles them later.
func (c *Claimer) Stop(ctx context.Context) error {
	c.mu.Lock()
	run := c.running
	c.running = nil
	c.mu.Unlock()
	if run != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return c.drain(ctx)
}

// drain waits for the unsettled commands and fails the ones that outlive goingOfflineWait.
func (c *Claimer) drain(ctx context.Context) error {
	pending := c.unsettled()
	if len(pending) == 0 {
		return nil
	}
	t := c.clk.NewTimer(goingOfflineWait)
	defer t.Stop()
	for _, j := range pending {
		select {
		case <-j.done:
		case <-t.C():
			c.failGoingOffline(ctx)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Claimer) unsettled() []*jobRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*jobRun
	for _, j := range c.jobs {
		if !j.isSettled() {
			out = append(out, j)
		}
	}
	slices.SortFunc(out, func(a, b *jobRun) int {
		if d := a.started.Compare(b.started); d != 0 {
			return d
		}
		return strings.Compare(a.id, b.id)
	})
	return out
}

// failGoingOffline settles every still unsettled command as failed and cancels the cancellable ones.
func (c *Claimer) failGoingOffline(ctx context.Context) {
	link := c.getLink()
	for _, j := range c.unsettled() {
		if !j.claimSettle() {
			continue
		}
		j.stopRenewing()
		if j.handler != nil && j.handler.Cancellable() {
			j.cancelRun()
		}
		c.log.Info("a cloud command was stopped because the node is going offline", "code", doctor.CLD063, "type", j.fullType, "job_id", j.id)
		if link != nil {
			link.Counters().Failed.Add(1)
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), goingOfflineCall)
			c.sendFailure(sctx, link, j.plane, j.id, j.token, failureText(doctor.CLD063, "the node is going offline"), 1)
			cancel()
		}
		c.writeAudit(j.id, j.fullType, audit.ResultFail, doctor.CLD063)
		c.removeInflight(j.id)
	}
}

// Inflight is the link's Deps.Inflight: the number of commands holding a slot and the record GET
// /cloud shows for the oldest (nil for none). The claim token is never part of it.
func (c *Claimer) Inflight() (int, map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.jobs) == 0 {
		return 0, nil
	}
	var oldest *jobRun
	for _, j := range c.jobs {
		if oldest == nil || j.started.Before(oldest.started) || (j.started.Equal(oldest.started) && j.id < oldest.id) {
			oldest = j
		}
	}
	return len(c.jobs), map[string]any{
		"job_id": oldest.id, "type": oldest.fullType, "plane": oldest.plane,
		"started_at": rfc3339(oldest.started), "lease_expires_at": rfc3339(oldest.lease),
	}
}

// ---------------------------------------------------------------------------------------------------
// The loop

type assignment struct {
	ID         string
	Type       string
	Attempts   int
	Params     json.RawMessage
	Token      string
	RenewEvery time.Duration
	Lease      time.Time
}

type claimWire struct {
	Jobs []struct {
		Job struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Attempts int    `json:"attempts"`
		} `json:"job"`
		Params          json.RawMessage `json:"params"`
		ClaimToken      *string         `json:"claimToken"`
		RenewIntervalMs int             `json:"renewIntervalMs"`
		LeaseExpiresAt  string          `json:"leaseExpiresAt"`
	} `json:"jobs"`
}

// Renew interval bounds: the default of the spec, and limits for a value the cloud sends.
const (
	defaultRenewEvery = 20 * time.Second
	minRenewEvery     = 5 * time.Second
	maxRenewEvery     = 60 * time.Second
)

func (c *Claimer) loop(ctx context.Context, link ClaimLink) {
	if link == nil {
		c.log.Warn("the command loop has no link to serve and does nothing")
		return
	}
	c.recoverOnce(ctx, link)
	bo := NewBackoff(c.rnd)
	for ctx.Err() == nil {
		if !c.online() {
			if !c.wait(ctx, idleWait) {
				return
			}
			continue
		}
		if st := link.State(); st != StateLinked && st != StateBackoff {
			// Disabled (the heartbeat's Probe re-checks), registering, revoked: nothing to claim with.
			if !c.wait(ctx, recheckWait, c.wakeCh) {
				return
			}
			continue
		}
		sess, ok := link.Session()
		types := c.claimTypes(sess)
		if !ok || len(types) == 0 {
			if !c.wait(ctx, idleWait, c.wakeCh) {
				return
			}
			continue
		}
		free := c.freeSlots(sess)
		if free == 0 {
			if !c.wait(ctx, recheckWait, c.slotCh) {
				return
			}
			continue
		}
		started := c.clk.Now()
		jobs, waitMs, err := c.claim(ctx, sess, types, free)
		if ctx.Err() != nil {
			// Stop cancelled the long poll: that is not a failure of the cloud.
			return
		}
		if !c.afterClaim(link, err) {
			if !c.wait(ctx, bo.Fail(retryAfterOf(err))) {
				return
			}
			continue
		}
		bo.Reset()
		if len(jobs) == 0 {
			link.Counters().EmptyPolls.Add(1)
			if !c.pace(ctx, sess, waitMs, started) {
				return
			}
			continue
		}
		c.dispatch(link, sess, jobs, free)
	}
}

// afterClaim feeds the outcome of a claim to the link's state machine and the counters; it reports
// whether the claim succeeded.
func (c *Claimer) afterClaim(link ClaimLink, err error) bool {
	if err != nil {
		// The counters first: whoever sees the link's new state sees them.
		if ae := asAPIError(err); ae != nil && ae.Status == 429 {
			link.Counters().RateLimited.Add(1)
		} else {
			link.Counters().ClaimFailures.Add(1)
		}
		c.log.Debug("a claim failed", "error", errText(err))
	}
	link.ClaimResult(err)
	return err == nil
}

// claimTypes are the job names a claim asks for: cloud.allow, in its order, limited to what this
// build can run.
func (c *Claimer) claimTypes(s Session) []string {
	var out []string
	for _, a := range s.Allow {
		if _, ok := c.d.Handlers.Lookup(a); ok && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// concurrency is how many commands may run at once. The localai plane runs one: the cloud hands a
// node at most one running command and clamps limit to 1 (cloud-app.md section 4.4).
func concurrency(s Session) int {
	if s.Plane == PlaneEAB && s.Concurrency > 1 {
		return s.Concurrency
	}
	return 1
}

func (c *Claimer) freeSlots(s Session) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := concurrency(s) - len(c.jobs); n > 0 {
		return n
	}
	return 0
}

// claim sends one claim request and returns the assignments with the waitMs it used.
func (c *Claimer) claim(ctx context.Context, s Session, types []string, free int) ([]assignment, int, error) {
	var path string
	var body map[string]any
	var timeout time.Duration
	waitMs := 0
	if s.Plane == PlaneEAB {
		prefixed := make([]string, len(types))
		for i, t := range types {
			prefixed[i] = TypePrefix + t
		}
		path = "/api/nodes/" + pathEscape(s.ServerNodeID) + "/claim"
		body = map[string]any{"types": prefixed, "limit": free}
		timeout = eabClaimTimeout
	} else {
		waitMs = min(maxClaimWaitMs, s.ClaimWaitMaxMs)
		path = "/api/nodes/" + pathEscape(s.ServerNodeID) + "/localai/commands/claim"
		body = map[string]any{"types": types, "limit": 1, "waitMs": waitMs}
		timeout = time.Duration(waitMs)*time.Millisecond + longPollGrace
	}
	reply, err := s.Client.Call(ctx, "POST", path, body, timeout)
	if err != nil {
		return nil, waitMs, err
	}
	var w claimWire
	if err := reply.Decode(&w); err != nil {
		return nil, waitMs, errors.New("cloud: the claim answer is not valid")
	}
	out := make([]assignment, 0, len(w.Jobs))
	for _, j := range w.Jobs {
		a := assignment{ID: j.Job.ID, Type: j.Job.Type, Attempts: j.Job.Attempts, Params: j.Params, RenewEvery: defaultRenewEvery}
		if j.ClaimToken != nil && len(*j.ClaimToken) <= 256 {
			a.Token = *j.ClaimToken
		}
		if j.RenewIntervalMs > 0 {
			a.RenewEvery = min(max(time.Duration(j.RenewIntervalMs)*time.Millisecond, minRenewEvery), maxRenewEvery)
		}
		if t, err := time.Parse(time.RFC3339, j.LeaseExpiresAt); err == nil {
			a.Lease = t
		}
		out = append(out, a)
	}
	return out, waitMs, nil
}

// pace waits before the next claim after an empty answer: at least 250 ms after the previous claim
// started on the long-poll plane, cloud.poll.claim_seconds on the plain poll (and on a long-poll
// plane whose cloud holds no request at all).
func (c *Claimer) pace(ctx context.Context, s Session, waitMs int, started time.Time) bool {
	if s.Plane != PlaneEAB && waitMs > 0 {
		return c.wait(ctx, minReclaim-c.clk.Now().Sub(started))
	}
	secs := s.ClaimPollSeconds
	if secs < 1 {
		secs = 3
	}
	return c.wait(ctx, time.Duration(secs)*time.Second)
}

// dispatch starts the commands of an answer. Anything beyond the free slots is refused: it was
// claimed on the cloud and must not be left to time out.
func (c *Claimer) dispatch(link ClaimLink, s Session, jobs []assignment, free int) {
	for i, a := range jobs {
		link.Counters().Claims.Add(1)
		if !jobIDRE.MatchString(a.ID) {
			// Nothing can be settled without an id that is safe in a path.
			c.log.Warn("the cloud sent a command without a usable id", "code", doctor.CLD058)
			continue
		}
		if i >= free {
			c.refuse(link, a, doctor.CLD058, "more commands than requested", audit.ResultDenied)
			continue
		}
		c.start(link, s, a)
	}
}

// refuse settles a command the loop will not run (no slot): failure with the code, in the background.
func (c *Claimer) refuse(link ClaimLink, a assignment, code, msg string, result audit.Result) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*callTimeout)
		defer cancel()
		plane := PlaneLocalai
		if s, ok := link.Session(); ok {
			plane = s.Plane
		}
		c.sendFailure(ctx, link, plane, a.ID, a.Token, failureText(code, msg), 1)
		link.Counters().Failed.Add(1)
		c.writeAudit(a.ID, printable(a.Type, 64), result, code)
	}()
}

// Probe is called by the link after each heartbeat while the node is disabled: one claim without a
// long poll, to learn whether the cloud accepts the node again (agent.md section 4.8.1). A command it
// returns is run like any other.
func (c *Claimer) Probe(ctx context.Context) {
	link := c.getLink()
	if link == nil || link.State() != StateDisabled || !c.online() {
		return
	}
	c.mu.Lock()
	running := c.running != nil
	c.mu.Unlock()
	if !running {
		return
	}
	c.probeMu.Lock()
	defer c.probeMu.Unlock()
	sess, ok := link.Session()
	types := c.claimTypes(sess)
	if !ok || len(types) == 0 {
		return
	}
	free := c.freeSlots(sess)
	if free == 0 {
		return
	}
	probe := sess
	probe.ClaimWaitMaxMs = 0
	jobs, _, err := c.claim(ctx, probe, types, free)
	if ctx.Err() != nil {
		return
	}
	if !c.afterClaim(link, err) {
		return
	}
	c.kick()
	if len(jobs) == 0 {
		link.Counters().EmptyPolls.Add(1)
		return
	}
	c.dispatch(link, sess, jobs, free)
}

// errNoSession is what a settle reports while the link cannot talk.
var errNoSession = ErrNotLinked
