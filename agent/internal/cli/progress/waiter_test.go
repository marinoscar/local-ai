package progress_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/cli/progress"
	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

var (
	ttyOpts   = progress.RenderOptions{TTY: true, UTF8: true}
	linesOpts = progress.RenderOptions{UTF8: true}
)

// readyLine is the final line the loaded step carries in these tests.
var readyLine = &progress.Line{Sev: progress.SevOK, Text: "Qwen 3.5 9B is ready."}

func withReady(spec progress.Spec) progress.Spec {
	inner := spec.OnEvent
	spec.OnEvent = func(ev progress.Event) progress.Step {
		s := inner(ev)
		if s.Done {
			s.Final = readyLine
		}
		return s
	}
	return spec
}

// A load seen from a scripted stream: the TTY transcript is a golden file (acceptance criterion).
func TestLoadProgressTTYGolden(t *testing.T) {
	r := newRig(t, agentevents.Options{}, ttyOpts)
	w, err := progress.Open(context.Background(), withReady(r.loadSpec()), r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)

	r.ev.WaitActive(1)
	publishLoading(r, "op_1", []string{"small-vlm"}, nil)
	r.sawUpdate()
	r.tick(time.Second)
	publishLoading(r, "op_1", nil, f64(0))
	r.sawUpdate()
	r.tick(2 * time.Second)
	publishLoading(r, "op_1", nil, f64(0.7))
	r.sawUpdate()
	r.tick(2 * time.Second)
	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 7100})

	o := r.result(out)
	if o.err != nil {
		t.Fatal(o.err)
	}
	if got, ok := o.res.Value.(agentevents.ModelLoaded); !ok || got.LoadMS != 7100 {
		t.Fatalf("result = %#v", o.res.Value)
	}
	if o.res.Elapsed != 5*time.Second {
		t.Errorf("elapsed = %v, want 5s on the manual clock", o.res.Elapsed)
	}
	golden(t, "load-tty", r.out.String())
}

// Off a terminal: one line per state change, no carriage return, no escape sequence.
func TestLoadProgressNonTTYLines(t *testing.T) {
	tests := []struct {
		name string
		ro   progress.RenderOptions
		want string
	}{
		{"utf8", linesOpts,
			"… Unloading Small VLM to make room\n… Loading Qwen 3.5 9B\n✓ Qwen 3.5 9B is ready.\n"},
		{"ascii", progress.RenderOptions{},
			"... Unloading Small VLM to make room\n... Loading Qwen 3.5 9B\n[ok] Qwen 3.5 9B is ready.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, agentevents.Options{}, tt.ro)
			w, err := progress.Open(context.Background(), withReady(r.loadSpec()), r.options())
			if err != nil {
				t.Fatal(err)
			}
			out := r.run(w)
			r.ev.WaitActive(1)
			publishLoading(r, "op_1", []string{"small-vlm"}, nil)
			r.sawEvent(agentevents.TypeModelLoading)
			r.tick(time.Second)
			publishLoading(r, "op_1", nil, f64(0))
			r.sawEvent(agentevents.TypeModelLoading)
			publishLoading(r, "op_1", nil, f64(0.5)) // same state: no new line
			r.sawEvent(agentevents.TypeModelLoading)
			r.tick(time.Second)
			r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
			r.sawEvent(agentevents.TypeModelLoaded)
			if o := r.result(out); o.err != nil {
				t.Fatal(o.err)
			}
			got := r.out.String()
			if got != tt.want {
				t.Fatalf("output:\n%q\nwant:\n%q", got, tt.want)
			}
			if strings.ContainsAny(got, "\r\x1b") {
				t.Fatalf("a non-terminal transcript must have no control characters: %q", got)
			}
		})
	}
}

// --quiet draws no progress but keeps the final line; --json prints nothing at all.
func TestQuietAndJSONDrawNothing(t *testing.T) {
	tests := []struct {
		name string
		ro   progress.RenderOptions
		want string
	}{
		{"quiet", progress.RenderOptions{UTF8: true, Silent: true}, "✓ Qwen 3.5 9B is ready.\n"},
		{"json", progress.RenderOptions{UTF8: true, Silent: true, JSON: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, agentevents.Options{}, tt.ro)
			w, err := progress.Open(context.Background(), withReady(r.loadSpec()), r.options())
			if err != nil {
				t.Fatal(err)
			}
			out := r.run(w)
			r.ev.WaitActive(1)
			publishLoading(r, "op_1", nil, f64(0.1))
			r.sawEvent(agentevents.TypeModelLoading)
			r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
			r.sawEvent(agentevents.TypeModelLoaded)
			if o := r.result(out); o.err != nil {
				t.Fatal(o.err)
			}
			if got := r.out.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
	}
}

// A rollback ends the wait with the step's error and leaves the line clean for the problem layout.
func TestRollbackEndsWithTheError(t *testing.T) {
	r := newRig(t, agentevents.Options{}, ttyOpts)
	w, err := progress.Open(context.Background(), r.loadSpec(), r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.3))
	r.sawEvent(agentevents.TypeModelLoading)
	r.ev.Publish(agentevents.TypeModelFailed, "op_1", agentevents.ModelFailed{
		Model: "qwen", Code: "LAI-LLM-009", Message: "The model failed to start.", RolledBack: true, Restored: []string{"small-vlm"},
	})
	r.sawEvent(agentevents.TypeModelFailed)

	o := r.result(out)
	var lf *loadFailed
	if !errors.As(o.err, &lf) || !lf.RolledBack || len(lf.Restored) != 1 {
		t.Fatalf("err = %v", o.err)
	}
	if got := r.out.String(); !strings.HasSuffix(got, "\r\x1b[K\x1b[?25h") {
		t.Fatalf("the live line must be cleared and the cursor restored before the error prints: %q", visible(got))
	}
}

// The connection drops after event 1: the client reconnects with Last-Event-ID 1 and the types filter.
func TestReconnectSendsLastEventID(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.2))
	r.sawEvent(agentevents.TypeModelLoading)

	r.ev.Drop()
	r.ev.WaitConns(2)
	r.ev.WaitActive(1)
	conns := r.ev.Connections()
	if conns[0].LastEventID != "" || conns[1].LastEventID != "1" {
		t.Fatalf("Last-Event-ID = %q then %q, want \"\" then \"1\"", conns[0].LastEventID, conns[1].LastEventID)
	}
	for i, c := range conns {
		if c.Query != "types=model.*" {
			t.Errorf("connection %d query = %q", i, c.Query)
		}
	}
	r.until("the re-read after the reconnect", func() bool { return r.rereadCount() >= 1 })

	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 5})
	r.sawEvent(agentevents.TypeModelLoaded)
	o := r.result(out)
	if o.err != nil || o.res.Reconnects != 1 || o.res.LastEventID != "2" {
		t.Fatalf("result = %+v, err %v", o.res, o.err)
	}
	if got := r.out.String(); !strings.Contains(got, "i Lost the connection to the agent.") || !strings.Contains(got, "i Reconnected to the agent.") {
		t.Fatalf("the outage must be shown:\n%s", got)
	}
}

// The client was away for more than the ring holds: the agent answers with a gap resync first, the
// CLI re-reads the real state, and the replay still finishes the wait.
func TestGapResyncRereadsAndReplays(t *testing.T) {
	r := newRig(t, agentevents.Options{Capacity: 3}, linesOpts)
	spec := r.loadSpec()
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.1)) // id 1
	r.sawEvent(agentevents.TypeModelLoading)

	r.ev.Down() // the agent cannot be reached while events pile up
	r.ev.Drop()
	r.until("a failed reconnect", func() bool { return r.agent.Count("/events?types=model.*") >= 2 })
	for i := 0; i < 4; i++ { // ids 2 to 5: the ring (3) no longer holds id 2
		publishLoading(r, "op_1", nil, f64(0.5))
	}
	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 9}) // id 6
	r.setRereads(rereadAnswer{step: progress.Step{Update: &progress.Update{Label: "Loading Qwen 3.5 9B", Percent: 60}}})
	r.ev.Up()
	before := r.rereadCount()
	r.tickUntil("the reconnect", func() bool { return len(r.ev.Connections()) >= 2 })

	r.sawEvent(agentevents.TypeModelLoading) // the replay that follows the resync
	r.sawEvent(agentevents.TypeModelLoading)
	r.sawEvent(agentevents.TypeModelLoaded)
	o := r.result(out)
	if o.err != nil {
		t.Fatal(o.err)
	}
	if got := r.ev.Connections(); got[len(got)-1].LastEventID != "1" {
		t.Fatalf("reconnected with Last-Event-ID %q, want 1", got[len(got)-1].LastEventID)
	}
	if r.rereadCount() <= before {
		t.Fatal("the gap resync must trigger a re-read")
	}
}

// The agent restarts mid-wait and is down for a while: the CLI polls the real state at the reconnect
// cadence and reports what it finds, even though the stream never came back.
func TestRestartWhileDownReportsRealState(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := withReady(r.loadSpec())
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.4))
	r.sawEvent(agentevents.TypeModelLoading)

	r.setRereads(
		rereadAnswer{step: progress.Step{Update: &progress.Update{Label: "Loading Qwen 3.5 9B"}}},
		rereadAnswer{step: progress.Step{Done: true, Result: "loaded after the restart", Final: readyLine}},
	)
	r.ev.Restart(true) // cut, ids restart, 503 until Up
	r.until("the first poll", func() bool { return r.rereadCount() >= 1 })
	select {
	case o := <-out:
		t.Fatalf("finished too early: %+v", o)
	case <-time.After(50 * time.Millisecond):
	}
	r.tickUntil("the second poll", func() bool { return r.rereadCount() >= 2 }) // still down: polls again
	o := r.result(out)
	if o.err != nil || o.res.Value != "loaded after the restart" {
		t.Fatalf("result = %+v, err %v", o.res, o.err)
	}
	if o.res.Reconnects != 0 || o.res.Rereads != 2 {
		t.Errorf("reconnects = %d, rereads = %d", o.res.Reconnects, o.res.Rereads)
	}
	r.noMutations()
	if got := r.out.String(); !strings.Contains(got, "i Lost the connection to the agent.") || !strings.HasSuffix(got, "✓ Qwen 3.5 9B is ready.\n") {
		t.Fatalf("transcript:\n%s", got)
	}
}

// The agent comes back: the first frame is the resync of the restart, the CLI re-reads and goes on.
func TestRestartThenResyncRestart(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	for i := 0; i < 3; i++ {
		publishLoading(r, "op_1", nil, f64(0.1*float64(i+1)))
		r.sawEvent(agentevents.TypeModelLoading)
	}
	r.ev.Restart(true)
	r.until("a failed attempt", func() bool { return r.agent.Count("/events?types=model.*") >= 2 })
	r.ev.Up()
	r.tickUntil("the reconnect", func() bool { return len(r.ev.Connections()) >= 2 })
	r.ev.WaitActive(1)
	conns := r.ev.Connections()
	if got := conns[len(conns)-1].LastEventID; got != "3" {
		t.Fatalf("reconnect Last-Event-ID = %q, want 3 (the old process's id)", got)
	}
	r.until("the re-read", func() bool { return r.rereadCount() >= 1 })

	r.ev.Publish(agentevents.TypeModelLoaded, "op_2", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
	r.sawEvent(agentevents.TypeModelLoaded)
	if o := r.result(out); o.err != nil || o.res.Reconnects != 1 {
		t.Fatalf("result = %+v, err %v", o.res, o.err)
	}
}

// A shutdown event ends the stream: the CLI re-reads at once and reconnects only after the backoff.
func TestShutdownEventRereadsThenBacksOff(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	r.setRereads(rereadAnswer{step: progress.Step{Done: true, Result: "finished before the agent stopped"}})
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.9))
	r.sawEvent(agentevents.TypeModelLoading)
	r.ev.Shutdown()

	o := r.result(out)
	if o.err != nil || o.res.Value != "finished before the agent stopped" {
		t.Fatalf("result = %+v, err %v", o.res, o.err)
	}
	if n := len(r.ev.Connections()); n != 1 {
		t.Errorf("connections = %d: a shutdown must back off before reconnecting", n)
	}
}

// A failing re-read is retried at the backoff cadence; a Fatal one ends the wait unwrapped.
func TestRereadErrors(t *testing.T) {
	boom := errors.New("model not found")
	tests := []struct {
		name    string
		answers []rereadAnswer
		wantErr error
	}{
		{"retried", []rereadAnswer{{err: io.ErrUnexpectedEOF}, {step: progress.Step{Done: true, Result: "ok"}}}, nil},
		{"fatal", []rereadAnswer{{err: progress.Fatal(boom)}}, boom},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, agentevents.Options{}, linesOpts)
			spec := r.loadSpec()
			spec.Reread = r.reread
			w, err := progress.Open(context.Background(), spec, r.options())
			if err != nil {
				t.Fatal(err)
			}
			r.setRereads(tt.answers...)
			out := r.run(w)
			r.ev.WaitActive(1)
			r.ev.Drop() // the reconnect triggers the re-read
			r.until("the first re-read", func() bool { return r.rereadCount() >= 1 })
			if tt.wantErr == nil {
				r.tickUntil("the retry", func() bool { return r.rereadCount() >= 2 })
			}
			o := r.result(out)
			if tt.wantErr == nil {
				if o.err != nil || o.res.Value != "ok" {
					t.Fatalf("result = %+v, err %v", o.res, o.err)
				}
				return
			}
			if o.err != boom || progress.IsFatal(o.err) {
				t.Fatalf("err = %v (fatal wrapper left: %v), want the bare %v", o.err, progress.IsFatal(o.err), boom)
			}
		})
	}
}

// Ctrl-C (a cancelled context, or the Interrupt channel) stops the waiting and sends nothing.
func TestCtrlCStopsWaitingAndSendsNoCancel(t *testing.T) {
	still := "i Still running in the background. Check with `aictl status`.\n"
	stillEs := "i Sigue en ejecución en segundo plano. Revise con `aictl status`.\n"
	tests := []struct {
		name  string
		ro    progress.RenderOptions
		lang  string
		press func(cancel context.CancelFunc, ch chan struct{})
		viaCh bool
		want  string
	}{
		{"cancelled context", linesOpts, "en", func(c context.CancelFunc, _ chan struct{}) { c() }, false, "… Loading Qwen 3.5 9B\n" + still},
		{"signal channel", linesOpts, "en", func(_ context.CancelFunc, ch chan struct{}) { ch <- struct{}{} }, true, "… Loading Qwen 3.5 9B\n" + still},
		{"spanish", linesOpts, "es", func(c context.CancelFunc, _ chan struct{}) { c() }, false, "… Loading Qwen 3.5 9B\n" + stillEs},
		{"quiet still says it", progress.RenderOptions{UTF8: true, Silent: true}, "en", func(c context.CancelFunc, _ chan struct{}) { c() }, false, still},
		{"json prints nothing", progress.RenderOptions{UTF8: true, Silent: true, JSON: true}, "en", func(c context.CancelFunc, _ chan struct{}) { c() }, false, ""},
		{"ascii", progress.RenderOptions{}, "en", func(c context.CancelFunc, _ chan struct{}) { c() }, false, "... Loading Qwen 3.5 9B\n[i] Still running in the background. Check with `aictl status`.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.ro.Lang = tt.lang
			r := newRig(t, agentevents.Options{}, tt.ro)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch := make(chan struct{}, 1)
			w, err := progress.Open(ctx, r.loadSpec(), r.options(func(o *progress.Options) {
				o.Lang = tt.lang
				if tt.viaCh {
					o.Interrupt = ch
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			out := r.run(w)
			r.ev.WaitActive(1)
			publishLoading(r, "op_1", nil, f64(0.3))
			r.sawEvent(agentevents.TypeModelLoading)
			tt.press(cancel, ch)

			o := r.result(out)
			if !errors.Is(o.err, progress.ErrInterrupted) || !errors.Is(o.err, context.Canceled) {
				t.Fatalf("err = %v", o.err)
			}
			if got := r.out.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
			r.noMutations()
		})
	}
}

// On a terminal Ctrl-C clears the line and shows the cursor again before the message.
func TestCtrlCRestoresTheTerminal(t *testing.T) {
	r := newRig(t, agentevents.Options{}, ttyOpts)
	ctx, cancel := context.WithCancel(context.Background())
	w, err := progress.Open(ctx, r.loadSpec(), r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	publishLoading(r, "op_1", nil, f64(0.3))
	r.sawEvent(agentevents.TypeModelLoading)
	cancel()
	if o := r.result(out); !errors.Is(o.err, progress.ErrInterrupted) {
		t.Fatalf("err = %v", o.err)
	}
	got := r.out.String()
	i, j := strings.LastIndex(got, "\x1b[?25h"), strings.Index(got, "i Still running")
	if i < 0 || j < 0 || i > j || !strings.Contains(got[:i], "\r\x1b[K") {
		t.Fatalf("transcript = %q", visible(got))
	}
}

// OnInterrupt can keep waiting (ark sync: "Cancel the sync too?"), and still sees the next press.
func TestOnInterruptHook(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	ch := make(chan struct{}, 4)
	var calls int
	var sawLive bool
	hook := func(ctx context.Context) progress.InterruptAction {
		calls++
		sawLive = ctx.Err() == nil // detached from the cancellation, so the hook may talk to the agent
		if calls == 1 {
			return progress.KeepWaiting
		}
		return progress.Leave
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := progress.Open(ctx, r.loadSpec(), r.options(func(o *progress.Options) {
		o.Interrupt, o.OnInterrupt = ch, hook
	}))
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)

	cancel() // a cancelled context is ignored once the caller owns the signals
	ch <- struct{}{}
	r.until("the first press", func() bool { return len(ch) == 0 })
	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
	// The wait kept going: the loaded event still arrives and finishes it.
	r.sawEvent(agentevents.TypeModelLoaded)
	if o := r.result(out); o.err != nil || calls != 1 || !sawLive {
		t.Fatalf("err = %v, hook calls = %d, live ctx = %v", o.err, calls, sawLive)
	}

	// Second round: the hook says Leave.
	r2 := newRig(t, agentevents.Options{}, linesOpts)
	calls = 0
	ch2 := make(chan struct{}, 4)
	w2, err := progress.Open(context.Background(), r2.loadSpec(), r2.options(func(o *progress.Options) {
		o.Interrupt, o.OnInterrupt = ch2, hook
	}))
	if err != nil {
		t.Fatal(err)
	}
	out2 := r2.run(w2)
	ch2 <- struct{}{}
	ch2 <- struct{}{}
	o := r2.result(out2)
	if !errors.Is(o.err, progress.ErrInterrupted) || calls != 2 {
		t.Fatalf("err = %v, hook calls = %d, want ErrInterrupted after 2", o.err, calls)
	}
	if got := r2.out.String(); strings.Count(got, "Still running") != 1 {
		t.Fatalf("the message must appear once, when the hook finally lets go:\n%s", got)
	}
	r2.noMutations()
}

// The deadline is measured on the clock; expiry is the context's DeadlineExceeded (exit 7).
func TestDeadline(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	w, err := progress.Open(context.Background(), r.loadSpec(), r.options(func(o *progress.Options) { o.Timeout = 30 * time.Second }))
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	r.tick(29 * time.Second)
	select {
	case o := <-out:
		t.Fatalf("ended early: %+v", o)
	default:
	}
	r.clock.Advance(time.Second)
	o := r.result(out)
	if !errors.Is(o.err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", o.err)
	}
	r.noMutations()
}

// A context deadline (the real clock) ends the wait the same way.
func TestContextDeadline(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	w, err := progress.Open(ctx, r.loadSpec(), r.options())
	if err != nil {
		t.Fatal(err)
	}
	o := r.result(r.run(w))
	if !errors.Is(o.err, context.DeadlineExceeded) || errors.Is(o.err, progress.ErrInterrupted) {
		t.Fatalf("err = %v", o.err)
	}
}

// A stream that goes silent (not even a ping for 45 s) is dropped and reopened.
func TestIdleStreamIsReopened(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.Reread = r.reread
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	r.tick(44 * time.Second)
	if n := len(r.ev.Connections()); n != 1 {
		t.Fatalf("connections = %d before the idle limit", n)
	}
	r.tick(time.Second)
	r.ev.WaitConns(2)
	r.until("the re-read", func() bool { return r.rereadCount() >= 1 })
	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
	r.sawEvent(agentevents.TypeModelLoaded)
	if o := r.result(out); o.err != nil || o.res.Reconnects != 1 {
		t.Fatalf("result = %+v, err %v", o.res, o.err)
	}
}

// Events that arrive before the operation id is known are held, then matched against it.
func TestAwaitKeyHoldsEventsUntilCorrelate(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.AwaitKey = true
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	r.ev.WaitActive(1)
	publishLoading(r, "op_other", nil, f64(0.1))
	publishLoading(r, "op_mine", nil, f64(0.2))
	out := r.run(w)
	time.Sleep(50 * time.Millisecond)
	if n := len(r.seen); n != 0 {
		t.Fatalf("%d events handled before the key was known", n)
	}
	w.Correlate("op_mine")
	r.sawEvent(agentevents.TypeModelLoading) // only the matching one
	r.ev.Publish(agentevents.TypeModelLoaded, "op_mine", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
	r.sawEvent(agentevents.TypeModelLoaded)
	if o := r.result(out); o.err != nil {
		t.Fatal(o.err)
	}
	if got := r.out.String(); got != "… Loading Qwen 3.5 9B\n" {
		t.Fatalf("output = %q", got)
	}
}

// Unknown event types and events nobody matches are ignored; the wait is not disturbed.
func TestUnknownAndForeignEventsAreIgnored(t *testing.T) {
	r := newRig(t, agentevents.Options{}, linesOpts)
	spec := r.loadSpec()
	spec.Types = []string{"*"}
	w, err := progress.Open(context.Background(), spec, r.options())
	if err != nil {
		t.Fatal(err)
	}
	out := r.run(w)
	r.ev.WaitActive(1)
	r.ev.Publish(agentevents.TypeKeyRotated, "", agentevents.KeyRotated{Fingerprint: "ab:cd"})
	r.sawEvent(agentevents.TypeKeyRotated)
	r.ev.Publish(agentevents.TypeModelLoaded, "op_1", agentevents.ModelLoaded{Model: "qwen", LoadMS: 1})
	r.sawEvent(agentevents.TypeModelLoaded)
	if o := r.result(out); o.err != nil {
		t.Fatal(o.err)
	}
}

// Open fails at once when the first connection fails; a Fatal marker never leaks to the caller.
func TestOpenFailures(t *testing.T) {
	denied := errors.New("not allowed")
	unreachable := io.ErrUnexpectedEOF
	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"fatal", progress.Fatal(denied), denied},
		{"unreachable", unreachable, unreachable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, agentevents.Options{}, linesOpts)
			_, err := progress.Open(context.Background(), r.loadSpec(), r.options(func(o *progress.Options) {
				o.Open = func(context.Context, events.Query) (io.ReadCloser, error) { return nil, tt.err }
			}))
			if err != tt.want {
				t.Fatalf("err = %v (fatal marker: %v), want the bare %v", err, progress.IsFatal(err), tt.want)
			}
		})
	}
	if _, err := progress.Open(context.Background(), progress.Spec{}, progress.Options{}); err == nil {
		t.Fatal("Open without an OpenFunc must fail")
	}
}

// Close ends a subscription that is never waited on (the mutating request failed).
func TestCloseReleasesTheConnection(t *testing.T) {
	r := newRig(t, agentevents.Options{}, ttyOpts)
	w, err := progress.Open(context.Background(), r.loadSpec(), r.options())
	if err != nil {
		t.Fatal(err)
	}
	r.ev.WaitActive(1)
	w.Close()
	r.ev.WaitActive(0)
}
