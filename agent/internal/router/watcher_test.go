package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	confake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/router/fake"
)

// The watcher is an app.Worker. internal/app imports this package (it wires the daemon), so the
// interface is spelled out here instead of imported.
var _ interface {
	Name() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
} = (*Watcher)(nil)

// pubRec records the events the watcher publishes.
type pubRec struct {
	mu  sync.Mutex
	evs []recEvent
}

type recEvent struct {
	typ  string
	opID string
	data any
}

func (p *pubRec) Publish(typ, opID string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evs = append(p.evs, recEvent{typ, opID, data})
	return nil
}

// take returns the events recorded since the last call.
func (p *pubRec) take() []recEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.evs
	p.evs = nil
	return out
}

// alertRec records Raise and Clear calls as "code|subject".
type alertRec struct {
	mu      sync.Mutex
	raised  []string
	cleared []string
}

func (a *alertRec) Raise(code, subject string, _ map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.raised = append(a.raised, code+"|"+subject)
}

func (a *alertRec) Clear(code, subject string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleared = append(a.cleared, code+"|"+subject)
}

func (a *alertRec) snapshot() (raised, cleared []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.raised...), append([]string(nil), a.cleared...)
}

// wrig is a watcher over the HTTP fake, with a manual clock and recorders.
type wrig struct {
	*rig
	w      *Watcher
	clock  *confake.Clock
	pub    *pubRec
	alerts *alertRec
}

func newWRig(t *testing.T, mutate ...func(*WatcherOptions)) *wrig {
	t.Helper()
	r := newRig(t, func(o *Options) { o.Timeout = 400 * time.Millisecond })
	r.fake.Add(fake.Model{ID: "model-c-q4km"})
	wr := &wrig{rig: r, clock: confake.NewClock(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), pub: &pubRec{}, alerts: &alertRec{}}
	o := WatcherOptions{
		Client: r.client, Publisher: wr.pub, Alerts: wr.alerts, Clock: wr.clock, Logger: r.logs.logger(),
		ModelsMax: func() int { return 2 },
		PresetIDs: func() []string { return []string{"model-a-q4km", "model-b-q4km", "model-c-q4km"} },
	}
	for _, m := range mutate {
		m(&o)
	}
	wr.w = NewWatcher(o)
	// The key must never reach a log line, whatever a test does.
	t.Cleanup(func() { r.noLeak(t) })
	return wr
}

func (wr *wrig) poll(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return wr.w.Poll(ctx)
}

// types summarises recorded events as "type model reason|load_ms".
func summarize(evs []recEvent) []string {
	var out []string
	for _, e := range evs {
		switch d := e.data.(type) {
		case events.ModelUnloaded:
			out = append(out, fmt.Sprintf("%s %s %s", e.typ, d.Model, d.Reason))
		case events.ModelLoaded:
			out = append(out, fmt.Sprintf("%s %s %d", e.typ, d.Model, d.LoadMS))
		default:
			out = append(out, fmt.Sprintf("%s %v", e.typ, e.data))
		}
		if e.opID != "" {
			out[len(out)-1] += " op=" + e.opID
		}
	}
	return out
}

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func codesOf(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, string(f.Code)+"|"+f.Subject)
	}
	sort.Strings(out)
	return out
}

func TestWatcherFirstViewIsABaseline(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	if v := wr.w.View(); !v.Stale || v.Models != nil || !v.At.IsZero() {
		t.Errorf("view before any poll = %+v", v)
	}
	if !wr.poll(t) {
		t.Fatal("poll failed")
	}
	equal(t, summarize(wr.pub.take()), nil) // loaded at start: nothing happened outside an operation
	v := wr.w.View()
	if v.Stale || len(v.Models) != 3 || v.At != wr.clock.Now() || v.Err != "" {
		t.Errorf("view = %+v", v)
	}
	// The view is a copy.
	v.Models[0].ID = "changed"
	if wr.w.View().Models[0].ID == "changed" {
		t.Error("View must return a copy")
	}
}

func TestWatcherCrashOutsideAnOperation(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)

	wr.fake.Crash("model-a-q4km", 1)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-a-q4km crash"})
	equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-009|model-a-q4km"})
	if r, _ := wr.alerts.snapshot(); !reflect.DeepEqual(r, []string{"LAI-LLM-009|model-a-q4km"}) {
		t.Errorf("raised = %v", r)
	}
	f := wr.w.Findings()[0]
	if f.Details["exit_code"] != 1 || f.Since != wr.clock.Now() {
		t.Errorf("finding = %+v", f)
	}

	// Still failed on the next poll: no second event, no second alert.
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil)
	if r, _ := wr.alerts.snapshot(); len(r) != 1 {
		t.Errorf("the alert was raised again: %v", r)
	}

	// The model is started again (failure cleared): the alert goes, and its load is reported.
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.loaded model-a-q4km 0"})
	if _, c := wr.alerts.snapshot(); !reflect.DeepEqual(c, []string{"LAI-LLM-009|model-a-q4km"}) {
		t.Errorf("cleared = %v", c)
	}
	if len(wr.w.Findings()) != 0 {
		t.Errorf("findings = %+v", wr.w.Findings())
	}
}

func TestWatcherLoadedOutsideAnOperationHasTheObservedLoadTime(t *testing.T) {
	wr := newWRig(t)
	wr.poll(t) // baseline: everything unloaded

	wr.fake.Set("model-a-q4km", fake.Loading)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil) // loading is not published by the watcher (the orchestrator owns model.loading)
	wr.clock.Advance(7100 * time.Millisecond)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.loaded model-a-q4km 7100"})

	// A load never seen in progress reports 0: the duration is not known.
	wr.fake.Set("model-b-q4km", fake.Loaded)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.loaded model-b-q4km 0"})
	for _, e := range wr.pub.take() {
		if e.opID != "" {
			t.Errorf("a watcher event carries an op id: %+v", e)
		}
	}
}

func TestWatcherStatusTransitions(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
		want []string
	}{
		{"loaded to unloaded is a crash", fake.Loaded, fake.Unloaded, []string{"model.unloaded m crash"}},
		{"loaded to loading (child restarted) is a crash", fake.Loaded, fake.Loading, []string{"model.unloaded m crash"}},
		{"loaded to sleeping is nothing", fake.Loaded, fake.Sleeping, nil},
		{"sleeping to loaded is nothing", fake.Sleeping, fake.Loaded, nil},
		{"sleeping to unloaded is a crash", fake.Sleeping, fake.Unloaded, []string{"model.unloaded m crash"}},
		{"unloaded to loaded", fake.Unloaded, fake.Loaded, []string{"model.loaded m 0"}},
		{"unloaded to loading is nothing", fake.Unloaded, fake.Loading, nil},
		{"loading to unloaded is nothing (a failed start is LLM-009's)", fake.Loading, fake.Unloaded, nil},
		{"loaded to loaded is nothing", fake.Loaded, fake.Loaded, nil},
		{"downloading to downloaded is nothing", fake.Downloading, fake.Downloaded, nil},
		{"loaded to incomplete is a crash", fake.Loaded, fake.Incomplete, []string{"model.unloaded m crash"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil; o.ModelsMax = nil })
			wr.fake.Add(fake.Model{ID: "m", Status: c.from})
			wr.poll(t)
			wr.pub.take()
			wr.fake.Set("m", c.to)
			wr.poll(t)
			equal(t, summarize(wr.pub.take()), c.want)
		})
	}
}

func TestWatcherModelRemovedWhileLoaded(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	wr.fake.Remove("model-a-q4km") // a reload dropped its section: the router unloads it
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-a-q4km preset_changed"})
	// An unloaded model that disappears is not news.
	wr.fake.Remove("model-b-q4km")
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil)
}

func TestWatcherSuppressedOperationIsSilent(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)

	release := wr.w.Suppress("op_1")
	wr.fake.Set("model-a-q4km", fake.Unloaded) // the operation evicts it
	wr.fake.Set("model-b-q4km", fake.Loaded)   // and loads another
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil)
	release()
	release() // idempotent
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil) // the result of the operation is the new baseline

	// After the operation the watcher is live again.
	wr.fake.Crash("model-b-q4km", 9)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-b-q4km crash"})
}

func TestWatcherSuppressCountsHolders(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	r1 := wr.w.Suppress("op_1")
	r2 := wr.w.Suppress("op_2")
	r1()
	wr.fake.Set("model-a-q4km", fake.Unloaded)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil) // op_2 still runs
	r2()
}

// stubRouter lets a test hold a poll in flight.
type stubRouter struct {
	Router
	models func(ctx context.Context) ([]ModelStatus, error)
}

func (s *stubRouter) Models(ctx context.Context) ([]ModelStatus, error) {
	if s.models == nil {
		return nil, nil
	}
	return s.models(ctx)
}

func (s *stubRouter) Progress(context.Context) (<-chan Progress, error) {
	return nil, errors.New("no stream")
}

func TestWatcherPollOverlappingAnOperationIsQuiet(t *testing.T) {
	for _, withOp := range []bool{true, false} {
		t.Run(fmt.Sprintf("operation=%v", withOp), func(t *testing.T) {
			var mu sync.Mutex
			cur := []ModelStatus{{ID: "m", Status: StatusLoaded}}
			block := false
			enter, proceed := make(chan struct{}), make(chan struct{})
			stub := &stubRouter{models: func(ctx context.Context) ([]ModelStatus, error) {
				mu.Lock()
				out, b := cur, block
				mu.Unlock()
				if b {
					enter <- struct{}{}
					<-proceed
				}
				return out, nil
			}}
			pub := &pubRec{}
			w := NewWatcher(WatcherOptions{Client: stub, Publisher: pub, Clock: confake.NewClock(time.Now())})
			w.Poll(context.Background()) // baseline: loaded

			// A poll starts and is held; meanwhile the model goes away and, maybe, an operation runs
			// and ends; then the poll returns what the router said.
			mu.Lock()
			block, cur = true, []ModelStatus{{ID: "m", Status: StatusUnloaded}}
			mu.Unlock()
			done := make(chan struct{})
			go func() { w.Poll(context.Background()); close(done) }()
			<-enter
			if withOp {
				release := w.Suppress("op_1")
				release()
			}
			proceed <- struct{}{}
			<-done
			got := summarize(pub.take())
			if withOp {
				equal(t, got, nil) // the change may be the operation's own
			} else {
				equal(t, got, []string{"model.unloaded m crash"})
			}
		})
	}
}

func TestWatcherFindings(t *testing.T) {
	t.Run("LLM-024 cache models", func(t *testing.T) {
		wr := newWRig(t)
		wr.fake.Add(fake.Model{ID: "org/stray:Q8_0", Source: "cache"})
		wr.poll(t)
		equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-024|"})
		if r, _ := wr.alerts.snapshot(); len(r) != 1 {
			t.Errorf("raised = %v", r)
		}
		wr.poll(t)
		if r, _ := wr.alerts.snapshot(); len(r) != 1 {
			t.Errorf("raised again: %v", r)
		}
		wr.fake.Remove("org/stray:Q8_0")
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("findings = %+v", wr.w.Findings())
		}
		if _, c := wr.alerts.snapshot(); !reflect.DeepEqual(c, []string{"LAI-LLM-024|"}) {
			t.Errorf("cleared = %v", c)
		}
	})
	t.Run("LLM-024 id not in the preset", func(t *testing.T) {
		wr := newWRig(t)
		wr.fake.Add(fake.Model{ID: "not-in-the-ini", Source: "preset"})
		wr.poll(t)
		f := wr.w.Findings()
		if len(f) != 1 || f[0].Code != CodeCacheModels || !reflect.DeepEqual(f[0].Details["models"], []string{"not-in-the-ini"}) {
			t.Errorf("findings = %+v", f)
		}
	})
	t.Run("LLM-024 off without the ids and without a cache source", func(t *testing.T) {
		wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil })
		wr.fake.Add(fake.Model{ID: "anything", Source: "preset"})
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("findings = %+v", wr.w.Findings())
		}
	})
	t.Run("LLM-028 too many loaded", func(t *testing.T) {
		wr := newWRig(t)
		wr.fake.Set("model-a-q4km", fake.Loaded)
		wr.fake.Set("model-b-q4km", fake.Loaded)
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Fatalf("2 loaded with models_max 2 is fine: %+v", wr.w.Findings())
		}
		wr.fake.Set("model-c-q4km", fake.Loaded)
		wr.poll(t)
		f := wr.w.Findings()
		if len(f) != 1 || f[0].Code != CodeTooManyLoaded || f[0].Details["models_max"] != 2 {
			t.Fatalf("findings = %+v", f)
		}
		if got := f[0].Details["loaded"]; !reflect.DeepEqual(got, []string{"model-a-q4km", "model-b-q4km", "model-c-q4km"}) {
			t.Errorf("loaded = %v", got)
		}
		wr.fake.Set("model-c-q4km", fake.Unloaded)
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("findings = %+v", wr.w.Findings())
		}
	})
	t.Run("LLM-028 is not evaluated during an operation", func(t *testing.T) {
		wr := newWRig(t)
		wr.poll(t)
		release := wr.w.Suppress("op_1")
		for _, id := range []string{"model-a-q4km", "model-b-q4km", "model-c-q4km"} {
			wr.fake.Set(id, fake.Loaded) // the transient overshoot of an evict-then-load
		}
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("raised during an operation: %+v", wr.w.Findings())
		}
		release()
		wr.poll(t)
		equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-028|"})
	})
	t.Run("LLM-028 off when models_max is not set", func(t *testing.T) {
		wr := newWRig(t, func(o *WatcherOptions) { o.ModelsMax = func() int { return 0 } })
		for _, id := range []string{"model-a-q4km", "model-b-q4km", "model-c-q4km"} {
			wr.fake.Set(id, fake.Loaded)
		}
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("findings = %+v", wr.w.Findings())
		}
	})
	t.Run("LLM-009 on the first view and for several models", func(t *testing.T) {
		wr := newWRig(t)
		wr.fake.Crash("model-a-q4km", 1)
		wr.fake.Crash("model-b-q4km", 137)
		wr.poll(t)
		equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-009|model-a-q4km", "LAI-LLM-009|model-b-q4km"})
		equal(t, summarize(wr.pub.take()), nil) // never loaded while watched: no crash event
	})
	t.Run("LLM-009 is kept while an operation runs", func(t *testing.T) {
		wr := newWRig(t)
		wr.fake.Crash("model-a-q4km", 1)
		wr.poll(t)
		release := wr.w.Suppress("op_1")
		wr.fake.Set("model-a-q4km", fake.Loading) // the orchestrator retries it
		wr.poll(t)
		equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-009|model-a-q4km"})
		release()
		wr.poll(t)
		if len(wr.w.Findings()) != 0 {
			t.Errorf("findings = %+v", wr.w.Findings())
		}
	})
}

func TestWatcherRouterDownAndRestart(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	wr.pub.take()

	wr.fake.Down(true)
	for i := 1; i <= 2; i++ {
		if wr.poll(t) {
			t.Fatal("poll against a down router succeeded")
		}
		if len(wr.w.Findings()) != 0 {
			t.Fatalf("raised after %d failures: a restart takes a few seconds", i)
		}
	}
	v := wr.w.View()
	if !v.Stale || v.Err == "" || len(v.Models) != 3 {
		t.Errorf("a failed poll must mark the view stale and keep the last picture: %+v", v)
	}
	wr.poll(t)
	equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-004|"})
	equal(t, summarize(wr.pub.take()), nil) // down is not a crash of a model

	// The router comes back after a restart: its children are gone.
	wr.fake.Restart()
	wr.fake.Down(false)
	if !wr.poll(t) {
		t.Fatal("poll after recovery failed")
	}
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-a-q4km crash"})
	if len(wr.w.Findings()) != 0 {
		t.Errorf("004 not cleared: %+v", wr.w.Findings())
	}
	if _, c := wr.alerts.snapshot(); !reflect.DeepEqual(c, []string{"LAI-LLM-004|"}) {
		t.Errorf("cleared = %v", c)
	}
	if v := wr.w.View(); v.Stale || v.Err != "" {
		t.Errorf("view after recovery = %+v", v)
	}
}

func TestWatcherRestartInsideAnOperationIsSilent(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	release := wr.w.Suppress("op_restart")
	wr.fake.Reset(true) // the unit is being restarted: connections drop
	wr.poll(t)
	wr.fake.Restart()
	wr.fake.Reset(false)
	wr.poll(t)
	release()
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), nil)
}

func TestWatcherRestartMidPoll(t *testing.T) {
	// The router dies while a poll is in flight, then comes back: the poll fails cleanly, the next
	// one diffs against the last good picture.
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	wr.fake.Delay(2 * time.Second) // the answer is slower than the client timeout
	if wr.poll(t) {
		t.Fatal("a slow poll must fail")
	}
	if v := wr.w.View(); !v.Stale || !strings.Contains(v.Err, "no answer within") {
		t.Errorf("view = %+v", v)
	}
	wr.fake.Delay(0)
	wr.fake.Crash("model-a-q4km", 1)
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-a-q4km crash"})
}

func TestWatcherKeyRejected(t *testing.T) {
	wr := newWRig(t)
	wr.poll(t)
	wr.fake.SetKey(secret(7)) // the unit has a key our file does not
	wr.poll(t)
	if len(wr.w.Findings()) != 0 {
		t.Fatalf("one 401 is not enough: %+v", wr.w.Findings())
	}
	wr.poll(t)
	equal(t, codesOf(wr.w.Findings()), []string{"LAI-LLM-005|"}) // and not 004: the router answers
	if v := wr.w.View(); !v.Stale || !strings.Contains(v.Err, "HTTP 401") {
		t.Errorf("view = %+v", v)
	}

	wr.fake.SetKey(wr.bearer)
	wr.poll(t)
	if len(wr.w.Findings()) != 0 {
		t.Errorf("005 not cleared: %+v", wr.w.Findings())
	}
	if _, c := wr.alerts.snapshot(); !reflect.DeepEqual(c, []string{"LAI-LLM-005|"}) {
		t.Errorf("cleared = %v", c)
	}
}

func TestWatcherRotationIsAbsorbed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "api-key")
	m0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	writeKey(t, path, secret(1), m0)
	wr := newWRig(t, func(o *WatcherOptions) {})
	c, err := NewClient(Options{BaseURL: wr.fake.URL(), Keys: NewFileKey(nil, path), Logger: wr.logs.logger()})
	if err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(WatcherOptions{Client: c, Publisher: wr.pub, Alerts: wr.alerts, Clock: wr.clock, Logger: wr.logs.logger()})
	if !w.Poll(context.Background()) {
		t.Fatal("first poll failed")
	}
	wr.fake.SetKey(secret(2))
	writeKey(t, path, secret(2), m0)
	if !w.Poll(context.Background()) {
		t.Fatal("a rotation must not fail the poll")
	}
	if r, _ := wr.alerts.snapshot(); len(r) != 0 {
		t.Errorf("alerts during a rotation: %v", r)
	}
}

func TestWatcherBadAnswersKeepTheLastView(t *testing.T) {
	cases := []struct {
		name   string
		script func(*wrig)
	}{
		{"not json", func(wr *wrig) { wr.fake.Malformed("<html>502 Bad Gateway</html>") }},
		{"half a list", func(wr *wrig) { wr.fake.Malformed(`{"data":[{"id":"a","status":{"value":"loaded"}},{"id":"b"}]}`) }},
		{"forced 500", func(wr *wrig) { wr.fake.Force(500, `{"error":{"message":"boom"}}`) }},
		{"redirect", func(wr *wrig) { wr.fake.Redirect(true) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wr := newWRig(t)
			wr.fake.Set("model-a-q4km", fake.Loaded)
			wr.poll(t)
			c.script(wr)
			for i := 0; i < 5; i++ {
				if wr.poll(t) {
					t.Fatal("poll succeeded")
				}
			}
			v := wr.w.View()
			if !v.Stale || len(v.Models) != 3 || !v.Models[0].Loaded() && !v.Models[1].Loaded() && !v.Models[2].Loaded() {
				t.Errorf("view = %+v", v)
			}
			if len(wr.w.Findings()) != 0 {
				t.Errorf("a bad answer is not a doctor code: %+v", wr.w.Findings())
			}
			equal(t, summarize(wr.pub.take()), nil)
		})
	}
}

func TestWatcherUnknownStatusIsLoggedAndTreatedAsNotRunning(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	wr.fake.Set("model-a-q4km", "hibernating")
	wr.poll(t)
	equal(t, summarize(wr.pub.take()), []string{"model.unloaded model-a-q4km crash"})
	if !strings.Contains(wr.logs.String(), "status this agent does not know") {
		t.Error("the unknown status was not logged")
	}
}

func TestWatcherNeverCallsForbiddenRoutes(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Set("model-a-q4km", fake.Loaded)
	wr.poll(t)
	wr.fake.Crash("model-a-q4km", 1)
	wr.fake.Down(true)
	wr.poll(t)
	wr.fake.Down(false)
	wr.poll(t)
	if len(wr.fake.Requests()) < 3 {
		t.Fatal("no traffic")
	}
	onlyAllowedCalls(t, wr.fake)
	for _, q := range wr.fake.Requests() {
		if q.Method != "GET" || q.Path != "/models" {
			t.Errorf("the watcher polls with GET /models only, saw %s", q)
		}
	}
}

func TestBackoffSkips(t *testing.T) {
	s := time.Second
	cases := []struct {
		fails          int
		interval, max_ time.Duration
		want           int
	}{
		{1, 5 * s, 30 * s, 1}, {2, 5 * s, 30 * s, 3}, {3, 5 * s, 30 * s, 5}, {4, 5 * s, 30 * s, 5}, {20, 5 * s, 30 * s, 5},
		{1, 5 * s, 5 * s, 0}, {1, 10 * time.Millisecond, 40 * time.Millisecond, 1},
	}
	for _, c := range cases {
		if got := backoffSkips(c.fails, c.interval, c.max_); got != c.want {
			t.Errorf("backoffSkips(%d, %s, %s) = %d, want %d", c.fails, c.interval, c.max_, got, c.want)
		}
	}
}

func TestNewWatcherDefaults(t *testing.T) {
	w := NewWatcher(WatcherOptions{Client: &stubRouter{}})
	if w.opt.Interval != DefaultPollInterval || w.opt.MaxBackoff != DefaultMaxBackoff || w.opt.DownAfter != DefaultDownAfter ||
		w.opt.UnauthorizedAfter != DefaultUnauthorizedAfter || w.clock == nil || w.Name() != "router-watcher" {
		t.Errorf("defaults not applied: %+v", w.opt)
	}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = w.Stop(context.Background())
	if err := NewWatcher(WatcherOptions{}).Start(context.Background()); err == nil {
		t.Error("a watcher without a client must not start")
	}
}

// mgrRec records the calls the adapter makes on the doctor manager.
type mgrRec struct {
	calls []string
}

func (m *mgrRec) Raise(code string, details map[string]any, _ ...doctor.RaiseOption) error {
	m.calls = append(m.calls, fmt.Sprintf("raise %s %v", code, details))
	return nil
}

func (m *mgrRec) Clear(code string) { m.calls = append(m.calls, "clear "+code) }

func TestManagerAlerts(t *testing.T) {
	m := &mgrRec{}
	sink := ManagerAlerts(m)
	sink.Raise(string(CodeModelFailed), "b", nil)
	sink.Raise(string(CodeModelFailed), "a", nil) // a second model: same alert, new details
	sink.Clear(string(CodeModelFailed), "b")      // a is still failing
	sink.Clear(string(CodeModelFailed), "a")
	sink.Clear(string(CodeModelFailed), "a") // nothing active: silent
	sink.Raise(string(CodeCacheModels), "", map[string]any{"models": []string{"x"}})
	sink.Clear(string(CodeCacheModels), "")
	equal(t, m.calls, []string{
		"raise " + string(CodeModelFailed) + " map[models:[b]]",
		"raise " + string(CodeModelFailed) + " map[models:[a b]]",
		"raise " + string(CodeModelFailed) + " map[models:[a]]",
		"clear " + string(CodeModelFailed),
		"raise " + string(CodeCacheModels) + " map[models:[x]]",
		"clear " + string(CodeCacheModels),
	})
}

// TestManagerAlertsOnTheRealManager runs the adapter against the doctor manager and the bus: the
// alert carries the table's severity, title and fix, and the event pair is published.
func TestManagerAlertsOnTheRealManager(t *testing.T) {
	bus := events.New(events.Options{Node: "n"})
	defer bus.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := bus.Subscribe(ctx, events.Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	mgr := doctor.NewManager(doctor.ManagerOptions{Bus: bus})
	sink := ManagerAlerts(mgr)
	next := func() events.Event {
		select {
		case e := <-ch:
			return e
		case <-time.After(2 * time.Second):
			t.Fatal("no event")
		}
		return events.Event{}
	}
	sink.Raise(string(CodeModelFailed), "a", nil)
	e := next()
	if e.Type != events.TypeAlertRaised || !strings.Contains(string(e.Data), `"code":"`+string(CodeModelFailed)+`"`) ||
		!strings.Contains(string(e.Data), `"severity":"error"`) {
		t.Errorf("raised = %s %s", e.Type, e.Data)
	}
	if active := mgr.Active(); len(active) != 1 || active[0].Code != string(CodeModelFailed) {
		t.Errorf("active = %+v", active)
	}
	sink.Clear(string(CodeModelFailed), "a")
	if e = next(); e.Type != events.TypeAlertCleared {
		t.Errorf("cleared = %s %s", e.Type, e.Data)
	}
}

func TestEveryWatcherCodeIsInTheDoctorTable(t *testing.T) {
	want := map[Code]doctor.Severity{
		CodeRouterDown: doctor.Error, CodeKeyRejected: doctor.Critical, CodeModelFailed: doctor.Error,
		CodeCacheModels: doctor.Warning, CodeTooManyLoaded: doctor.Warning,
	}
	for c, sev := range want {
		d, ok := doctor.Lookup(string(c))
		if !ok || !doctor.Emittable(string(c)) || d.Severity != sev {
			t.Errorf("%s: in table %v, emittable %v, severity %v (want %v)", c, ok, doctor.Emittable(string(c)), d.Severity, sev)
		}
	}
}

// --- the worker: goroutines, clock, SSE, key.rotated ---

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not reached in time: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startWorker(t *testing.T, wr *wrig) {
	t.Helper()
	if err := wr.w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := wr.w.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
}

func TestWorkerPollsOnTheClockAndOnKick(t *testing.T) {
	wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil })
	wr.fake.SSEStatus(404) // SSE off: the poll alone must do the work
	startWorker(t, wr)

	eventually(t, "the first poll", func() bool { return wr.fake.Calls("GET", "/models") == 1 })
	eventually(t, "the poll ticker", func() bool { return wr.clock.Tickers() >= 1 })
	wr.fake.Set("model-a-q4km", fake.Loaded)

	// Nothing happens until the clock moves.
	time.Sleep(30 * time.Millisecond)
	if n := wr.fake.Calls("GET", "/models"); n != 1 {
		t.Fatalf("polled %d times without a tick", n)
	}
	wr.clock.Advance(DefaultPollInterval)
	eventually(t, "the tick poll", func() bool { return wr.fake.Calls("GET", "/models") == 2 })
	var seen []string
	eventually(t, "the loaded event", func() bool {
		seen = append(seen, summarize(wr.pub.take())...)
		return len(seen) > 0
	})
	equal(t, seen, []string{"model.loaded model-a-q4km 0"})

	// Kick polls at once.
	wr.w.Kick()
	wr.w.Kick() // coalesced
	eventually(t, "the kick poll", func() bool { return wr.fake.Calls("GET", "/models") >= 3 })
}

func TestWorkerBacksOffWhileTheRouterIsDown(t *testing.T) {
	wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil })
	wr.fake.SSEStatus(404)
	wr.fake.Down(true)
	startWorker(t, wr)
	eventually(t, "the first poll", func() bool { return wr.fake.Calls("GET", "/models") == 1 })
	eventually(t, "the ticker", func() bool { return wr.clock.Tickers() >= 1 })

	// One failure: the next tick is skipped, the one after polls.
	wr.clock.Advance(DefaultPollInterval)
	time.Sleep(40 * time.Millisecond)
	if n := wr.fake.Calls("GET", "/models"); n != 1 {
		t.Fatalf("the tick after one failure must be skipped, %d polls", n)
	}
	wr.clock.Advance(DefaultPollInterval)
	eventually(t, "the backed-off poll", func() bool { return wr.fake.Calls("GET", "/models") == 2 })

	// Recovery resets the cadence: a Kick (or the next due tick) polls and the router answers.
	wr.fake.Down(false)
	wr.w.Kick()
	eventually(t, "recovery", func() bool { return !wr.w.View().Stale })
	wr.clock.Advance(DefaultPollInterval)
	eventually(t, "a poll at the normal cadence", func() bool { return wr.fake.Calls("GET", "/models") >= 4 })
}

func TestWorkerSSENudgesThePoll(t *testing.T) {
	var got []Progress
	var mu sync.Mutex
	wr := newWRig(t, func(o *WatcherOptions) {
		o.PresetIDs = nil
		o.OnProgress = func(p Progress) { mu.Lock(); got = append(got, p); mu.Unlock() }
	})
	startWorker(t, wr)
	eventually(t, "the stream", func() bool { return wr.fake.Streams() == 1 })
	eventually(t, "the first poll", func() bool { return wr.fake.Calls("GET", "/models") >= 1 })
	base := wr.fake.Calls("GET", "/models")

	wr.fake.Set("model-a-q4km", fake.Loading)
	wr.fake.Emit("model-a-q4km", "status_change", map[string]any{
		"status":   "loading",
		"progress": map[string]any{"stages": []string{"text_model"}, "current": "text_model", "value": 0.25},
	})
	eventually(t, "an SSE-triggered poll with no tick", func() bool { return wr.fake.Calls("GET", "/models") > base })
	eventually(t, "OnProgress", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	p, ok := wr.w.LatestProgress("model-a-q4km")
	if !ok || p.Value != 0.25 {
		t.Errorf("LatestProgress = %+v %v", p, ok)
	}
	wr.fake.Emit("model-a-q4km", "status_change", map[string]any{"status": "loaded"})
	eventually(t, "progress forgotten once loaded", func() bool { _, ok := wr.w.LatestProgress("model-a-q4km"); return !ok })
}

func TestWorkerSSEReconnectsWithBackoff(t *testing.T) {
	wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil })
	startWorker(t, wr)
	eventually(t, "the stream", func() bool { return wr.fake.Streams() == 1 })

	wr.fake.DropStreams() // llama restarted
	eventually(t, "the reconnect timer", func() bool { return wr.clock.Tickers() >= 2 })
	if wr.fake.Streams() != 0 {
		t.Fatal("reconnected before the backoff")
	}
	wr.clock.Advance(sseMinBackoff)
	eventually(t, "the stream is back", func() bool { return wr.fake.Streams() == 1 })
}

func TestWorkerSSEUnavailableIsLoggedOnce(t *testing.T) {
	wr := newWRig(t, func(o *WatcherOptions) { o.PresetIDs = nil })
	wr.fake.SSEStatus(404)
	startWorker(t, wr)
	for i := 0; i < 4; i++ {
		eventually(t, "a reconnect attempt", func() bool { return wr.fake.Calls("GET", "/models/sse") >= i+1 })
		eventually(t, "the backoff timer", func() bool { return wr.clock.Tickers() >= 2 })
		wr.clock.Advance(DefaultMaxBackoff)
	}
	if n := strings.Count(wr.logs.String(), "progress stream is not available"); n != 1 {
		t.Errorf("logged %d times, want once per episode", n)
	}
	// The next attempts back off up to the cap, they do not hammer the router.
	if wr.fake.Calls("GET", "/models/sse") > 6 {
		t.Errorf("too many reconnects: %d", wr.fake.Calls("GET", "/models/sse"))
	}
}

// keyClient is a Router that counts KeyRotated calls.
type keyClient struct {
	stubRouter
	mu      sync.Mutex
	rotated int
	polls   int
}

func (k *keyClient) KeyRotated() { k.mu.Lock(); k.rotated++; k.mu.Unlock() }
func (k *keyClient) Progress(ctx context.Context) (<-chan Progress, error) {
	return nil, errors.New("no stream")
}

func (k *keyClient) counts() (int, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.rotated, k.polls
}

func TestWorkerKeyRotatedEventRefreshesAndPolls(t *testing.T) {
	bus := events.New(events.Options{Node: "n"})
	defer bus.Close()
	kc := &keyClient{}
	kc.models = func(context.Context) ([]ModelStatus, error) {
		kc.mu.Lock()
		kc.polls++
		kc.mu.Unlock()
		return []ModelStatus{{ID: "m", Status: StatusUnloaded}}, nil
	}
	w := NewWatcher(WatcherOptions{Client: kc, Publisher: bus, Clock: confake.NewClock(time.Now())})
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Stop(context.Background()) }()
	eventually(t, "the first poll", func() bool { _, p := kc.counts(); return p >= 1 })
	eventually(t, "the subscription", func() bool { return bus.Counters().Subscribers == 1 })

	_, before := kc.counts()
	if err := bus.Publish(events.TypeKeyRotated, "", events.KeyRotated{Fingerprint: "…abcd"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the client was told and a poll ran", func() bool { r, p := kc.counts(); return r == 1 && p > before })
}

func TestWorkerStopIsPrompt(t *testing.T) {
	wr := newWRig(t)
	if err := wr.w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the stream", func() bool { return wr.fake.Streams() == 1 })
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := wr.w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("Stop took %s", time.Since(start))
	}
	eventually(t, "the stream closed", func() bool { return wr.fake.Streams() == 0 })
	// Stop with nothing started is harmless.
	if err := NewWatcher(WatcherOptions{Client: &stubRouter{}}).Stop(context.Background()); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}

func TestWatcherKeyNeverInLogsOrFindings(t *testing.T) {
	wr := newWRig(t)
	wr.fake.Crash("model-a-q4km", 1)
	wr.fake.SetKey(secret(9))
	for i := 0; i < 3; i++ {
		wr.poll(t) // 401s, the 005 finding
	}
	wr.fake.Force(500, `{"error":{"message":"echo `+wr.bearer+`"}}`)
	wr.fake.SetKey(wr.bearer)
	wr.poll(t)
	for _, f := range wr.w.Findings() {
		if fmt.Sprint(f.Details) != "" && strings.Contains(fmt.Sprint(f.Details), wr.bearer) {
			t.Errorf("finding details carry the key: %+v", f)
		}
	}
	if v := wr.w.View(); strings.Contains(v.Err, wr.bearer) {
		t.Errorf("view error carries the key: %q", v.Err)
	}
	wr.noLeak(t)
	if !strings.Contains(wr.logs.String(), "doctor finding raised") {
		t.Error("the scenario logged nothing: the scan proves nothing")
	}
}
