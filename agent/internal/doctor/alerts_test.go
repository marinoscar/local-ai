package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/events"
)

// pubEvent is one published event.
type pubEvent struct {
	Type string
	Data events.Alert
}

// fakeBus records what the manager publishes.
type fakeBus struct {
	mu   sync.Mutex
	evs  []pubEvent
	fail error
}

func (b *fakeBus) Publish(typ, opID string, data any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	if opID != "" {
		panic("alert events carry no op id")
	}
	b.evs = append(b.evs, pubEvent{typ, data.(events.Alert)})
	return nil
}

func (b *fakeBus) take() []pubEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.evs
	b.evs = nil
	return out
}

// script is a fast check whose results the test changes between passes.
type script struct {
	mu  sync.Mutex
	res []Result
}

func (s *script) set(res ...Result) {
	s.mu.Lock()
	s.res = res
	s.mu.Unlock()
}

func (s *script) check(id, area string) Check {
	return Check{ID: id, Area: area, Fast: true, Run: func(context.Context) []Result {
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]Result(nil), s.res...)
	}}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newManager(t *testing.T, checks ...Check) (*Manager, *fakeBus, *clock) {
	t.Helper()
	reg := newReg(t, checks...)
	bus := &fakeBus{}
	clk := &clock{t: time.Date(2026, 11, 2, 8, 0, 30, 0, time.UTC)}
	return NewManager(ManagerOptions{Registry: reg, Bus: bus, Now: clk.now}), bus, clk
}

func eventKinds(evs []pubEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Type+" "+e.Data.Code)
	}
	return out
}

func TestAlertRaisedKeptAndCleared(t *testing.T) {
	var s script
	m, bus, clk := newManager(t, s.check("hw.battery", "HW"))
	ctx := context.Background()

	// A passing first pass changes nothing.
	m.Tick(ctx)
	if len(m.Active()) != 0 || len(bus.take()) != 0 {
		t.Fatal("a clean pass must raise nothing")
	}

	// First failing run raises.
	s.set(Result{Code: HW012, Details: map[string]any{"battery_percent": 64}})
	m.Tick(ctx)
	evs := bus.take()
	if got, want := eventKinds(evs), []string{"alert.raised LAI-HW-012"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if e := evs[0].Data; e.Severity != "warning" || e.Title != "Running on battery" || e.Fix == "" {
		t.Errorf("event data = %+v", e)
	}
	active := m.Active()
	if len(active) != 1 {
		t.Fatalf("active = %+v", active)
	}
	since := active[0].Since
	if !since.Equal(time.Date(2026, 11, 2, 8, 0, 30, 0, time.UTC)) || active[0].Details["battery_percent"] != 64 {
		t.Errorf("alert = %+v", active[0])
	}

	// Still failing: kept, no new event, Since unchanged, details refreshed.
	clk.add(time.Minute)
	s.set(Result{Code: HW012, Details: map[string]any{"battery_percent": 51}})
	m.Tick(ctx)
	if evs := bus.take(); len(evs) != 0 {
		t.Fatalf("a kept alert must not publish: %v", eventKinds(evs))
	}
	if a := m.Active()[0]; !a.Since.Equal(since) || a.Details["battery_percent"] != 51 {
		t.Errorf("kept alert = %+v, want same Since and fresh details", a)
	}

	// First passing run clears.
	clk.add(time.Minute)
	s.set()
	m.Tick(ctx)
	if got, want := eventKinds(bus.take()), []string{"alert.cleared LAI-HW-012"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if len(m.Active()) != 0 {
		t.Fatal("alert still active after a pass")
	}

	// Raising again starts a new alert with a new Since.
	clk.add(time.Minute)
	s.set(Result{Code: HW012})
	m.Tick(ctx)
	if len(bus.take()) != 1 || !m.Active()[0].Since.After(since) {
		t.Errorf("a re-raised alert must be new: %+v", m.Active())
	}
}

func TestAlertEscalationPublishesAgain(t *testing.T) {
	var s script
	m, bus, _ := newManager(t, s.check("agt.state_intact", "AGT"))
	s.set(Result{Code: AGT003})
	m.Tick(context.Background())
	s.set(Result{Code: AGT003, Severity: Critical}) // defaults and offline mode were used
	m.Tick(context.Background())
	evs := bus.take()
	if len(evs) != 2 || evs[0].Data.Severity != "warning" || evs[1].Type != events.TypeAlertRaised || evs[1].Data.Severity != "critical" {
		t.Fatalf("events = %+v, want raised(warning) then raised(critical)", evs)
	}
	if a := m.Active()[0]; a.Severity != Critical {
		t.Errorf("alert severity = %s", a.Severity)
	}
}

func TestSeveralChecksSameCodeAndSeveralCodes(t *testing.T) {
	var a, b script
	m, bus, _ := newManager(t, a.check("net.one", "NET"), b.check("net.two", "NET"))
	ctx := context.Background()
	a.set(Result{Code: NET013}, Result{Code: NET011})
	b.set(Result{Code: NET013})
	m.Tick(ctx)
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.raised LAI-NET-011", "alert.raised LAI-NET-013"}) {
		t.Fatalf("events = %v (one alert per code, in code order)", got)
	}
	// One of the two checks recovers: the alert stays while the other still reports the code.
	a.set(Result{Code: NET011})
	m.Tick(ctx)
	if evs := bus.take(); len(evs) != 0 {
		t.Fatalf("events = %v, want none", eventKinds(evs))
	}
	b.set()
	m.Tick(ctx)
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.cleared LAI-NET-013"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestEventDrivenAlertsAndChecksDoNotClearEachOther(t *testing.T) {
	var s script
	m, bus, _ := newManager(t, s.check("agt.config", "AGT"))
	ctx := context.Background()

	if err := m.Raise(AGT016, map[string]any{"target": "agent"}); err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.raised LAI-AGT-016"}) {
		t.Fatalf("events = %v", got)
	}
	// A clean pass of the checks must not clear an alert raised by an event.
	m.Tick(ctx)
	if len(m.Active()) != 1 || len(bus.take()) != 0 {
		t.Fatal("a clean pass cleared an event-driven alert")
	}
	// A second Raise replaces the details and keeps Since without a new event.
	if err := m.Raise(AGT016, map[string]any{"target": "llama"}); err != nil {
		t.Fatal(err)
	}
	if a := m.Active()[0]; a.Details["target"] != "llama" || len(bus.take()) != 0 {
		t.Errorf("alert = %+v", a)
	}

	// Now a check reports it as well: Clear must not clear it, the check pass does.
	s.set(Result{Code: AGT016})
	m.Tick(ctx)
	m.Clear(AGT016)
	if len(m.Active()) != 1 || len(bus.take()) != 0 {
		t.Fatal("Clear removed an alert a check still reports")
	}
	s.set()
	m.Tick(ctx)
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.cleared LAI-AGT-016"}) {
		t.Fatalf("events = %v", got)
	}

	// Event-only alert: Clear withdraws it; clearing twice or clearing an unknown code is harmless.
	if err := m.Raise(SEC009, nil); err != nil {
		t.Fatal(err)
	}
	bus.take()
	m.Clear(SEC009)
	m.Clear(SEC009)
	m.Clear(unknownCode("SEC"))
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.cleared LAI-SEC-009"}) {
		t.Fatalf("events = %v", got)
	}
	if len(m.Active()) != 0 {
		t.Fatalf("active = %+v", m.Active())
	}
}

func TestRaiseSeverityOverrideAndErrors(t *testing.T) {
	m, bus, _ := newManager(t)
	if err := m.Raise(SEC004, map[string]any{"retry_after_s": 300}, WithSeverity(Critical)); err != nil {
		t.Fatal(err)
	}
	if a := m.Active()[0]; a.Severity != Critical {
		t.Errorf("severity = %s, want the override", a.Severity)
	}
	if evs := bus.take(); len(evs) != 1 || evs[0].Data.Severity != "critical" {
		t.Errorf("events = %+v", evs)
	}
	if err := m.Raise(SEC004, nil); err != nil { // back to the default: published as a change
		t.Fatal(err)
	}
	if a := m.Active()[0]; a.Severity != Warning {
		t.Errorf("severity = %s, want the default", a.Severity)
	}

	for _, tc := range []struct {
		name string
		code string
		opts []RaiseOption
		want error
	}{
		{"unknown code", unknownCode("SEC"), nil, ErrUnknownCode},
		{"retired code", KIT022, nil, ErrUnknownCode},
		{"empty code", "", nil, ErrUnknownCode},
		{"bad severity", SEC004, []RaiseOption{WithSeverity("fatal")}, ErrBadSeverity},
		{"retired is not a severity", SEC004, []RaiseOption{WithSeverity(Retired)}, ErrBadSeverity},
	} {
		if err := m.Raise(tc.code, nil, tc.opts...); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestActiveOrderAndCopies(t *testing.T) {
	m, _, clk := newManager(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(m.Raise(AGT015, nil)) // info
	clk.add(time.Second)
	must(m.Raise(HW012, map[string]any{"k": "v"})) // warning
	clk.add(time.Second)
	must(m.Raise(HW013, nil)) // warning, newer
	clk.add(time.Second)
	must(m.Raise(AGT004, nil)) // critical, newest
	var got []string
	for _, a := range m.Active() {
		got = append(got, a.Code)
	}
	if want := []string{AGT004, HW012, HW013, AGT015}; !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v (severity, then oldest, then code)", got, want)
	}
	m.Active()[1].Details["k"] = "changed"
	if m.Active()[1].Details["k"] != "v" {
		t.Error("Active leaked the details map")
	}
}

func TestAlertIn(t *testing.T) {
	m, _, _ := newManager(t)
	if err := m.Raise(HW012, nil); err != nil {
		t.Fatal(err)
	}
	a := m.Active()[0]
	if a.Title != "Running on battery" {
		t.Errorf("Active() text must be English: %q", a.Title)
	}
	es := a.In("es")
	if es.Code != a.Code || es.Severity != a.Severity || es.Title != "Funcionando con batería" {
		t.Errorf("In(es) = %+v", es)
	}
	if a.In("fr").Title != a.Title {
		t.Error("an unknown language must stay English")
	}
}

func TestTickIgnoresACancelledPass(t *testing.T) {
	var s script
	m, bus, _ := newManager(t, s.check("hw.battery", "HW"))
	s.set(Result{Code: HW012})
	m.Tick(context.Background())
	bus.take()
	s.set() // would clear...
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ...but the agent is shutting down
	m.Tick(ctx)
	if len(m.Active()) != 1 || len(bus.take()) != 0 {
		t.Fatal("a pass cut short by shutdown must change nothing")
	}
}

func TestAStuckCheckRaisesAGT099AndRecovers(t *testing.T) {
	release := make(chan struct{})
	reg := newReg(t, Check{ID: "hw.stuck", Area: "HW", Fast: true, Timeout: 20 * time.Millisecond,
		Run: func(context.Context) []Result { <-release; return nil }})
	bus := &fakeBus{}
	m := NewManager(ManagerOptions{Registry: reg, Bus: bus})
	m.Tick(context.Background())
	evs := bus.take()
	if len(evs) != 1 || evs[0].Data.Code != AGT099 || evs[0].Data.Severity != "error" {
		t.Fatalf("events = %+v", evs)
	}
	if a := m.Active()[0]; a.Details["check"] != "hw.stuck" || a.Details["reason"] != "timeout" {
		t.Errorf("details = %+v", a.Details)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for len(m.Active()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("alert never cleared after the check recovered")
		}
		time.Sleep(10 * time.Millisecond)
		m.Tick(context.Background())
	}
}

func TestSlowChecksDoNotDriveAlerts(t *testing.T) {
	reg := newReg(t, problemCheck("net.dns", "NET", false, Result{Code: NET013}))
	m := NewManager(ManagerOptions{Registry: reg})
	m.Tick(context.Background())
	if len(m.Active()) != 0 {
		t.Fatal("only fast checks run on the timer")
	}
}

func TestAPublishErrorDoesNotStopTheAlert(t *testing.T) {
	bus := &fakeBus{fail: errors.New("refused")}
	m := NewManager(ManagerOptions{Registry: NewRegistry(), Bus: bus})
	if err := m.Raise(HW012, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.Active()) != 1 {
		t.Fatal("the alert must exist even when the event was refused")
	}
	m.Clear(HW012)
	if len(m.Active()) != 0 {
		t.Fatal("the alert must clear even when the event was refused")
	}
}

func TestNoBusIsFine(t *testing.T) {
	m := NewManager(ManagerOptions{Registry: NewRegistry()})
	if err := m.Raise(HW012, nil); err != nil {
		t.Fatal(err)
	}
	m.Clear(HW012)
}

// The manager publishes through the real bus: the payload passes the bus's typed registry and
// reaches a subscriber with the documented shape.
func TestAlertEventsThroughTheRealBus(t *testing.T) {
	bus := events.New(events.Options{Node: "node-a"})
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	filter, err := events.NewFilter("alert.*")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := bus.Subscribe(ctx, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(ManagerOptions{Registry: NewRegistry(), Bus: bus})
	if err := m.Raise(HW012, map[string]any{"battery_percent": 64}); err != nil {
		t.Fatal(err)
	}
	m.Clear(HW012)

	recv := func() events.Event {
		select {
		case ev := <-ch:
			return ev
		case <-time.After(2 * time.Second):
			t.Fatal("no event")
		}
		return events.Event{}
	}
	raised, cleared := recv(), recv()
	if raised.Type != "alert.raised" || cleared.Type != "alert.cleared" {
		t.Fatalf("types = %s, %s", raised.Type, cleared.Type)
	}
	var data map[string]any
	if err := json.Unmarshal(raised.Data, &data); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"code": HW012, "severity": "warning", "title": "Running on battery",
		"fix": "Plug in the charger. A laptop server stops when the battery runs out"}
	if !reflect.DeepEqual(data, want) {
		t.Errorf("data = %v, want %v (details never go out on the bus)", data, want)
	}
	if bus.Counters().Rejected != 0 {
		t.Error("the bus rejected a doctor event")
	}
}

func TestStartRunsAtOnceThenEveryInterval(t *testing.T) {
	var s script
	var runs atomicCounter
	reg := newReg(t, Check{ID: "hw.battery", Area: "HW", Fast: true, Run: func(ctx context.Context) []Result {
		runs.inc()
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]Result(nil), s.res...)
	}})
	bus := &fakeBus{}
	m := NewManager(ManagerOptions{Registry: reg, Bus: bus, Interval: 10 * time.Millisecond})
	s.set(Result{Code: HW012})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Error("a second Start must fail")
	}
	waitFor(t, "the first pass and an alert", func() bool { return len(m.Active()) == 1 })
	waitFor(t, "periodic passes", func() bool { return runs.get() >= 3 })
	s.set()
	waitFor(t, "the alert to clear on a later pass", func() bool { return len(m.Active()) == 0 })

	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := runs.get()
	time.Sleep(50 * time.Millisecond)
	if runs.get() != n {
		t.Error("passes continued after Stop")
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("a second Stop must be harmless: %v", err)
	}
	if m.Name() != "doctor" {
		t.Errorf("Name = %q", m.Name())
	}
	if got := eventKinds(bus.take()); !reflect.DeepEqual(got, []string{"alert.raised LAI-HW-012", "alert.cleared LAI-HW-012"}) {
		t.Errorf("events = %v", got)
	}
}

func TestStopCancelsAPassInProgress(t *testing.T) {
	started := make(chan struct{}, 1)
	reg := newReg(t, Check{ID: "hw.wait", Area: "HW", Fast: true, Run: func(ctx context.Context) []Result {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return []Result{{Code: HW012}}
	}})
	m := NewManager(ManagerOptions{Registry: reg, Interval: time.Hour})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("Stop = %v", err)
	}
	if len(m.Active()) != 0 {
		t.Error("a cancelled pass must not raise")
	}
}

type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() { c.mu.Lock(); c.n++; c.mu.Unlock() }
func (c *atomicCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
