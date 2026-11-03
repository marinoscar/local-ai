package health_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/services"
	sfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
	"github.com/marinoscar/local-ai/agent/internal/svc/health"
)

// t0 is the one instant the tests start from; every clock and fixture derives from it.
var t0 = time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)

var errDown = errors.New("connection refused")

// fakeProbe is a scripted health.Probe: the fields are what it answers next. Safe for concurrent use.
type fakeProbe struct {
	mu        sync.Mutex
	live      error
	content   health.Content
	readiness health.Readiness
	deep      []health.Problem

	liveCalls, contentCalls, readyCalls, deepCalls int
}

func (p *fakeProbe) Liveness(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.liveCalls++
	return p.live
}

func (p *fakeProbe) Content(context.Context) health.Content {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.contentCalls++
	return p.content
}

func (p *fakeProbe) Readiness(context.Context) health.Readiness {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.readyCalls++
	return p.readiness
}

func (p *fakeProbe) Deep(context.Context) []health.Problem {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deepCalls++
	return p.deep
}

func (p *fakeProbe) set(f func(p *fakeProbe)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *fakeProbe) calls() (live, content, ready, deep int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.liveCalls, p.contentCalls, p.readyCalls, p.deepCalls
}

// recPub records the service.state events.
type recPub struct {
	mu  sync.Mutex
	evs []events.ServiceState
	ch  chan events.ServiceState
}

func newPub() *recPub { return &recPub{ch: make(chan events.ServiceState, 64)} }

func (p *recPub) Publish(typ, _ string, data any) error {
	if typ != events.TypeServiceState {
		return errors.New("unexpected event type " + typ)
	}
	ev := data.(events.ServiceState)
	p.mu.Lock()
	p.evs = append(p.evs, ev)
	p.mu.Unlock()
	p.ch <- ev
	return nil
}

func (p *recPub) all() []events.ServiceState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]events.ServiceState(nil), p.evs...)
}

func (p *recPub) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = nil
}

// transitions renders the events as "name from>to".
func (p *recPub) transitions() []string {
	var out []string
	for _, e := range p.all() {
		out = append(out, e.Name+" "+e.From+">"+e.To)
	}
	return out
}

// rig is a supervisor over a fake manager, a manual clock and scripted probes.
type rig struct {
	t     *testing.T
	clock *cfake.Clock
	mgr   *sfake.Manager
	pub   *recPub
	sup   *health.Supervisor
	log   *bytes.Buffer
	probe map[string]*fakeProbe
	mode  string

	metrics *health.Metrics
}

type rigOpts struct {
	units    []string // services backed by a unit: kiwix, whisper
	unitless []string // piper, maps
	enabled  map[string]func() bool
	require  []string
	tune     func(o *health.Options)
}

var unitOf = map[string]string{"kiwix": services.UnitKiwix, "whisper": services.UnitWhisper}

var downCode = map[string]string{"kiwix": health.CodeLibraryNotRunning, "whisper": health.CodeSpeechNotRunning}

func newRig(t *testing.T, o rigOpts) *rig {
	t.Helper()
	r := &rig{t: t, clock: cfake.NewClock(t0), mgr: sfake.New(), pub: newPub(), log: &bytes.Buffer{}, probe: map[string]*fakeProbe{}, mode: "online"}
	r.mgr.SetClock(r.clock.Now)
	var specs []health.Spec
	for _, n := range append(append([]string{}, o.units...), o.unitless...) {
		p := &fakeProbe{}
		r.probe[n] = p
		specs = append(specs, health.Spec{Name: n, Unit: unitOf[n], Enabled: o.enabled[n], Probe: p, DownCode: downCode[n]})
	}
	r.metrics = health.NewMetrics()
	opt := health.Options{
		Manager: r.mgr, Specs: specs, Publisher: r.pub, Clock: r.clock, Metrics: r.metrics,
		Mode:            func() string { return r.mode },
		RequireServices: func() []string { return o.require },
		Logger:          slog.New(slog.NewJSONHandler(r.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
	if o.tune != nil {
		o.tune(&opt)
	}
	sup, err := health.New(opt)
	if err != nil {
		t.Fatal(err)
	}
	r.sup = sup
	return r
}

// setUnit puts a unit in the given state; running units have been up for an hour unless since says
// otherwise.
func (r *rig) setUnit(name, active string, since time.Duration) {
	r.t.Helper()
	sub := "running"
	switch active {
	case services.ActiveInactive:
		sub = "dead"
	case services.ActiveFailed:
		sub = "failed"
	}
	st := services.UnitState{
		Unit: unitOf[name], ActiveState: active, SubState: sub, UnitFileState: "enabled",
		ActiveEnterTimestamp: r.clock.Now().Add(-since),
	}
	r.mgr.Set(st)
	r.sup.Observe(context.Background(), st)
}

func (r *rig) record(name string) health.Record {
	r.t.Helper()
	for _, rec := range r.sup.Records(context.Background()) {
		if rec.Name == name {
			return rec
		}
	}
	r.t.Fatalf("no record for %s", name)
	return health.Record{}
}

func (r *rig) state(name string) health.State { return r.record(name).State }

// tick advances the clock and runs one supervisor round.
func (r *rig) tick(d time.Duration) {
	r.clock.Advance(d)
	r.sup.Tick(context.Background())
}

func (r *rig) restarts(unit string) int {
	n := 0
	for _, c := range r.mgr.Calls() {
		if c.Verb == services.VerbRestart && c.Unit == unit {
			n++
		}
	}
	return n
}

func (r *rig) codes(name string) []string {
	var out []string
	for _, p := range r.record(name).Problems {
		out = append(out, p.Code)
	}
	return out
}

// logLines decodes the JSON log lines the supervisor wrote.
func (r *rig) logLines() []map[string]any {
	var out []map[string]any
	for _, line := range bytes.Split(r.log.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			r.t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ config.Clock = (*cfake.Clock)(nil)
