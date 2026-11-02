package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/events"
)

// Timing of the event stream (agent.md section 3.10).
const (
	// EventsHeartbeat is the interval of the `: ping` comment that keeps proxies and browsers from
	// closing an idle stream.
	EventsHeartbeat = 15 * time.Second
	// EventsWriteTimeout bounds one write to the client. The route has no deadline (a stream lives
	// as long as the client), so the write deadline is set per write instead: a client that stopped
	// reading is cut off here, the goroutine ends, and shutdown never waits for it.
	EventsWriteTimeout = 10 * time.Second
)

// EventsOptions configures GET /events.
type EventsOptions struct {
	// Bus is the event bus to stream. Required.
	Bus *events.Bus
	// Stopping returns the channel that closes when the server begins to stop. A stream then ends
	// with a shutdown event, so Server.Shutdown can finish. It is a function because the routes are
	// registered before the Server exists: pass func() <-chan struct{} { return srv.Stopping() }.
	// Nil means the stream never hears about the shutdown (tests of the handler alone).
	Stopping func() <-chan struct{}
	// Clock drives the heartbeat; default config.RealClock. Tests use a manual clock.
	Clock config.Clock
	// Heartbeat overrides EventsHeartbeat. Zero means the default.
	Heartbeat time.Duration
	// WriteTimeout overrides EventsWriteTimeout. Zero means the default.
	WriteTimeout time.Duration
	// Logger receives the notice of a dropped slow client. Nil: slog.Default.
	Logger *slog.Logger
}

// EventsRoute returns the GET /events route (agent.md sections 3.8.24 and 3.10): auth op, or portal
// with its types restricted; no handler timeout; a GET, so the starting gate does not hold it back.
func EventsRoute(opts EventsOptions) (Route, error) {
	if opts.Bus == nil {
		return Route{}, errors.New("api: events route needs a bus")
	}
	h := &eventsHandler{
		bus:      opts.Bus,
		stopping: opts.Stopping,
		clock:    opts.Clock,
		beat:     opts.Heartbeat,
		wtimeout: opts.WriteTimeout,
		log:      opts.Logger,
	}
	if h.clock == nil {
		h.clock = config.RealClock{}
	}
	if h.beat <= 0 {
		h.beat = EventsHeartbeat
	}
	if h.wtimeout <= 0 {
		h.wtimeout = EventsWriteTimeout
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	h.log = h.log.With("component", "api")
	return Route{
		Method:  http.MethodGet,
		Pattern: "/events",
		Auth:    []Level{AuthOp, AuthPortal},
		Timeout: 0, // a stream
		Handler: HandlerFunc(h.serve),
	}, nil
}

// RegisterEvents adds GET /events to reg (the seam of section 3.9). AGENT-20 calls it with the
// registry it gives api.New; a nil reg means DefaultRegistry.
func RegisterEvents(reg *Registry, opts EventsOptions) error {
	rt, err := EventsRoute(opts)
	if err != nil {
		return err
	}
	if reg == nil {
		reg = DefaultRegistry
	}
	return reg.Add(rt)
}

type eventsHandler struct {
	bus      *events.Bus
	stopping func() <-chan struct{}
	clock    config.Clock
	beat     time.Duration
	wtimeout time.Duration
	log      *slog.Logger
}

func (h *eventsHandler) stopCh() <-chan struct{} {
	if h.stopping == nil {
		return nil // a nil channel never fires
	}
	return h.stopping()
}

func (h *eventsHandler) serve(w http.ResponseWriter, r *http.Request) error {
	info := InfoFrom(r.Context())

	filter, err := events.ParseFilter(strings.Join(r.URL.Query()["types"], ","))
	if err != nil {
		return NewError(CodeInvalidRequest, http.StatusBadRequest,
			map[string]any{"reason": "invalid_types", "param": "types", "detail": err.Error()})
	}
	if info.Principal.Kind == KindPortal {
		// Anything the Portal asks beyond its set is dropped silently, never refused
		// (survival-portal.md section 3.3).
		filter = filter.Restrict(events.PortalTypes())
	}
	lastID := parseLastEventID(r.Header.Get("Last-Event-ID"))

	stopping := h.stopCh()
	select {
	case <-stopping:
		return NewError(CodeStarting, http.StatusServiceUnavailable, nil)
	default:
	}

	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return nil
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel() // ends the subscription goroutine whichever way this function returns
	ch, err := h.bus.Subscribe(ctx, filter, lastID)
	if err != nil {
		if errors.Is(err, events.ErrClosed) {
			return NewError(CodeStarting, http.StatusServiceUnavailable, nil)
		}
		return err
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // no server-wide write timeout for a stream (ErrNotSupported on a recorder: fine)
	w.WriteHeader(http.StatusOK)
	if err := h.flush(rc); err != nil {
		return nil // the client is already gone
	}

	tick := h.clock.NewTicker(h.beat)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil // client disconnected
		case <-stopping:
			h.write(w, rc, shutdownFrame)
			return nil
		case ev, ok := <-ch:
			if !ok {
				if ctx.Err() == nil {
					select {
					case <-stopping:
						h.write(w, rc, shutdownFrame)
					default:
						// Dropped for falling behind (or the bus closed): end the stream; the client
						// reconnects with Last-Event-ID and gets a replay or a resync.
						h.log.Warn("event stream closed: client too slow or bus closed", "req_id", info.ID)
					}
				}
				return nil
			}
			f, err := frameOf(ev)
			if err != nil {
				h.log.Error("event not encodable, skipped", "type", ev.Type, "id", ev.ID, "error", err.Error())
				continue
			}
			if !h.write(w, rc, f) {
				return nil
			}
		case <-tick.C():
			if !h.write(w, rc, pingFrame) {
				return nil
			}
		}
	}
}

var (
	pingFrame     = []byte(": ping\n\n")
	shutdownFrame = []byte("event: shutdown\ndata: {}\n\n")
)

// write sends one frame and flushes it, under a fresh write deadline. It reports false when the
// connection is gone or stalled.
func (h *eventsHandler) write(w http.ResponseWriter, rc *http.ResponseController, frame []byte) bool {
	_ = rc.SetWriteDeadline(time.Now().Add(h.wtimeout))
	if _, err := w.Write(frame); err != nil {
		return false
	}
	return h.flush(rc) == nil
}

func (h *eventsHandler) flush(rc *http.ResponseController) error {
	err := rc.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil // a writer that cannot flush still got the bytes
	}
	return err
}

// frameOf renders an event as an SSE frame (agent.md section 3.10): id, event and data lines and a
// blank line. A regular event carries the whole envelope as data; resync carries only its payload
// ({"reason": ...}), and has no id line when it is the synthetic one of a replay, so it does not move
// the client's Last-Event-ID.
func frameOf(ev events.Event) ([]byte, error) {
	var data []byte
	if ev.Type == events.TypeResync {
		data = ev.Data
	} else {
		var err error
		if data, err = json.Marshal(ev); err != nil {
			return nil, err
		}
	}
	var b bytes.Buffer
	if ev.ID != 0 {
		b.WriteString("id: ")
		b.WriteString(strconv.FormatUint(ev.ID, 10))
		b.WriteByte('\n')
	}
	b.WriteString("event: ")
	b.WriteString(ev.Type)
	b.WriteString("\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes(), nil
}

// parseLastEventID reads the Last-Event-ID header: a non-negative decimal integer. Anything else
// (absent, empty, garbage, negative, too large) means "no replay": a malformed header never
// makes the stream fail.
func parseLastEventID(v string) uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
