package app

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// ---- fakes of the shutdown seam -------------------------------------------------------------------

type order struct {
	mu   sync.Mutex
	list []string
}

func (o *order) add(s string) {
	o.mu.Lock()
	o.list = append(o.list, s)
	o.mu.Unlock()
}

func (o *order) get() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.list...)
}

func (o *order) index(s string) int {
	for i, x := range o.get() {
		if x == s {
			return i
		}
	}
	return -1
}

// fakeStop is a subsystem of the shutdown that records when it is asked and either returns at once or
// blocks until its context ends (a slow cloud, a running transition, a running sync).
type fakeStop struct {
	name  string
	log   *order
	block bool
	err   error
}

func (f *fakeStop) wait(ctx context.Context) error {
	f.log.add(f.name + ":begin")
	if f.block {
		<-ctx.Done()
		f.log.add(f.name + ":abandoned")
		return ctx.Err()
	}
	f.log.add(f.name + ":end")
	return f.err
}

func (f *fakeStop) Shutdown(ctx context.Context) error       { return f.wait(ctx) }
func (f *fakeStop) CancelGraceful(ctx context.Context) error { return f.wait(ctx) }
func (f *fakeStop) Stop(ctx context.Context) error           { return f.wait(ctx) }
func (f *fakeStop) SetMutationsOpen(open bool) {
	if !open {
		f.log.add(f.name + ":gate-closed")
	}
}
func (f *fakeStop) Close() error { f.log.add(f.name + ":close"); return f.err }

// slowClose is a state store whose Close does not return until the test is over (a stuck disk).
type slowClose struct {
	log     *order
	release chan struct{}
}

func (s *slowClose) Close() error {
	s.log.add("state:begin")
	<-s.release
	return nil
}

type fakeBus struct{ log *order }

func (b fakeBus) Close() { b.log.add("bus:close") }

type fakeProber struct{ log *order }

func (p fakeProber) Stop() { p.log.add("prober:stop") }

type logWorker struct {
	name string
	log  *order
}

func (w logWorker) Name() string                { return w.name }
func (w logWorker) Start(context.Context) error { return nil }
func (w logWorker) Stop(ctx context.Context) error {
	w.log.add("worker " + w.name + ":stop")
	return nil
}

// shutdownRig is a Daemon with only the shutdown seam filled in, on a manual clock.
type shutdownRig struct {
	d     *Daemon
	clock *cfgfake.Clock
	log   *order
	note  *notifyListener
	logs  *syncBuf
}

func newShutdownRig(t *testing.T, mutate func(r *shutdownRig, s *shutdownSet)) *shutdownRig {
	t.Helper()
	r := &shutdownRig{clock: cfgfake.NewClock(t0), log: &order{}, note: newNotifyListener(t, false), logs: &syncBuf{}}
	d := NewDaemon(Env{Clock: r.clock, Notifier: NewNotifier(r.note.name), Stdout: r.logs, Hooks: NewHooks(), Getenv: func(string) string { return "" }})
	d.ensureLogger()
	d.periodic = []Worker{logWorker{"first", r.log}, logWorker{"second", r.log}}
	s := shutdownSet{
		link:   &fakeStop{name: "link", log: r.log},
		engine: &fakeStop{name: "engine", log: r.log},
		syncer: &fakeStop{name: "sync", log: r.log},
		drill:  &fakeStop{name: "drill", log: r.log},
		orch:   &fakeStop{name: "orch", log: r.log},
		server: &fakeStop{name: "server", log: r.log},
		bus:    fakeBus{r.log},
		state:  &fakeStop{name: "state", log: r.log},
		audit:  &fakeStop{name: "audit", log: r.log},
		prober: fakeProber{r.log},
	}
	if mutate != nil {
		mutate(r, &s)
	}
	d.stoppers = s
	r.d = d
	return r
}

// start runs the shutdown in the background and returns the channel its report arrives on.
func (r *shutdownRig) start() <-chan ShutdownReport {
	done := make(chan ShutdownReport, 1)
	go func() { done <- r.d.Shutdown() }()
	return done
}

// untilLogged waits until the subsystems have recorded every entry. The wait ends when the entry is
// there (the client side of the shutdown has done it), never after a fixed time; the manual clock is
// moved only after that, so a deadline can never fire before the step that waits on it exists.
func (r *shutdownRig) untilLogged(t *testing.T, entries ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for _, e := range entries {
		for r.log.index(e) < 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%q was never recorded; order: %v", e, r.log.get())
			}
			runtime.Gosched()
		}
	}
}

// finish waits for the report.
func (r *shutdownRig) finish(t *testing.T, done <-chan ShutdownReport) ShutdownReport {
	t.Helper()
	select {
	case rep := <-done:
		return rep
	case <-time.After(30 * time.Second):
		t.Fatalf("the shutdown did not finish; order: %v", r.log.get())
		return ShutdownReport{}
	}
}

// ---- tests ----------------------------------------------------------------------------------------

// The order of agent.md section 4.12.
func TestShutdownOrder(t *testing.T) {
	r := newShutdownRig(t, nil)
	rep := r.finish(t, r.start())

	if got := r.note.next(t); got != "STOPPING=1" {
		t.Errorf("first sd_notify message = %q, want STOPPING=1", got)
	}
	l := r.log
	before := func(a, b string) {
		t.Helper()
		ia, ib := l.index(a), l.index(b)
		if ia < 0 || ib < 0 || ia > ib {
			t.Errorf("%q must come before %q; order: %v", a, b, l.get())
		}
	}
	before("server:gate-closed", "worker second:stop") // 1 before 2
	before("worker second:stop", "worker first:stop")  // reverse start order
	before("prober:stop", "link:begin")                // 2 before 3
	before("worker first:stop", "link:begin")
	for _, g := range []string{"link:end", "engine:end", "sync:end", "drill:end"} { // 3, 4, 5 before 6
		before(g, "orch:begin")
	}
	before("orch:end", "server:begin") // 6 before 7
	before("server:end", "bus:close")  // the bus closes after the server stopped
	before("bus:close", "state:close") // 8 last
	before("state:close", "audit:close")
	if len(rep.Steps) < 8 {
		t.Errorf("the report has %d steps: %+v", len(rep.Steps), rep.Steps)
	}
	if !r.d.stopping.Load() {
		t.Error("stopping is not set")
	}
	// Calling it twice is harmless and does not run the steps again.
	n := len(l.get())
	r.d.Shutdown()
	if len(l.get()) != n {
		t.Error("a second Shutdown ran the steps again")
	}
}

// A slow cloud, a running mode transition, a running sync and a drill do not hold the shutdown past
// its budget: each is abandoned at its own deadline (13 s, 10 s, 10 s, 10 s), the rest runs, and the
// whole thing ends inside 25 s of the daemon's clock. The clock moves only once every slow step is
// waiting on it, and no step is touched by anything but its deadline.
func TestShutdownBudgetWithSlowSubsystems(t *testing.T) {
	r := newShutdownRig(t, func(r *shutdownRig, s *shutdownSet) {
		s.link = &fakeStop{name: "link", log: r.log, block: true}
		s.engine = &fakeStop{name: "engine", log: r.log, block: true}
		s.syncer = &fakeStop{name: "sync", log: r.log, block: true}
		s.drill = &fakeStop{name: "drill", log: r.log, block: true}
	})
	start := r.clock.Now()
	done := r.start()
	r.untilLogged(t, "link:begin", "engine:begin", "sync:begin", "drill:begin")
	select {
	case <-done:
		t.Fatal("the shutdown ended before any deadline passed")
	default:
	}
	r.clock.Advance(shutdownModeBudget) // 10 s: the engine, the sync and the drill are abandoned
	r.untilLogged(t, "engine:abandoned", "sync:abandoned", "drill:abandoned")
	// Each abandoned step is recorded (and its duration read from the clock) before the clock moves on.
	for _, name := range []string{"mode_transition", "sync", "drill"} {
		for !r.logs.has("shutdown step degraded", map[string]any{"name": name}) {
			runtime.Gosched()
		}
	}
	if r.log.index("link:abandoned") >= 0 {
		t.Error("the cloud link was abandoned at 10 s; its budget is 13 s")
	}
	r.clock.Advance(shutdownLinkBudget - shutdownModeBudget) // 13 s in all: the link is abandoned
	rep := r.finish(t, done)

	limits := map[string]time.Duration{"cloud_link": shutdownLinkBudget, "mode_transition": shutdownModeBudget, "sync": shutdownSyncBudget, "drill": shutdownDrillBudget}
	for _, s := range rep.Steps {
		want, ok := limits[s.Name]
		if !ok {
			continue
		}
		delete(limits, s.Name)
		if !s.TimedOut {
			t.Errorf("step %s was not abandoned: %+v", s.Name, s)
		}
		if s.Duration != want {
			t.Errorf("step %s took %v, want its budget %v", s.Name, s.Duration, want)
		}
	}
	if len(limits) != 0 {
		t.Errorf("steps not in the report: %v", limits)
	}
	if elapsed := r.clock.Now().Sub(start); elapsed != shutdownLinkBudget || rep.Duration != shutdownLinkBudget {
		t.Errorf("the shutdown took %v (report %v) of virtual time, want %v", elapsed, rep.Duration, shutdownLinkBudget)
	}
	// What follows the slow group still ran, in order.
	for _, want := range []string{"orch:end", "server:end", "bus:close", "state:close", "audit:close"} {
		if r.log.index(want) < 0 {
			t.Errorf("%s did not run after the slow subsystems: %v", want, r.log.get())
		}
	}
}

// Every later step has its budget too, and the budgets add up to less than the total of 25 s, which is
// less than systemd's TimeoutStopSec of 30 s.
func TestShutdownBudgetsFitTheTotal(t *testing.T) {
	r := newShutdownRig(t, func(r *shutdownRig, s *shutdownSet) {
		s.link = &fakeStop{name: "link", log: r.log, block: true}
		s.orch = &fakeStop{name: "orch", log: r.log, block: true}
		s.server = &fakeStop{name: "server", log: r.log, block: true}
		s.state = &slowClose{log: r.log, release: make(chan struct{})}
		t.Cleanup(func() { close(s.state.(*slowClose).release) })
	})
	start := r.clock.Now()
	done := r.start()
	r.untilLogged(t, "link:begin")
	r.clock.Advance(shutdownLinkBudget)
	r.untilLogged(t, "orch:begin")
	r.clock.Advance(shutdownModelBudget)
	r.untilLogged(t, "server:begin")
	r.clock.Advance(shutdownHTTPBudget)
	r.untilLogged(t, "state:begin")
	r.clock.Advance(shutdownFlushBudget)
	rep := r.finish(t, done)

	want := shutdownLinkBudget + shutdownModelBudget + shutdownHTTPBudget + shutdownFlushBudget
	if elapsed := r.clock.Now().Sub(start); elapsed != want || rep.Duration != want {
		t.Errorf("the shutdown took %v, want the sum of the budgets %v", rep.Duration, want)
	}
	if want > ShutdownBudget || ShutdownBudget >= 30*time.Second {
		t.Errorf("budgets %v, total %v: they must fit inside the total, which must stay under 30 s", want, ShutdownBudget)
	}
	for _, name := range []string{"cloud_link", "model_operations", "http_server", "flush"} {
		found := false
		for _, s := range rep.Steps {
			if s.Name == name {
				found = true
				if !s.TimedOut {
					t.Errorf("step %s should have been abandoned: %+v", name, s)
				}
			}
		}
		if !found {
			t.Errorf("step %s is not in the report", name)
		}
	}
}

// The total budget is a hard cap: once it has passed, no later step waits for anything.
func TestShutdownTotalBudgetIsAHardCap(t *testing.T) {
	r := newShutdownRig(t, func(r *shutdownRig, s *shutdownSet) {
		s.server = &fakeStop{name: "server", log: r.log, block: true}
	})
	start := r.clock.Now()
	done := r.start()
	r.untilLogged(t, "server:begin")
	// The server's own budget is 5 s; jump past the total at once: the server step and everything
	// after it ends together.
	r.clock.Advance(ShutdownBudget)
	rep := r.finish(t, done)
	if elapsed := r.clock.Now().Sub(start); elapsed != ShutdownBudget {
		t.Errorf("virtual time %v", elapsed)
	}
	for _, s := range rep.Steps {
		if s.Name == "http_server" && !s.TimedOut {
			t.Errorf("the server step was not cut off: %+v", s)
		}
	}
	// The flush is not clipped by the total (state.json matters more than a few seconds): it still
	// runs, with its own 3 s, so the worst case is 28 s.
	r.untilLogged(t, "state:close", "audit:close")
	if got := ShutdownBudget + shutdownFlushBudget; got >= 30*time.Second {
		t.Errorf("the worst case %v must stay under systemd's 30 s", got)
	}
}

// A subsystem that is missing (a daemon that stopped starting half way) is skipped, not a panic.
func TestShutdownWithNothingBuilt(t *testing.T) {
	d := NewDaemon(Env{Notifier: NewNotifier(""), Stdout: &syncBuf{}, Hooks: NewHooks(), Getenv: func(string) string { return "" }})
	d.ensureLogger()
	rep := d.Shutdown()
	if rep.Duration < 0 {
		t.Error("negative duration")
	}
}

// During the shutdown the mutating routes answer 503 LAI-AGT-024 but POST /mode/survival is still
// accepted, and recorded as a pending transition in state.json so that it runs at the next start.
func TestShutdownQueuesSurvivalAndRefusesMutations(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	hold := &gatedStop{entered: make(chan struct{}), release: make(chan struct{})}
	d.stoppers.link = hold // a slow cloud: the shutdown waits here while the server still answers
	done := make(chan struct{})
	go func() { d.Shutdown(); close(done) }()
	<-hold.entered

	if got := r.doAuth("GET", "/health", nil, false); got.status != 200 {
		t.Errorf("GET /health while stopping = %d", got.status)
	}
	load := r.do("POST", "/models/"+modelAlpha+"/unload", map[string]any{})
	if load.status != http.StatusServiceUnavailable || errCode(load) != doctor.AGT024 {
		t.Errorf("a mutating route while stopping = %d %s, want 503 LAI-AGT-024", load.status, load.body)
	}
	sv := r.doAuth("POST", "/mode/survival", map[string]any{}, false)
	if sv.status != http.StatusAccepted || sv.json()["queued"] != true {
		t.Errorf("POST /mode/survival while stopping = %d %s, want 202 queued", sv.status, sv.body)
	}
	close(hold.release)
	<-done

	var persisted state.State
	if err := json.Unmarshal([]byte(r.read("var/lib/localai/state.json")), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.PendingMode == nil || *persisted.PendingMode != state.ModeSurvival {
		t.Errorf("pending_mode in state.json = %v, want survival (it runs at the next start)", persisted.PendingMode)
	}
	if !r.sawStopping() {
		t.Error("STOPPING=1 was not sent")
	}
	for _, c := range r.sysd.Calls() {
		if c.Unit == services.UnitLlama && (c.Verb == services.VerbStop || c.Verb == services.VerbRestart) {
			t.Errorf("the shutdown touched llama: %+v", c)
		}
	}
}

func (r *rig) sawStopping() bool {
	for _, m := range r.d.env.Notifier.Sent() {
		if m == "STOPPING=1" {
			return true
		}
	}
	return false
}

// gatedStop is a link whose Shutdown waits for the test.
type gatedStop struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gatedStop) Shutdown(ctx context.Context) error {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	return nil
}

func TestErrString(t *testing.T) {
	if errString(nil) != "" || !strings.Contains(errString(context.Canceled), "canceled") {
		t.Error("errString")
	}
}
