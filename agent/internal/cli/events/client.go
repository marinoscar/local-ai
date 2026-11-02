package events

import (
	"context"
	"encoding/json"
	"io"

	agentevents "github.com/marinoscar/local-ai/agent/internal/events"
)

// Event is the envelope of one agent event (agent.md section 3.10): id, type, at, node, op_id and
// the typed payload as raw JSON. It is the type the agent's bus publishes, so a command decodes
// Data into the payload structs of internal/events (ModelLoaded, SyncDone, ...) and a rename there
// breaks the build here instead of silently at run time.
type Event = agentevents.Event

// Event types the client itself acts on.
const (
	TypeResync   = agentevents.TypeResync
	TypeShutdown = agentevents.TypeShutdown
)

// Query is what a subscription asks of GET /events.
type Query struct {
	// Types are the patterns of ?types= (an exact type or a prefix ending in "*"); empty means all.
	Types []string
	// LastEventID, when set, goes out as the Last-Event-ID header: the agent replays what the
	// client missed, or answers with a resync when it cannot.
	LastEventID string
}

// OpenFunc opens GET /events for q and returns the live body. A non-2xx answer is an error. The
// body must end when ctx is cancelled.
type OpenFunc func(ctx context.Context, q Query) (io.ReadCloser, error)

// Item is one thing read from the stream: a keepalive ping or an event.
type Item struct {
	// Ping is true for a comment frame (the 15 s keepalive); Event is then zero.
	Ping bool
	// ID and HasID are the frame's "id:" line (the value to send as Last-Event-ID).
	ID    string
	HasID bool
	// Event is the decoded event. An unknown type is passed on untouched: deciding what to do with
	// it is the caller's job, and the usual answer is to ignore it.
	Event Event
}

// Stream is an open event stream.
type Stream struct {
	rc io.ReadCloser
	p  *Parser
}

// Open subscribes with q through open.
func Open(ctx context.Context, open OpenFunc, q Query) (*Stream, error) {
	rc, err := open(ctx, q)
	if err != nil {
		return nil, err
	}
	return NewStream(rc), nil
}

// NewStream wraps an already open body (tests, and callers that opened it themselves).
func NewStream(rc io.ReadCloser) *Stream { return &Stream{rc: rc, p: NewParser(rc)} }

// Next returns the next item; io.EOF means the agent closed the stream.
func (s *Stream) Next() (Item, error) {
	f, err := s.p.Next()
	if err != nil {
		return Item{}, err
	}
	if f.Comment {
		return Item{Ping: true}, nil
	}
	return Item{ID: f.ID, HasID: f.HasID, Event: decode(f)}, nil
}

// Close ends the stream.
func (s *Stream) Close() error { return s.rc.Close() }

// decode turns a frame into an Event. A regular event carries the whole envelope as data; resync
// and shutdown carry only their payload, so the type then comes from the "event:" line. Data that
// is not JSON leaves Data empty rather than failing the stream.
func decode(f Frame) Event {
	var ev Event
	if json.Unmarshal([]byte(f.Data), &ev) == nil && ev.Type != "" {
		if f.Event != "" {
			ev.Type = f.Event
		}
		return ev
	}
	ev = Event{Type: f.Event}
	if json.Valid([]byte(f.Data)) {
		ev.Data = json.RawMessage(f.Data)
	}
	return ev
}
