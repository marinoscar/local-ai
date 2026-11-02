package progress_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/cli/fake"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	cfake "github.com/marinoscar/local-ai/agent/internal/config/fake"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var epoch = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

// syncBuf is a bytes.Buffer the renderer and the test may touch from two goroutines.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// tickRenderer reports every Tick, so a test knows the waiting goroutine handled a clock advance.
type tickRenderer struct {
	*progress.TextRenderer
	ticks   chan struct{}
	updates chan struct{}
}

// Update reports every update once it is drawn: OnEvent returns before the Waiter renders its step,
// so a test that advances the manual clock must wait for this, not for the event.
func (r *tickRenderer) Update(u progress.Update) {
	r.TextRenderer.Update(u)
	select {
	case r.updates <- struct{}{}:
	default:
	}
}

func (r *tickRenderer) Tick() {
	r.TextRenderer.Tick()
	select {
	case r.ticks <- struct{}{}:
	default:
	}
}

// rig is a fake agent with a scripted event stream, a manual clock and a renderer.
type rig struct {
	t     *testing.T
	agent *fake.Agent
	ev    *fake.Events
	clock *cfake.Clock
	out   *syncBuf
	rend  *tickRenderer
	seen  chan string // event types OnEvent has handled

	mu      sync.Mutex
	rereads []rereadAnswer
	nreread int
}

type rereadAnswer struct {
	step progress.Step
	err  error
}

func newRig(t *testing.T, busOpts agentevents.Options, ro progress.RenderOptions) *rig {
	t.Helper()
	r := &rig{t: t, clock: cfake.NewClock(epoch), out: &syncBuf{}, seen: make(chan string, 256)}
	r.agent = fake.New(t, fake.Options{NoSocket: true})
	r.ev = r.agent.Events(t, busOpts)
	ro.Out, ro.Clock = r.out, r.clock
	r.rend = &tickRenderer{TextRenderer: progress.NewRenderer(ro), ticks: make(chan struct{}, 1), updates: make(chan struct{}, 64)}
	return r
}

// open is the events.OpenFunc over the fake agent's TCP listener; a 503 and a refused connection
// are retryable, any other answer is final.
func (r *rig) open(ctx context.Context, q events.Query) (io.ReadCloser, error) {
	u := r.agent.URL + api.BasePath + "/events"
	if len(q.Types) > 0 {
		u += "?types=" + strings.Join(q.Types, ",")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if q.LastEventID != "" {
		req.Header.Set("Last-Event-ID", q.LastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return resp.Body, nil
	case resp.StatusCode == http.StatusServiceUnavailable:
		resp.Body.Close()
		return nil, io.ErrUnexpectedEOF
	}
	resp.Body.Close()
	return nil, progress.Fatal(io.ErrClosedPipe)
}

func (r *rig) options(mod ...func(*progress.Options)) progress.Options {
	o := progress.Options{Open: r.open, Renderer: r.rend, Clock: r.clock, Lang: "en"}
	for _, m := range mod {
		m(&o)
	}
	return o
}

// loadSpec follows a model load of "qwen" the way `aictl load` will: loading updates, loaded ends,
// failed ends with an error, a rollback is called out.
func (r *rig) loadSpec() progress.Spec {
	return progress.Spec{
		Types: []string{"model.*"},
		Match: func(ev progress.Event, key string) bool {
			if key == "" {
				return true
			}
			return ev.OpID == key || ev.Type == agentevents.TypeModelLoaded || ev.Type == agentevents.TypeModelFailed
		},
		OnEvent: func(ev progress.Event) progress.Step {
			defer func() { r.seen <- ev.Type }()
			switch ev.Type {
			case agentevents.TypeModelLoading:
				var d agentevents.ModelLoading
				_ = json.Unmarshal(ev.Data, &d)
				label := "Loading Qwen 3.5 9B"
				if len(d.Unloading) > 0 && d.Progress == nil {
					return progress.Step{Update: &progress.Update{Label: "Unloading Small VLM to make room", Percent: progress.NoBar}}
				}
				pct := float64(progress.NoBar)
				if d.Progress != nil {
					pct = *d.Progress * 100
				}
				return progress.Step{Update: &progress.Update{Label: label, Percent: pct}}
			case agentevents.TypeModelLoaded:
				var d agentevents.ModelLoaded
				_ = json.Unmarshal(ev.Data, &d)
				return progress.Step{Done: true, Result: d}
			case agentevents.TypeModelFailed:
				var d agentevents.ModelFailed
				_ = json.Unmarshal(ev.Data, &d)
				return progress.Step{Err: &loadFailed{d}}
			}
			return progress.Step{}
		},
	}
}

type loadFailed struct{ agentevents.ModelFailed }

func (e *loadFailed) Error() string { return e.Code + ": " + e.Message }

// reread is a Spec.Reread that answers from the queue and counts its calls.
func (r *rig) reread(ctx context.Context) (progress.Step, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nreread++
	if len(r.rereads) == 0 {
		return progress.Step{}, nil
	}
	a := r.rereads[0]
	if len(r.rereads) > 1 {
		r.rereads = r.rereads[1:]
	}
	return a.step, a.err
}

func (r *rig) setRereads(a ...rereadAnswer) {
	r.mu.Lock()
	r.rereads = a
	r.mu.Unlock()
}

func (r *rig) rereadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nreread
}

type outcome struct {
	res progress.Result
	err error
}

// run starts Wait on its own goroutine and waits for its ticker, so clock advances are seen.
func (r *rig) run(w *progress.Wait) <-chan outcome {
	r.t.Helper()
	ch := make(chan outcome, 1)
	go func() {
		res, err := w.Wait()
		ch <- outcome{res, err}
	}()
	r.until("the Waiter's ticker", func() bool { return r.clock.Tickers() >= 1 })
	return ch
}

// tick advances the clock and waits until the Waiter handled the tick.
func (r *rig) tick(d time.Duration) {
	r.t.Helper()
	select {
	case <-r.rend.ticks:
	default:
	}
	r.clock.Advance(d)
	select {
	case <-r.rend.ticks:
	case <-time.After(5 * time.Second):
		r.t.Fatal("the Waiter did not handle the clock tick")
	}
}

// tickUntil advances the clock a second at a time until cond holds. A fixed advance can race with the
// Waiter still booking its next attempt, so tests that wait for a retry loop on this instead.
func (r *rig) tickUntil(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		// The Waiter may have finished (and stopped ticking) between the check and the tick.
		select {
		case <-r.rend.ticks:
		default:
		}
		r.clock.Advance(time.Second)
		select {
		case <-r.rend.ticks:
		case <-time.After(100 * time.Millisecond):
		}
	}
	r.t.Fatalf("timed out waiting for %s", what)
}

// until polls cond for a few seconds of real time.
func (r *rig) until(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s", what)
}

// sawEvent waits for OnEvent to have handled one event of typ.
func (r *rig) sawEvent(typ string) {
	r.t.Helper()
	select {
	case got := <-r.seen:
		if got != typ {
			r.t.Fatalf("handled %q, want %q", got, typ)
		}
	case <-time.After(5 * time.Second):
		r.t.Fatalf("timed out waiting for event %q to be handled", typ)
	}
}

// sawUpdate waits until the Waiter has drawn one more update.
func (r *rig) sawUpdate() {
	r.t.Helper()
	select {
	case <-r.rend.updates:
	case <-time.After(5 * time.Second):
		r.t.Fatal("timed out waiting for an update to be drawn")
	}
}

func (r *rig) result(ch <-chan outcome) outcome {
	r.t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		r.t.Fatal("Wait did not return")
		return outcome{}
	}
}

// noMutations fails if the fake agent saw anything but reads: Ctrl-C must never send a cancel.
func (r *rig) noMutations() {
	r.t.Helper()
	for _, q := range r.agent.Requests() {
		if q.Method != http.MethodGet {
			r.t.Errorf("the agent saw %s %s: a wait must send no mutation", q.Method, q.Path)
		}
	}
}

func publishLoading(r *rig, op string, unloading []string, progressValue *float64) {
	r.ev.Publish(agentevents.TypeModelLoading, op, agentevents.ModelLoading{Model: "qwen", Unloading: unloading, OpID: op, Progress: progressValue})
}

func f64(v float64) *float64 { return &v }

// visible makes the control characters of a terminal transcript readable in a golden file.
func visible(s string) string {
	s = strings.ReplaceAll(s, "\x1b", `\e`)
	return strings.ReplaceAll(s, "\r", `\r`)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	got = visible(got)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from %s:\n--- got\n%s\n--- want\n%s", name, path, got, want)
	}
}
