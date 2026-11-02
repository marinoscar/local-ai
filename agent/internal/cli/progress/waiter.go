// Package progress waits for a long agent operation by following the server-sent events of
// GET /events, shows what is happening, and gives the caller the real final state (aictl.md section
// 3.3 "Progress", section 5 "Failure handling", AICTL-03).
//
// The rules it keeps, in the order they matter:
//
//   - Ctrl-C stops the waiting and nothing else. The Waiter never sends a cancel or a DELETE; the
//     operation keeps running in the agent. A caller that wants a different answer (ark sync asks
//     "Cancel the sync too?") supplies Options.OnInterrupt.
//   - Events are never the source of truth. After a resync, a shutdown, a lost connection or a
//     reconnect the Waiter calls Spec.Reread and acts on the real state, and while the stream is
//     down it polls Reread at the reconnect cadence, so an operation that ended while the agent
//     was restarting is still reported.
//   - The subscription is opened before the caller's mutating request (Open), so no event is
//     missed; events that arrive before the operation id is known are held until Correlate.
//
// The package is independent of the command framework: it takes an events.OpenFunc and returns
// plain errors; internal/cli binds both (Call.Follow).
package progress

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cli/events"
	"github.com/marinoscar/local-ai/agent/internal/config"
)

// Event is one agent event (the envelope of agent.md section 3.10).
type Event = events.Event

// Step is what a Spec says about one event (or about a re-read): show something, finish, or fail.
// The zero Step means "nothing to do".
type Step struct {
	// Update, when set, replaces what the live line shows.
	Update *Update
	// Done ends the wait successfully; Result is handed back to the caller (a decoded document,
	// whatever the command needs to print its final lines).
	Done   bool
	Result any
	// Final is optional: the line that replaces the live line when the wait ends (Done or Err).
	// Commands usually leave it nil and print their own result.
	Final *Line
	// Err ends the wait with this error (a rollback, a failed sync); the live line is cleared
	// first, so the error's own layout starts on a clean line.
	Err error
}

// Spec describes what to follow.
type Spec struct {
	// Types are the ?types= patterns of the subscription.
	Types []string
	// AwaitKey holds events back until Wait.Correlate delivers the key (the op_id or run_id from
	// the response of the mutating request). Without it every event goes straight to Match.
	AwaitKey bool
	// Match selects the events that belong to this wait; key is what Correlate gave ("" when
	// AwaitKey is false). Nil accepts every event the types let through.
	Match func(ev Event, key string) bool
	// OnEvent maps a matching event to a Step. It runs on the waiting goroutine.
	OnEvent func(ev Event) Step
	// Reread reads the real state of the resource ("GET /models/{id}", "GET /mode", ...): after a
	// resync, a shutdown, a dropped connection and every reconnect, and while the stream is down.
	// Nil disables the re-read (the caller then trusts events only). An error from it is retried
	// at the reconnect cadence unless it is wrapped by Fatal.
	Reread func(ctx context.Context) (Step, error)
	// Initial is shown at once, before any event.
	Initial *Update
}

// Options are the environment of a wait.
type Options struct {
	// Open opens GET /events. Required.
	Open events.OpenFunc
	// Renderer draws progress; nil draws nothing.
	Renderer Renderer
	// Clock drives elapsed time, the redraw tick, the idle check and the reconnect backoff; default
	// the real clock.
	Clock config.Clock
	// Timeout is the overall deadline measured on Clock (0: none besides the context's own).
	Timeout time.Duration
	// Lang is "en" or "es", for the notices the Waiter prints.
	Lang string

	// Interrupt, when set, is the source of Ctrl-C: one receive per press. When it is nil a context
	// cancelled for any reason but its deadline counts as the press. When it is set the caller took
	// over the signals and a cancelled context is ignored.
	Interrupt <-chan struct{}
	// OnInterrupt, when set, is asked what to do on Ctrl-C instead of leaving at once (ark sync
	// asks "Cancel the sync too? [y/N]"). The live line is cleared first. ctx is detached from
	// the cancellation but keeps the deadline, so the hook can still talk to the agent. KeepWaiting
	// carries on; Leave ends the wait as an ordinary Ctrl-C.
	OnInterrupt func(ctx context.Context) InterruptAction

	// Backoff is the wait between reconnect attempts (default 1 s, aictl.md section 5).
	Backoff time.Duration
	// IdleTimeout drops a stream that has been silent this long, pings included (default 45 s, three
	// of the agent's 15 s keepalives), and reconnects.
	IdleTimeout time.Duration
	// TickEvery is the redraw and housekeeping interval (default RedrawInterval).
	TickEvery time.Duration
}

// InterruptAction is the answer of Options.OnInterrupt.
type InterruptAction int

// Answers of Options.OnInterrupt.
const (
	// Leave stops waiting: "Still running in the background", exit 130.
	Leave InterruptAction = iota
	// KeepWaiting carries on as if nothing had been pressed.
	KeepWaiting
)

// Result describes how a successful wait went.
type Result struct {
	// Value is Step.Result of the step that finished the wait.
	Value any
	// Elapsed is the time from Open to the end, on Options.Clock.
	Elapsed time.Duration
	// LastEventID is the last "id:" seen, "" when none was.
	LastEventID string
	// Reconnects counts the times the stream was opened again; Rereads the calls to Spec.Reread.
	Reconnects int
	Rereads    int
}

// ErrInterrupted ends a wait after Ctrl-C. It wraps context.Canceled, so ExitFor maps it to 130.
// The Renderer has already printed the "Still running in the background" line.
var ErrInterrupted = fmt.Errorf("progress: stopped waiting: %w", context.Canceled)

// Fatal marks err as not worth retrying: a rejected subscription (403), a resource that does not
// exist (404). The Waiter returns it at once instead of reconnecting.
func Fatal(err error) error {
	if err == nil {
		return nil
	}
	return &fatalError{err}
}

type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// unfatal returns the error a Fatal wrapper carries, and any other error as it is.
func unfatal(err error) error {
	var f *fatalError
	if errors.As(err, &f) {
		return f.err
	}
	return err
}

// IsFatal reports whether err was marked with Fatal.
func IsFatal(err error) bool {
	var f *fatalError
	return errors.As(err, &f)
}

// connItem is what a connection's reader hands over.
type connItem struct {
	it  events.Item
	err error
}

type conn struct {
	s      *events.Stream
	items  chan connItem
	cancel context.CancelFunc
}

const itemBuffer = 1024

// startConn reads s in the background so the agent never sees a slow client, whatever the caller does
// between Open and Wait.
func startConn(parent context.Context, s *events.Stream, cancel context.CancelFunc) *conn {
	c := &conn{s: s, items: make(chan connItem, itemBuffer), cancel: cancel}
	go func() {
		for {
			it, err := s.Next()
			select {
			case c.items <- connItem{it, err}:
			case <-parent.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return c
}

func (c *conn) close() {
	c.cancel()
	c.s.Close()
}

// Wait is a subscription in progress. Create it with Open, call Correlate once the key is known,
// then Wait. It is not safe for concurrent Waits.
type Wait struct {
	spec Spec
	o    Options
	ctx  context.Context // the caller's: Ctrl-C and its own deadline

	netCtx    context.Context // detached from Ctrl-C, keeps the deadline
	cancelNet context.CancelFunc

	start    time.Time
	deadline time.Time

	cur    *conn
	lastID string

	keyCh  chan string
	key    string
	keySet bool
	held   []Event

	res Result
}

// Open subscribes (first connection, synchronously, so an unreachable agent or a rejected request
// fails here) and starts reading in the background. Close the Wait if Wait is never called.
func Open(ctx context.Context, spec Spec, o Options) (*Wait, error) {
	if o.Open == nil {
		return nil, errors.New("progress: Options.Open is required")
	}
	if o.Clock == nil {
		o.Clock = config.RealClock{}
	}
	if o.Renderer == nil {
		o.Renderer = nopRenderer{}
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 45 * time.Second
	}
	if o.TickEvery <= 0 {
		o.TickEvery = RedrawInterval
	}
	w := &Wait{spec: spec, o: o, ctx: ctx, keyCh: make(chan string, 1)}
	w.start = o.Clock.Now()

	// The network context ignores Ctrl-C (it is handled by Wait, which then decides) but keeps the
	// caller's deadline and values.
	base := context.WithoutCancel(ctx)
	if dl, ok := ctx.Deadline(); ok {
		w.deadline = dl
		w.netCtx, w.cancelNet = context.WithDeadline(base, dl)
	} else {
		w.netCtx, w.cancelNet = context.WithCancel(base)
	}
	if o.Timeout > 0 {
		if d := w.start.Add(o.Timeout); w.deadline.IsZero() || d.Before(w.deadline) {
			w.deadline = d
		}
	}
	if !spec.AwaitKey {
		w.keySet = true
	}
	if err := w.connect(); err != nil {
		w.cancelNet()
		return nil, unfatal(err)
	}
	return w, nil
}

// connect opens the stream with the last id seen.
func (w *Wait) connect() error {
	cctx, cancel := context.WithCancel(w.netCtx)
	s, err := events.Open(cctx, w.o.Open, events.Query{Types: w.spec.Types, LastEventID: w.lastID})
	if err != nil {
		cancel()
		return err
	}
	w.cur = startConn(cctx, s, cancel)
	return nil
}

// Correlate tells the Wait which operation it follows (the op_id or run_id of the response). It may
// be called from another goroutine, before or while Wait runs; events held back since Open are then
// matched against it.
func (w *Wait) Correlate(key string) {
	select {
	case w.keyCh <- key:
	default:
		// A second key replaces an unread first one.
		select {
		case <-w.keyCh:
		default:
		}
		select {
		case w.keyCh <- key:
		default:
		}
	}
}

// Close ends the subscription without waiting (the mutating request failed, say).
func (w *Wait) Close() {
	if w.cur != nil {
		w.cur.close()
		w.cur = nil
	}
	w.cancelNet()
	w.o.Renderer.Clear()
}

// Run is Open followed by Wait, for waits that follow an operation already running.
func Run(ctx context.Context, spec Spec, o Options) (Result, error) {
	w, err := Open(ctx, spec, o)
	if err != nil {
		return Result{}, err
	}
	return w.Wait()
}

// async results of the goroutines that talk to the agent without blocking the loop.
type dialResult struct {
	conn *conn
	err  error
}

type rereadResult struct {
	step Step
	err  error
}

// Wait follows the operation until it finishes, fails, is interrupted or times out. It returns the
// context's DeadlineExceeded on timeout (exit 7), ErrInterrupted on Ctrl-C (exit 130), the error of
// a failing Step as it is, and Fatal errors unwrapped from nothing: they are returned as given.
func (w *Wait) Wait() (res Result, err error) {
	r := w.o.Renderer
	defer func() {
		if w.cur != nil {
			w.cur.close()
			w.cur = nil
		}
		w.cancelNet()
		r.Clear()
		w.res.Elapsed = w.o.Clock.Now().Sub(w.start)
		w.res.LastEventID = w.lastID
		if err == nil {
			res = w.res
		}
		err = unfatal(err)
	}()

	tick := w.o.Clock.NewTicker(w.o.TickEvery)
	defer tick.Stop()
	if w.spec.Initial != nil {
		r.Update(*w.spec.Initial)
	}

	var (
		now         = w.o.Clock.Now()
		lastFrame   = now
		parentDone  = w.ctx.Done()
		interrupts  = w.o.Interrupt
		dialing     bool
		dialCh      = make(chan dialResult, 1)
		rereading   bool
		rereadCh    = make(chan rereadResult, 1)
		needReread  bool
		nextAttempt time.Time
		nextReread  time.Time
		outage      bool // the "lost connection" notice is on screen
	)
	if w.o.Interrupt != nil {
		parentDone = nil
	}

	lose := func(delay time.Duration, quiet bool) {
		if w.cur != nil {
			w.cur.close()
			w.cur = nil
		}
		nextAttempt = w.o.Clock.Now().Add(delay)
		needReread = true
		if !outage && !quiet {
			outage = true
			r.Notice(Line{Sev: SevInfo, Text: text(w.o.Lang, msgLost)})
		}
	}

	// apply acts on a Step. It reports whether the wait is over and why.
	apply := func(s Step) (bool, error) {
		if s.Update != nil {
			r.Update(*s.Update)
		}
		switch {
		case s.Err != nil:
			r.Finish(s.Final)
			return true, s.Err
		case s.Done:
			w.res.Value = s.Result
			r.Finish(s.Final)
			return true, nil
		}
		return false, nil
	}

	dispatch := func(ev Event) (bool, error) {
		if w.spec.OnEvent == nil {
			return false, nil
		}
		if !w.keySet {
			if len(w.held) >= itemBuffer {
				w.held = w.held[1:]
			}
			w.held = append(w.held, ev)
			return false, nil
		}
		if w.spec.Match != nil && !w.spec.Match(ev, w.key) {
			return false, nil
		}
		return apply(w.spec.OnEvent(ev))
	}

	for {
		// Housekeeping that depends on the state, not on one channel: start a dial or a re-read.
		now = w.o.Clock.Now()
		if w.cur == nil && !dialing && !now.Before(nextAttempt) {
			dialing = true
			go func() {
				cctx, cancel := context.WithCancel(w.netCtx)
				s, derr := events.Open(cctx, w.o.Open, events.Query{Types: w.spec.Types, LastEventID: w.lastID})
				if derr != nil {
					cancel()
					dialCh <- dialResult{err: derr}
					return
				}
				dialCh <- dialResult{conn: startConn(cctx, s, cancel)}
			}()
		}
		if needReread && !rereading && w.spec.Reread != nil && !now.Before(nextReread) && (w.cur != nil || !dialing) {
			needReread, rereading = false, true
			w.res.Rereads++
			go func() {
				s, rerr := w.spec.Reread(w.netCtx)
				rereadCh <- rereadResult{s, rerr}
			}()
		}

		var items <-chan connItem
		if w.cur != nil {
			items = w.cur.items
		}

		select {
		case ci := <-items:
			if ci.err != nil {
				if w.netCtx.Err() != nil {
					continue
				}
				lose(0, false)
				continue
			}
			lastFrame = w.o.Clock.Now()
			if ci.it.Ping {
				continue
			}
			if ci.it.HasID {
				w.lastID = ci.it.ID
			}
			switch ci.it.Event.Type {
			case events.TypeShutdown:
				// The agent is stopping: give it a moment before the first reconnect.
				lose(w.o.Backoff, false)
			case events.TypeResync:
				needReread = true
			default:
				if done, derr := dispatch(ci.it.Event); done {
					return res, derr
				}
			}

		case d := <-dialCh:
			dialing = false
			if d.err != nil {
				if w.netCtx.Err() != nil {
					continue
				}
				if IsFatal(d.err) {
					r.Finish(nil)
					return res, d.err
				}
				// The stream is not back: poll the real state, once per attempt, so an operation
				// that ended while the agent restarted is still reported.
				nextAttempt = w.o.Clock.Now().Add(w.o.Backoff)
				needReread = true
				continue
			}
			w.cur = d.conn
			w.res.Reconnects++
			lastFrame = w.o.Clock.Now()
			needReread, nextReread = true, time.Time{}
			if outage {
				outage = false
				r.Notice(Line{Sev: SevInfo, Text: text(w.o.Lang, msgBack)})
			}

		case rr := <-rereadCh:
			rereading = false
			if rr.err != nil {
				if w.netCtx.Err() != nil {
					continue
				}
				if IsFatal(rr.err) {
					r.Finish(nil)
					return res, rr.err
				}
				needReread, nextReread = true, w.o.Clock.Now().Add(w.o.Backoff)
				continue
			}
			if done, derr := apply(rr.step); done {
				return res, derr
			}

		case k := <-w.keyCh:
			w.key, w.keySet = k, true
			held := w.held
			w.held = nil
			for _, ev := range held {
				if done, derr := dispatch(ev); done {
					return res, derr
				}
			}

		case <-tick.C():
			now = w.o.Clock.Now()
			if !w.deadline.IsZero() && !now.Before(w.deadline) {
				r.Finish(nil)
				return res, context.DeadlineExceeded
			}
			if w.cur != nil && now.Sub(lastFrame) >= w.o.IdleTimeout {
				lose(0, false) // silent too long: the connection is half open
			}
			r.Tick()

		case <-w.netCtx.Done():
			if errors.Is(w.netCtx.Err(), context.DeadlineExceeded) {
				r.Finish(nil)
				return res, context.DeadlineExceeded
			}
			return res, w.netCtx.Err()

		case <-interrupts:
			if stop := w.interrupted(); stop {
				return res, ErrInterrupted
			}

		case <-parentDone:
			if errors.Is(w.ctx.Err(), context.DeadlineExceeded) {
				r.Finish(nil)
				return res, context.DeadlineExceeded
			}
			parentDone = nil // one press: the hook may keep waiting, and later presses need the channel
			if stop := w.interrupted(); stop {
				return res, ErrInterrupted
			}
		}
	}
}

// interrupted handles one Ctrl-C and reports whether the wait ends.
func (w *Wait) interrupted() bool {
	r := w.o.Renderer
	if w.o.OnInterrupt != nil {
		r.Clear()
		if w.o.OnInterrupt(w.netCtx) == KeepWaiting {
			return false
		}
	}
	r.Interrupted()
	return true
}

// nopRenderer draws nothing.
type nopRenderer struct{}

func (nopRenderer) Update(Update) {}
func (nopRenderer) Notice(Line)   {}
func (nopRenderer) Tick()         {}
func (nopRenderer) Clear()        {}
func (nopRenderer) Finish(*Line)  {}
func (nopRenderer) Interrupted()  {}
