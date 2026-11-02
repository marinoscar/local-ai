package services_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cfgfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/services/fake"
)

type fakeProber struct {
	mu    sync.Mutex
	errs  map[string]error
	calls map[string]int
}

func newProber() *fakeProber { return &fakeProber{errs: map[string]error{}, calls: map[string]int{}} }

func (p *fakeProber) Check(_ context.Context, pr services.Probe) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[pr.Service]++
	return p.errs[pr.Service]
}

func (p *fakeProber) set(service string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.errs[service] = err
}

func (p *fakeProber) count(service string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[service]
}

type recPub struct{ ch chan services.StateEvent }

func newPub() *recPub { return &recPub{ch: make(chan services.StateEvent, 64)} }

func (p *recPub) PublishServiceState(e services.StateEvent) { p.ch <- e }

func (p *recPub) next(t *testing.T) services.StateEvent {
	t.Helper()
	select {
	case e := <-p.ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no service.state event")
	}
	return services.StateEvent{}
}

func (p *recPub) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-p.ch:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(60 * time.Millisecond):
	}
}

type recAlerts struct {
	mu      sync.Mutex
	raised  []string
	cleared []string
	details []map[string]any
}

func (a *recAlerts) Raise(code, service string, details map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.raised = append(a.raised, code+" "+service)
	a.details = append(a.details, details)
}

func (a *recAlerts) Clear(code, service string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleared = append(a.cleared, code+" "+service)
}

func (a *recAlerts) snapshot() (raised, cleared []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.raised...), append([]string(nil), a.cleared...)
}

type rig struct {
	mgr    *fake.Manager
	prober *fakeProber
	pub    *recPub
	alerts *recAlerts
	clock  *cfgfake.Clock
	w      *services.Watcher
	t0     time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{mgr: fake.New(), prober: newProber(), pub: newPub(), alerts: &recAlerts{}, t0: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
	r.clock = cfgfake.NewClock(r.t0)
	r.mgr.SetClock(r.clock.Now)
	r.w = services.NewWatcher(services.WatcherOptions{
		Manager: r.mgr, Prober: r.prober, Ports: services.Ports{Llama: 8080, Kiwix: 8888, Whisper: 8081},
		Publisher: r.pub, Alerts: r.alerts, Clock: r.clock,
	})
	return r
}

func (r *rig) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.w.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		sctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		if err := r.w.Stop(sctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
}

func (r *rig) active(unit string) {
	r.mgr.Set(services.UnitState{Unit: unit, ActiveState: "active", SubState: "running", MainPID: 10, ActiveEnterTimestamp: r.clock.Now(), UnitFileState: "enabled"})
}

func (r *rig) status(t *testing.T, name string) services.Status {
	t.Helper()
	for _, s := range r.w.Snapshot(context.Background()) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return services.Status{}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWatcherSnapshotBeforeAnyEvent(t *testing.T) {
	r := newRig(t)
	r.mgr.Set(services.UnitState{
		Unit: services.UnitLlama, ActiveState: "active", SubState: "running", NRestarts: 2, UnitFileState: "enabled",
		ActiveEnterTimestamp: r.t0, ExecMainStatus: 1, ExecMainExitTimestamp: r.t0.Add(-time.Minute),
	})
	got := r.w.Snapshot(context.Background())
	if len(got) != 3 || got[0].Name != "llama" || got[1].Name != "kiwix" || got[2].Name != "whisper" {
		t.Fatalf("snapshot = %+v", got)
	}
	l := got[0]
	if l.Unit != services.UnitLlama || l.Active != "active" || l.Sub != "running" || l.Restarts != 2 || !l.Enabled || !l.Since.Equal(r.t0) {
		t.Errorf("llama = %+v", l)
	}
	if l.LastExit == nil || l.LastExit.Code != 1 || !l.LastExit.At.Equal(r.t0.Add(-time.Minute)) {
		t.Errorf("last exit = %+v", l.LastExit)
	}
	// Active but not probed yet: starting, not healthy.
	if l.Healthy || l.Health != services.HealthStarting {
		t.Errorf("llama health = %v %s", l.Healthy, l.Health)
	}
	// A unit with no file is inactive, down, never exited, not enabled.
	k := got[1]
	if k.Active != "inactive" || k.Healthy || k.Health != services.HealthDown || k.LastExit != nil || k.Enabled || !k.Since.IsZero() {
		t.Errorf("kiwix = %+v", k)
	}
}

func TestWatcherSnapshotWhenSystemdCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.mgr.Fail(services.VerbState, "", errors.New("bus down"))
	for _, s := range r.w.Snapshot(context.Background()) {
		if s.Active != "unknown" || s.Healthy || s.Health != services.HealthDown {
			t.Errorf("%s = %+v", s.Name, s)
		}
	}
}

func TestWatcherProbesOnStartAndPublishesChanges(t *testing.T) {
	r := newRig(t)
	r.active(services.UnitLlama)
	r.start(t)
	// The first observation sends no event; the probe that follows the start makes it healthy.
	eventually(t, "llama healthy", func() bool { return r.status(t, "llama").Healthy })
	if s := r.status(t, "llama"); s.Health != services.HealthOK {
		t.Fatalf("llama = %+v", s)
	}
	ev := r.pub.next(t)
	if ev.Name != "llama" || ev.From != services.HealthStarting || ev.To != services.HealthOK || !ev.Healthy || ev.Active != "active" || ev.Sub != "running" || ev.Code != "" || ev.Unit != services.UnitLlama {
		t.Errorf("event = %+v", ev)
	}
	r.pub.none(t)

	// The unit stops: down, with the new active state.
	r.mgr.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "inactive", SubState: "dead", ExecMainStatus: 143, ExecMainExitTimestamp: r.clock.Now()})
	ev = r.pub.next(t)
	if ev.From != services.HealthOK || ev.To != services.HealthDown || ev.Healthy || ev.Active != "inactive" || ev.Sub != "dead" {
		t.Errorf("stop event = %+v", ev)
	}
	if s := r.status(t, "llama"); s.Healthy || s.Health != services.HealthDown || !s.Since.IsZero() || s.LastExit == nil || s.LastExit.Code != 143 {
		t.Errorf("stopped llama = %+v", s)
	}
	// A sub-state change alone is a change.
	r.mgr.Set(services.UnitState{Unit: services.UnitLlama, ActiveState: "inactive", SubState: "failed"})
	if ev = r.pub.next(t); ev.Sub != "failed" || ev.From != services.HealthDown || ev.To != services.HealthDown {
		t.Errorf("sub event = %+v", ev)
	}
	// Starting again: the probe runs again and the unit is healthy again.
	r.prober.set("llama", nil)
	r.clock.Advance(time.Second)
	r.active(services.UnitLlama)
	eventually(t, "llama healthy again", func() bool { return r.status(t, "llama").Healthy })
}

func TestWatcherUnanswering(t *testing.T) {
	tests := []struct {
		service string
		unit    string
		code    string
	}{
		{"llama", services.UnitLlama, "LAI-LLM-004"},
		{"kiwix", services.UnitKiwix, "LAI-SVC-002"},
		{"whisper", services.UnitWhisper, "LAI-SVC-011"},
	}
	for _, tc := range tests {
		t.Run(tc.service, func(t *testing.T) {
			r := newRig(t)
			r.prober.set(tc.service, errors.New("connection refused"))
			r.active(tc.unit)
			r.start(t)
			eventually(t, "first probe", func() bool { return r.prober.count(tc.service) >= 1 })

			// The first minute after a start only counts as starting, and raises nothing.
			r.clock.Advance(50 * time.Second)
			r.w.CheckNow(context.Background(), tc.service)
			if s := r.status(t, tc.service); s.Health != services.HealthStarting || s.Healthy || s.Code != "" {
				t.Fatalf("after 50 s = %+v", s)
			}
			// Past the grace period the failure is first degraded, then, a minute after it began, down with a code.
			r.clock.Advance(15 * time.Second) // 65 s since the start, 65 s since the first failure
			r.w.CheckNow(context.Background(), tc.service)
			raised, _ := r.alerts.snapshot()
			if len(raised) != 1 || raised[0] != tc.code+" "+tc.service {
				t.Fatalf("raised = %v, want [%s %s]", raised, tc.code, tc.service)
			}
			if s := r.status(t, tc.service); s.Health != services.HealthDown || s.Code != tc.code || s.Healthy {
				t.Fatalf("down status = %+v", s)
			}
			if d := r.alerts.details[0]; d["unit"] != tc.unit {
				t.Errorf("alert details = %v", d)
			}
			// Still failing: the alert is raised once, not on every probe.
			r.clock.Advance(30 * time.Second)
			r.w.CheckNow(context.Background(), tc.service)
			r.w.CheckNow(context.Background(), tc.service)
			if raised, _ := r.alerts.snapshot(); len(raised) != 1 {
				t.Errorf("raised again: %v", raised)
			}
			// It answers again: the alert is withdrawn.
			r.prober.set(tc.service, nil)
			r.w.CheckNow(context.Background(), tc.service)
			_, cleared := r.alerts.snapshot()
			if len(cleared) != 1 || cleared[0] != tc.code+" "+tc.service {
				t.Fatalf("cleared = %v", cleared)
			}
			if s := r.status(t, tc.service); !s.Healthy || s.Health != services.HealthOK || s.Code != "" {
				t.Fatalf("recovered status = %+v", s)
			}
		})
	}
}

func TestWatcherDegradedBeforeDownForALongRunningService(t *testing.T) {
	r := newRig(t)
	r.active(services.UnitLlama)
	r.start(t)
	eventually(t, "llama healthy", func() bool { return r.status(t, "llama").Healthy })
	// Ten minutes later a single probe fails: degraded, no alert.
	r.clock.Advance(10 * time.Minute)
	r.prober.set("llama", errors.New("timeout"))
	r.w.CheckNow(context.Background(), "llama")
	if s := r.status(t, "llama"); s.Health != services.HealthDegraded || s.Healthy || s.Code != "" {
		t.Fatalf("status = %+v", s)
	}
	if raised, _ := r.alerts.snapshot(); len(raised) != 0 {
		t.Fatalf("raised = %v", raised)
	}
	// A recovery inside the minute clears nothing and leaves no alert behind.
	r.clock.Advance(30 * time.Second)
	r.prober.set("llama", nil)
	r.w.CheckNow(context.Background(), "llama")
	if s := r.status(t, "llama"); s.Health != services.HealthOK {
		t.Fatalf("status = %+v", s)
	}
	if raised, cleared := r.alerts.snapshot(); len(raised) != 0 || len(cleared) != 0 {
		t.Fatalf("alerts raised %v cleared %v", raised, cleared)
	}
	// A failure that lasts a minute is down.
	r.prober.set("llama", errors.New("timeout"))
	r.w.CheckNow(context.Background(), "llama")
	r.clock.Advance(61 * time.Second)
	r.w.CheckNow(context.Background(), "llama")
	if s := r.status(t, "llama"); s.Health != services.HealthDown || s.Code != "LAI-LLM-004" {
		t.Fatalf("status = %+v", s)
	}
}

func TestWatcherRestartResetsHealth(t *testing.T) {
	r := newRig(t)
	r.prober.set("llama", errors.New("refused"))
	r.active(services.UnitLlama)
	r.start(t)
	eventually(t, "first probe", func() bool { return r.prober.count("llama") >= 1 })
	r.clock.Advance(90 * time.Second)
	r.w.CheckNow(context.Background(), "llama")
	if s := r.status(t, "llama"); s.Health != services.HealthDown {
		t.Fatalf("status = %+v", s)
	}
	// systemd restarted the unit: a new ActiveEnterTimestamp starts the minute over.
	r.prober.set("llama", nil)
	r.active(services.UnitLlama)
	eventually(t, "healthy after the restart", func() bool { return r.status(t, "llama").Healthy })
	_, cleared := r.alerts.snapshot()
	if len(cleared) != 1 {
		t.Errorf("cleared = %v, want the alert withdrawn once", cleared)
	}
}

func TestWatcherProbesOnTheirOwnIntervals(t *testing.T) {
	r := newRig(t)
	for _, u := range []string{services.UnitLlama, services.UnitKiwix, services.UnitWhisper} {
		r.active(u)
	}
	r.start(t)
	eventually(t, "the three start probes", func() bool {
		return r.prober.count("llama") >= 1 && r.prober.count("kiwix") >= 1 && r.prober.count("whisper") >= 1
	})
	base := map[string]int{"llama": r.prober.count("llama"), "kiwix": r.prober.count("kiwix"), "whisper": r.prober.count("whisper")}
	// 15 s later only llama is due; at 30 s the others are too.
	waitTicker(t, r.clock)
	r.clock.Advance(15 * time.Second)
	eventually(t, "llama probed at 15 s", func() bool { return r.prober.count("llama") > base["llama"] })
	if r.prober.count("kiwix") != base["kiwix"] || r.prober.count("whisper") != base["whisper"] {
		t.Errorf("kiwix or whisper probed after 15 s: %v", r.prober.calls)
	}
	r.clock.Advance(15 * time.Second)
	eventually(t, "kiwix and whisper probed at 30 s", func() bool {
		return r.prober.count("kiwix") > base["kiwix"] && r.prober.count("whisper") > base["whisper"]
	})
}

func waitTicker(t *testing.T, c *cfgfake.Clock) {
	t.Helper()
	eventually(t, "the health ticker", func() bool { return c.Tickers() > 0 })
}

func TestWatcherIgnoresProbesOfAStoppedUnit(t *testing.T) {
	r := newRig(t)
	r.start(t)
	r.w.CheckNow(context.Background())
	if n := r.prober.count("llama") + r.prober.count("kiwix") + r.prober.count("whisper"); n != 0 {
		t.Errorf("stopped units were probed %d times", n)
	}
}

func TestWatcherStartFailsWithoutASubscription(t *testing.T) {
	r := newRig(t)
	r.mgr.Fail(services.VerbSubscribe, "", errors.New("no bus"))
	if err := r.w.Start(context.Background()); err == nil {
		t.Fatal("Start succeeded without a subscription")
	}
}

func TestWatcherName(t *testing.T) {
	if got := newRig(t).w.Name(); got != "services" {
		t.Errorf("Name = %q", got)
	}
}
