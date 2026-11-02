package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Conn is one GET /events connection the fake accepted.
type Conn struct {
	// Query is the raw query string ("types=model.*,mode.changed").
	Query string
	// LastEventID is the Last-Event-ID header ("" when the client sent none).
	LastEventID string
}

// Events is a scripted event stream behind GET /events. It is backed by the agent's real bus
// (internal/events), so replay, the gap and restart resyncs and the type filter behave exactly as
// the agent does, and it writes the frames the way the agent's handler does (agent.md section
// 3.10). A test publishes events and then makes the connection misbehave: Ping, Drop, Shutdown,
// Restart, Down.
type Events struct {
	t testing.TB

	mu     sync.Mutex
	bus    *events.Bus
	opts   events.Options
	conns  []Conn
	active map[*sseConn]struct{}
	down   bool
}

// injected is a frame the test writes on a connection; end closes the connection after it.
type injected struct {
	b   []byte
	end bool
}

type sseConn struct {
	raw  chan injected // frames the test injects (ping, shutdown)
	drop chan struct{} // closed to cut the connection
	once sync.Once
}

func (c *sseConn) cut() { c.once.Do(func() { close(c.drop) }) }

// Events registers GET /events on the fake agent and returns the control handle.
func (a *Agent) Events(t testing.TB, o events.Options) *Events {
	t.Helper()
	if o.Node == "" {
		o.Node = "ai-test"
	}
	e := &Events{t: t, opts: o, bus: events.New(o), active: map[*sseConn]struct{}{}}
	t.Cleanup(func() {
		e.mu.Lock()
		bus := e.bus
		e.mu.Unlock()
		bus.Close()
	})
	a.Handle(http.MethodGet, "/events", e.serve)
	return e
}

// Publish publishes an event on the current bus; the payload must be the registered struct.
func (e *Events) Publish(typ, opID string, data any) {
	e.t.Helper()
	e.mu.Lock()
	bus := e.bus
	e.mu.Unlock()
	if err := bus.Publish(typ, opID, data); err != nil {
		e.t.Fatalf("publish %s: %v", typ, err)
	}
}

// Bus returns the current bus (for counters and for Resync).
func (e *Events) Bus() *events.Bus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bus
}

// Connections returns the connections accepted so far, oldest first.
func (e *Events) Connections() []Conn {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Conn(nil), e.conns...)
}

// Active is the number of connections currently open.
func (e *Events) Active() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.active)
}

// WaitConns waits until n connections have been accepted in total.
func (e *Events) WaitConns(n int) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(e.Connections()) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatalf("connections = %d, want %d", len(e.Connections()), n)
}

// WaitActive waits until exactly n connections are open and subscribed.
func (e *Events) WaitActive(n int) {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.Active() == n && e.Bus().Counters().Subscribers == n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	e.t.Fatalf("active connections = %d, want %d", e.Active(), n)
}

// Ping writes the ": ping" keepalive on every open connection.
func (e *Events) Ping() { e.inject(injected{b: []byte(": ping\n\n")}) }

// Shutdown writes the shutdown frame on every open connection and closes it, as the agent does when
// it stops.
func (e *Events) Shutdown() {
	e.inject(injected{b: []byte("event: shutdown\ndata: {}\n\n"), end: true})
}

// Drop closes every open connection without a word, as a crash or a network loss does.
func (e *Events) Drop() { e.cutAll() }

// Restart simulates an agent restart: every connection is cut, the bus is replaced by a new one
// (ids start again at 1) that begins with the startup resync (reason "restart"). The agent stays
// unreachable until Up when down is true.
func (e *Events) Restart(down bool) {
	e.mu.Lock()
	old := e.bus
	e.bus = events.New(e.opts)
	e.down = down
	nb := e.bus
	e.mu.Unlock()
	old.Close()
	e.cutAll()
	if err := nb.Resync(events.ResyncRestart); err != nil {
		e.t.Fatalf("resync: %v", err)
	}
}

// Down makes the stream refuse connections with 503 "starting" (the agent is restarting); Up undoes it.
func (e *Events) Down() { e.setDown(true) }

// Up lets connections in again.
func (e *Events) Up() { e.setDown(false) }

func (e *Events) setDown(v bool) {
	e.mu.Lock()
	e.down = v
	e.mu.Unlock()
}

func (e *Events) inject(f injected) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for c := range e.active {
		select {
		case c.raw <- f:
		default:
		}
	}
}

func (e *Events) cutAll() {
	e.mu.Lock()
	var cs []*sseConn
	for c := range e.active {
		cs = append(cs, c)
	}
	e.mu.Unlock()
	for _, c := range cs {
		c.cut()
	}
}

func (e *Events) serve(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	down, bus := e.down, e.bus
	e.mu.Unlock()
	if down {
		WriteEnvelope(w, http.StatusServiceUnavailable, api.CodeStarting, "The agent is starting.", "Wait a moment.", nil)
		return
	}
	filter, err := events.ParseFilter(strings.Join(r.URL.Query()["types"], ","))
	if err != nil {
		WriteEnvelope(w, http.StatusBadRequest, api.CodeInvalidRequest, "Bad types.", "", nil)
		return
	}
	last := r.Header.Get("Last-Event-ID")
	lastID, _ := strconv.ParseUint(strings.TrimSpace(last), 10, 64)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	ch, err := bus.Subscribe(ctx, filter, lastID)
	if err != nil {
		WriteEnvelope(w, http.StatusServiceUnavailable, api.CodeStarting, "The agent is stopping.", "", nil)
		return
	}
	c := &sseConn{raw: make(chan injected, 16), drop: make(chan struct{})}
	e.mu.Lock()
	e.conns = append(e.conns, Conn{Query: r.URL.RawQuery, LastEventID: last})
	e.active[c] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.active, c)
		e.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()
	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.drop:
			return
		case f := <-c.raw:
			if !write(f.b) || f.end {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !write(frame(ev)) {
				return
			}
		}
	}
}

// frame renders an event the way the agent's handler does: id (absent on the synthetic resync),
// event and data lines; resync carries only its payload as data.
func frame(ev events.Event) []byte {
	var data []byte
	if ev.Type == events.TypeResync {
		data = ev.Data
	} else {
		data, _ = json.Marshal(ev)
	}
	var b bytes.Buffer
	if ev.ID != 0 {
		b.WriteString("id: " + strconv.FormatUint(ev.ID, 10) + "\n")
	}
	b.WriteString("event: " + ev.Type + "\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}
