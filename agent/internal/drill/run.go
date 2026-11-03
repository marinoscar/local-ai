package drill

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Errors of the engine. Start also returns *RefusedError and *InvalidRequestError.
var (
	// ErrNoRun: Abort found no drill running (or not the one it was asked for).
	ErrNoRun = errors.New("drill: no drill is running")
	// ErrClosing: the engine is shutting down and starts no run.
	ErrClosing = errors.New("drill: the agent is stopping")

	errAborted  = errors.New("aborted by request")
	errShutdown = errors.New("the agent was stopping")
)

// InvalidRequestError is a Start request the engine cannot honor: an unknown trigger, a bad --only
// list or a --only entry that matches no check.
type InvalidRequestError struct {
	Field   string
	Reason  string
	Unknown []string
}

func (e *InvalidRequestError) Error() string {
	return fmt.Sprintf("drill: invalid %s: %s", e.Field, e.Reason)
}

// Publisher receives the drill events; *events.Bus implements it.
type Publisher interface {
	Publish(typ, opID string, data any) error
}

// FenceReport is what the network fence saw while it was on (drill.md section 4.4).
type FenceReport struct {
	BlockedDials int
	// Callers name who tried to dial out.
	Callers []string
}

// Fence is the network fence (DRILL-02): while a run is active the agent's outbound transport
// refuses every non-loopback dial. Enable is called before the first check and Disable after the last
// (and on every exit); Disable reports what was refused.
type Fence interface {
	Enable()
	Disable() FenceReport
}

// Restorer captures and restores the node's model state (DRILL-03, drill.md section 4.3). Capture runs
// before the first check; the result is persisted in state.json and handed back to Restore on the
// way out of every exit, and after a crash by Recover. Restore must be idempotent: after a crash it
// can run again for a state that was already restored.
type Restorer interface {
	Capture(ctx context.Context) (Captured, error)
	Restore(ctx context.Context, c Captured) (Restored, error)
}

// Metric names (drill.md section 7).
const (
	MetricLastOverall  = "localai_drill_last_overall"            // 0 pass, 1 warn, 2 fail
	MetricLastFinished = "localai_drill_last_finished_timestamp" // unix seconds
	MetricCheckMS      = "localai_drill_check_ms"                // label check
)

// Metrics receives the drill gauges. The agent has no Prometheus endpoint in phase 1 (agent.md
// section 7), so the wiring decides where they go (counters in GET /status, the heartbeat).
type Metrics interface {
	Set(name string, labels map[string]string, value float64)
}

// Deps are what the engine needs. Registry, Results and State are required; everything else is
// optional and its absence has the documented effect.
type Deps struct {
	Registry *Registry
	Results  *Results
	State    StateStore

	// Publisher receives drill.started, drill.check_done and drill.done.
	Publisher Publisher
	// Mode, Ark, Llama and Power are the preconditions' probes; a nil probe never refuses.
	Mode  ModeView
	Ark   ArkActivity
	Llama LlamaActivity
	Power PowerSource
	// Fence is the network fence; nil runs without one (the document says enabled: false).
	Fence Fence
	// Restorer captures and restores the model state; nil runs without one (no restored block).
	Restorer Restorer
	// Metrics receives the gauges; nil drops them.
	Metrics Metrics

	// NodeID, AgentVersion and CatalogVersion fill the document header.
	NodeID         string
	AgentVersion   string
	CatalogVersion func() string

	// Clock defaults to config.RealClock; Rand to crypto/rand (run ids); Logger to slog.Default.
	Clock  config.Clock
	Rand   io.Reader
	Logger *slog.Logger
}

// Engine runs drills. Create it with New, call Recover once at startup, feed RunRetries a ticker, and
// call Shutdown while stopping.
type Engine struct {
	d     Deps
	clock config.Clock
	log   *slog.Logger
	block stateBlock
	pre   Preconditions

	mu      sync.Mutex
	active  *activeRun
	closing bool
}

// New returns an Engine, or an error when a required dependency is missing.
func New(d Deps) (*Engine, error) {
	switch {
	case d.Registry == nil:
		return nil, errors.New("drill: the engine needs a check registry")
	case d.Results == nil:
		return nil, errors.New("drill: the engine needs a results store")
	case d.State == nil:
		return nil, errors.New("drill: the engine needs a state store")
	}
	e := &Engine{d: d, clock: d.Clock, log: d.Logger}
	if e.clock == nil {
		e.clock = config.RealClock{}
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	e.log = e.log.With("component", "drill")
	e.block = stateBlock{store: d.State, log: e.log}
	e.pre = Preconditions{Mode: d.Mode, Ark: d.Ark, Llama: d.Llama, Power: d.Power, Log: e.log}
	return e, nil
}

func (e *Engine) now() time.Time { return e.clock.Now().UTC().Truncate(time.Second) }

// StartRequest is a request to run a drill.
type StartRequest struct {
	// Quick skips the full-hash verification and the model loads other than the default.
	Quick bool
	// Only runs just these checks (an id selects itself and what is under it); empty runs all.
	Only []string
	// Trigger is TriggerManual (the default) or TriggerScheduled.
	Trigger Trigger
	// OpID is the operation id of the request; it stamps the run's events. Empty for a retry.
	OpID string
}

// StartState says what Start did.
type StartState string

const (
	// StartRunning: the run started and runs in the background.
	StartRunning StartState = "running"
	// StartSkipped: a scheduled run that a precondition held back; its document says why.
	StartSkipped StartState = "skipped"
)

// StartResult is what Start returns. For a skipped run, Code, Reason and RetryAt mirror the document
// (RetryAt is nil when the agent will not try again).
type StartResult struct {
	RunID   string
	State   StartState
	Code    string
	Reason  string
	RetryAt *time.Time
}

// activeRun is the run in flight (or being started).
type activeRun struct {
	id     string
	opID   string
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu   sync.Mutex
	doc  *Document // the live document header; nil until execute begins
	recs []record  // the checks recorded so far
}

func (a *activeRun) setDoc(d *Document) {
	a.mu.Lock()
	a.doc = d
	a.mu.Unlock()
}

func (a *activeRun) addRecord(r record) {
	a.mu.Lock()
	a.recs = append(a.recs, r)
	a.mu.Unlock()
}

func (a *activeRun) records() []record {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]record(nil), a.recs...)
}

// snapshot returns the live document as a copy marked running, nil before the run began.
func (a *activeRun) snapshot() *Document {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.doc == nil {
		return nil
	}
	c := *a.doc
	c.Checks = checkResults(a.recs)
	c.State = StateRunning
	c.Overall = OverallRunning
	c.FinishedAt = nil
	_, _, c.Counts = aggregate(a.recs)
	return &c
}

// Start validates req, checks the preconditions and starts a run in the background. A manual run a
// precondition refuses is a *RefusedError (409 with the code); a scheduled one is recorded as a
// skipped document, with a retry for a sync that is running (daily for 7 days) or a busy llama (every
// 2 hours for 48 hours, then --quick), and returns StartSkipped. A request the engine cannot honor is
// an *InvalidRequestError; a stopping engine is ErrClosing.
func (e *Engine) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	return e.start(ctx, req, nil)
}

func (e *Engine) start(ctx context.Context, req StartRequest, retry *deferral) (StartResult, error) {
	switch req.Trigger {
	case "":
		req.Trigger = TriggerManual
	case TriggerManual, TriggerScheduled:
	default:
		return StartResult{}, &InvalidRequestError{Field: "trigger", Reason: "unknown_trigger"}
	}
	only, err := NormalizeOnly(req.Only)
	if err != nil {
		return StartResult{}, &InvalidRequestError{Field: "only", Reason: err.Error()}
	}
	req.Only = only
	checks, err := e.d.Registry.Select(ctx, only)
	if err != nil {
		var unknown *UnknownCheckError
		if errors.As(err, &unknown) {
			return StartResult{}, &InvalidRequestError{Field: "only", Reason: "unknown_check", Unknown: unknown.IDs}
		}
		return StartResult{}, err
	}

	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return StartResult{}, ErrClosing
	}
	if cur := e.active; cur != nil {
		e.mu.Unlock()
		return e.refuse(req, retry, drillRunning(cur.id))
	}
	now := e.now()
	ar := e.reserve(now, req.OpID)
	e.active = ar
	e.mu.Unlock()

	opts := preOpts{}
	if retry != nil && retry.Code == CodeBusy && !now.Before(retry.DeadlineAt) {
		// The 48 hour deferral ran out: the drill runs anyway, as --quick (drill.md section 4.1).
		opts.skipBusy = true
		req.Quick = true
		e.log.Info("busy deferral expired: running as quick", "first_at", retry.FirstAt, "attempts", retry.Attempts)
	}
	if ref := e.pre.Evaluate(ar.ctx, opts); ref != nil {
		e.release(ar)
		return e.refuse(req, retry, ref)
	}
	if req.Trigger == TriggerScheduled || !req.Quick {
		// A run that stands for the scheduled drill ends the wait for one.
		if e.block.deferral() != nil {
			if err := e.block.setDeferral(nil); err != nil {
				e.log.Warn("could not clear the drill deferral", "error", err.Error())
			}
		}
	}
	go e.execute(ar, req, checks)
	return StartResult{RunID: ar.id, State: StartRunning}, nil
}

// reserve makes the slot of a run: its id and its cancelable context. The caller holds e.mu.
func (e *Engine) reserve(now time.Time, opID string) *activeRun {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &activeRun{id: e.newRunID(now, ""), opID: opID, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// release frees the slot and wakes whoever waits for it.
func (e *Engine) release(ar *activeRun) {
	e.mu.Lock()
	if e.active == ar {
		e.active = nil
	}
	e.mu.Unlock()
	ar.cancel(nil)
	close(ar.done)
}

// newRunID returns drl-YYYYMMDDTHHMMSSZ-<4hex>, never one that is kept or equal to avoid (the id of
// the run in flight, when the caller has one). It draws random suffixes and, if the generator keeps
// colliding, walks the 65536 suffixes of that second in order.
func (e *Engine) newRunID(now time.Time, avoid string) string {
	rd := e.d.Rand
	if rd == nil {
		rd = rand.Reader
	}
	stamp := "drl-" + now.Format("20060102T150405Z") + "-"
	free := func(id string) bool { return id != avoid && !e.d.Results.Has(id) }
	var b [2]byte
	for i := 0; i < 16; i++ {
		if _, err := io.ReadFull(rd, b[:]); err != nil {
			b = [2]byte{byte(now.Nanosecond()), byte(now.Nanosecond() >> 8)}
		}
		if id := stamp + hex.EncodeToString(b[:]); free(id) {
			return id
		}
	}
	start := int(b[0])<<8 | int(b[1])
	for i := 1; i < 1<<16; i++ {
		n := (start + i) & 0xffff
		if id := fmt.Sprintf("%s%04x", stamp, n); free(id) {
			return id
		}
	}
	return stamp + hex.EncodeToString(b[:])
}

// Running reports the run in flight, so the sync engine can refuse to start while a drill is active.
func (e *Engine) Running() (runID string, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active == nil {
		return "", false
	}
	return e.active.id, true
}

// Abort stops the running drill (runID empty means whichever runs): the run restores the node and its
// document is marked aborted. It returns at once; Wait waits for the restore. With no drill running,
// or a different one, it is ErrNoRun.
func (e *Engine) Abort(runID string) (string, error) {
	e.mu.Lock()
	ar := e.active
	e.mu.Unlock()
	if ar == nil || (runID != "" && runID != ar.id) {
		return "", ErrNoRun
	}
	ar.cancel(errAborted)
	e.log.Info("drill abort requested", "run_id", ar.id)
	return ar.id, nil
}

// Wait blocks until no drill is running, or ctx ends.
func (e *Engine) Wait(ctx context.Context) error {
	for {
		e.mu.Lock()
		ar := e.active
		e.mu.Unlock()
		if ar == nil {
			return nil
		}
		select {
		case <-ar.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Shutdown stops accepting runs, aborts the running one so that it restores the node, and waits for
// it (or ctx). The run is marked aborted. An agent that is killed instead of stopped leaves
// drill.in_progress behind, and Recover finishes the job at the next start.
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closing = true
	ar := e.active
	e.mu.Unlock()
	if ar != nil {
		ar.cancel(errShutdown)
	}
	return e.Wait(ctx)
}

// Get returns the document of a run: the live snapshot (state running) for the run in flight, else
// the kept file. ErrNotFound when it is not kept.
func (e *Engine) Get(runID string) ([]byte, error) {
	e.mu.Lock()
	ar := e.active
	e.mu.Unlock()
	if ar != nil && ar.id == runID {
		if doc := ar.snapshot(); doc != nil {
			return encode(doc)
		}
	}
	return e.d.Results.Get(runID)
}

// Latest returns the last finished run's document (ErrNotFound when none is kept).
func (e *Engine) Latest() ([]byte, error) { return e.d.Results.Latest() }

// record is a check's result plus what the aggregation needs to know about it.
type record struct {
	CheckResult
	critical bool
	// neutral: a critical check skipped by --quick does not count against survival_ready.
	neutral bool
}

func checkResults(recs []record) []CheckResult {
	out := make([]CheckResult, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.CheckResult)
	}
	return out
}

// aggregate computes overall, survival_ready and the counts (drill.md section 3.4). overall is fail
// if any check fails, else warn if any warns, else pass. survival_ready is true when at least one
// critical check ran and every critical check passed or warned; a critical check that was skipped
// (other than by --quick) or failed makes it false.
func aggregate(recs []record) (overall string, ready bool, counts Counts) {
	overall = OverallPass
	evaluated, vetoed := 0, false
	for _, r := range recs {
		switch r.Result {
		case ResultPass:
			counts.Pass++
		case ResultWarn:
			counts.Warn++
		case ResultFail:
			counts.Fail++
		case ResultSkip:
			counts.Skip++
		}
		if r.critical && !r.neutral {
			evaluated++
			if r.Result == ResultFail || r.Result == ResultSkip {
				vetoed = true
			}
		}
	}
	switch {
	case counts.Fail > 0:
		overall = OverallFail
	case counts.Warn > 0:
		overall = OverallWarn
	}
	return overall, evaluated > 0 && !vetoed, counts
}

func (e *Engine) actor() audit.Actor { return audit.System() }

func (e *Engine) publish(typ, opID string, data any) {
	if e.d.Publisher == nil {
		return
	}
	if err := e.d.Publisher.Publish(typ, opID, data); err != nil && !errors.Is(err, events.ErrClosed) {
		e.log.Warn("could not publish a drill event", "type", typ, "error", err.Error())
	}
}
