// Package events is the agent's in-process event bus (agent.md section 3.10): every subsystem
// publishes what happens (mode changes, model loads, sync progress, alerts) and GET /events streams
// it to aictl, the local UI, ai-desktop and the Survival Portal. The Portal shows "brain ready" and
// survival warnings from these events with no network, so the bus is doomsday-critical.
//
// Properties the rest of the agent relies on:
//
//   - Publish never blocks and never waits for a reader: it appends to a ring of the last 1000
//     events under a short lock and hands the event to each interested subscriber's own queue.
//   - A subscriber that falls more than 256 events behind is dropped (its channel is closed), not
//     waited for. It reconnects with Last-Event-ID and gets a replay, or a resync event when the ring
//     no longer holds what it missed.
//   - The bus lock is never held while writing to a connection: delivery to the network happens in
//     the subscriber's own goroutine, which ends when its context ends, when it is dropped, or when
//     the bus is closed. Nothing is left running after Close and the cancellation of the contexts.
//   - Event data is typed: Publish accepts only the struct registered for the type (types.go), so
//     a publisher cannot invent a shape or put a secret into a free-form map by accident.
//
// The package uses the standard library only and has no dependency on the rest of the agent.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Defaults of the ring and of the per-subscriber queue (agent.md section 3.10).
const (
	// DefaultCapacity is the number of events the ring keeps.
	DefaultCapacity = 1000
	// DefaultSubscriberBuffer is the number of undelivered events a subscriber may have queued
	// before it is dropped.
	DefaultSubscriberBuffer = 256
)

// Errors returned by Publish, Resync and Subscribe.
var (
	// ErrUnknownType: the event type is not in the event table.
	ErrUnknownType = errors.New("events: unknown event type")
	// ErrReservedType: resync and shutdown belong to the bus, not to publishers.
	ErrReservedType = errors.New("events: reserved event type")
	// ErrPayloadType: the data is not the struct registered for the type.
	ErrPayloadType = errors.New("events: payload does not match the event type")
	// ErrBadReason: Resync was given a reason other than gap or restart.
	ErrBadReason = errors.New("events: unknown resync reason")
	// ErrClosed: the bus was closed (the agent is stopping).
	ErrClosed = errors.New("events: bus closed")
)

// Event is one bus event and, as JSON, the `data` field of its SSE frame.
type Event struct {
	// ID is the per-process monotonic id, starting at 1. It is 0 on the synthetic resync a client
	// gets when its Last-Event-ID cannot be honoured: that frame carries no id line, so it does not
	// move the client's Last-Event-ID.
	ID uint64 `json:"id"`
	// Type is the event type, one of the Type* constants.
	Type string `json:"type"`
	// At is when the event was published, UTC, to the second.
	At time.Time `json:"at"`
	// Node is node.id of this agent.
	Node string `json:"node"`
	// OpID is the operation the event belongs to; omitted when there is none.
	OpID string `json:"op_id,omitempty"`
	// Data is the typed payload as JSON. It is shared by every subscriber: do not modify it.
	Data json.RawMessage `json:"data"`
}

// Options configures a Bus. Every field is optional.
type Options struct {
	// Node is node.id, stamped on every event.
	Node string
	// Capacity is the ring size, default DefaultCapacity.
	Capacity int
	// SubscriberBuffer is the queue limit of one subscriber, default DefaultSubscriberBuffer.
	SubscriberBuffer int
	// Now is the clock for Event.At, default time.Now. Tests inject a fixed one.
	Now func() time.Time
}

// Counters are the bus counters of GET /status?counters=1.
type Counters struct {
	// Published is the number of events accepted (including resync events).
	Published uint64 `json:"published"`
	// DroppedClients is the number of subscribers dropped for falling behind.
	DroppedClients uint64 `json:"dropped_clients"`
	// Rejected is the number of Publish calls refused for an unknown type or a wrong payload.
	Rejected uint64 `json:"rejected"`
	// Subscribers is the number of live subscribers.
	Subscribers int `json:"subscribers"`
}

// Bus is the event bus. Create it with New; it is safe for concurrent use.
type Bus struct {
	node     string
	capacity int
	subBuf   int
	now      func() time.Time

	mu     sync.Mutex
	ring   []Event
	start  int    // index of the oldest event
	count  int    // events in the ring
	lastID uint64 // id of the newest event; 0 before the first
	subs   map[*subscriber]struct{}
	closed bool

	published atomic.Uint64
	dropped   atomic.Uint64
	rejected  atomic.Uint64
}

// New returns a bus.
func New(opts Options) *Bus {
	b := &Bus{node: opts.Node, capacity: opts.Capacity, subBuf: opts.SubscriberBuffer, now: opts.Now}
	if b.capacity <= 0 {
		b.capacity = DefaultCapacity
	}
	if b.subBuf <= 0 {
		b.subBuf = DefaultSubscriberBuffer
	}
	if b.now == nil {
		b.now = time.Now
	}
	b.ring = make([]Event, b.capacity)
	b.subs = map[*subscriber]struct{}{}
	return b
}

// Publish adds an event of type typ for operation opID (empty when there is none). data must be the
// struct registered for typ (a value or a pointer), for example ModelLoaded for TypeModelLoaded. It
// never blocks. A refused event (unknown type, wrong payload, reserved type) is counted in
// Counters.Rejected and returned as an error; it is a programming error, so callers may log it and
// carry on. Publish keeps working after Close, so a subsystem that stops late cannot panic.
func (b *Bus) Publish(typ, opID string, data any) error {
	if err := checkPayload(typ, data); err != nil {
		b.rejected.Add(1)
		return err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		b.rejected.Add(1)
		return fmt.Errorf("events: encode %s: %w", typ, err)
	}
	b.add(typ, opID, raw)
	return nil
}

// Resync adds a resync event for every client: "re-read /status, you may have missed events". The
// agent calls Resync(ResyncRestart) when startup completes (agent.md section 4.1 step 17). Because
// the event sits in the ring with an id, a client that reconnects with an older Last-Event-ID gets
// it in its replay.
func (b *Bus) Resync(reason string) error {
	if reason != ResyncGap && reason != ResyncRestart {
		return fmt.Errorf("%w: %q", ErrBadReason, reason)
	}
	b.add(TypeResync, "", resyncData(reason))
	return nil
}

func resyncData(reason string) json.RawMessage {
	raw, _ := json.Marshal(ResyncData{Reason: reason})
	return raw
}

// add stores the event and hands it to every subscriber whose filter matches. The lock is short:
// no I/O and no wait, only appends.
func (b *Bus) add(typ, opID string, raw json.RawMessage) {
	b.mu.Lock()
	b.lastID++
	ev := Event{ID: b.lastID, Type: typ, At: b.stamp(), Node: b.node, OpID: opID, Data: raw}
	if b.count < b.capacity {
		b.ring[(b.start+b.count)%b.capacity] = ev
		b.count++
	} else {
		b.ring[b.start] = ev
		b.start = (b.start + 1) % b.capacity
	}
	for s := range b.subs {
		if s.filter.Match(typ) && !s.push(ev) {
			delete(b.subs, s)
			s.stop()
			b.dropped.Add(1)
		}
	}
	b.mu.Unlock()
	b.published.Add(1)
}

func (b *Bus) stamp() time.Time { return b.now().UTC().Truncate(time.Second) }

// LastID returns the id of the newest event, 0 when none was published yet.
func (b *Bus) LastID() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastID
}

// Counters returns the bus counters.
func (b *Bus) Counters() Counters {
	b.mu.Lock()
	n := len(b.subs)
	b.mu.Unlock()
	return Counters{
		Published:      b.published.Load(),
		DroppedClients: b.dropped.Load(),
		Rejected:       b.rejected.Load(),
		Subscribers:    n,
	}
}

// Close ends every subscription (their channels close) and makes Subscribe fail with ErrClosed. It
// is idempotent. The agent calls it after the HTTP server stopped (agent.md section 4.12).
func (b *Bus) Close() {
	b.mu.Lock()
	b.closed = true
	subs := b.subs
	b.subs = map[*subscriber]struct{}{}
	b.mu.Unlock()
	for s := range subs {
		s.stop()
	}
}

// Subscribe returns a channel of the events that pass filter. The channel is closed when ctx ends,
// when the subscriber is dropped for being too slow (more than SubscriberBuffer events queued), or
// when the bus is closed; the caller tells these apart by its own context. Cancel ctx to unsubscribe:
// the goroutine behind the channel then exits, whether or not anyone still reads it.
//
// lastID is the client's Last-Event-ID, 0 for none (a new client sees live events only). Otherwise:
//
//   - lastID is the newest id, or the ring still holds every event after it: those events, in
//     order, come first (none when lastID is the newest), then live events, with no gap and no
//     duplicate;
//   - the ring no longer holds event lastID+1: a synthetic resync (reason gap, id 0) comes first,
//     then everything the ring holds, then live events;
//   - lastID is greater than the newest id (it names an event of a previous agent process): a
//     synthetic resync (reason restart, id 0) comes first, then live events.
//
// The replay is chosen and the subscriber registered under one lock, which is what makes the
// hand-over to live events gapless.
func (b *Bus) Subscribe(ctx context.Context, filter Filter, lastID uint64) (<-chan Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &subscriber{
		bus:    b,
		filter: filter,
		limit:  b.subBuf,
		out:    make(chan Event),
		wake:   make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	if lastID > 0 {
		s.replay = b.replayLocked(filter, lastID)
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go s.run(ctx)
	return s.out, nil
}

// replayLocked builds the events a client with Last-Event-ID lastID is owed. b.mu is held.
func (b *Bus) replayLocked(filter Filter, lastID uint64) []Event {
	switch {
	case lastID > b.lastID:
		return []Event{b.synthetic(ResyncRestart)}
	case lastID == b.lastID:
		return nil
	}
	oldest := b.lastID - uint64(b.count) + 1 // count > 0 here: lastID < b.lastID means an event exists
	var out []Event
	from := lastID + 1
	if from < oldest {
		out = append(out, b.synthetic(ResyncGap))
		from = oldest
	}
	for id := from; id <= b.lastID; id++ {
		ev := b.ring[(b.start+int(id-oldest))%b.capacity]
		if filter.Match(ev.Type) {
			out = append(out, ev)
		}
	}
	return out
}

func (b *Bus) synthetic(reason string) Event {
	return Event{Type: TypeResync, At: b.stamp(), Node: b.node, Data: resyncData(reason)}
}

// remove forgets a subscriber whose goroutine ended.
func (b *Bus) remove(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// subscriber is one Subscribe call: a bounded queue filled by the bus under its lock, and a
// goroutine that moves the queue to the unbuffered out channel.
type subscriber struct {
	bus    *Bus
	filter Filter
	limit  int
	out    chan Event
	wake   chan struct{} // capacity 1: "the queue is not empty"
	done   chan struct{} // closed by stop: dropped, or the bus closed
	once   sync.Once

	replay []Event // delivered first; touched by the goroutine only

	mu    sync.Mutex
	queue []Event // live events not yet delivered; at most limit
}

// push queues ev and reports false when the queue is full: the subscriber must be dropped. It never
// blocks. The bus lock is held by the caller; the order is always bus.mu, then s.mu.
func (s *subscriber) push(ev Event) bool {
	s.mu.Lock()
	if len(s.queue) >= s.limit {
		s.mu.Unlock()
		return false
	}
	s.queue = append(s.queue, ev)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

func (s *subscriber) stop() { s.once.Do(func() { close(s.done) }) }

// front returns the oldest queued event without removing it: it leaves the queue only once
// delivered, so the limit counts the event being written too.
func (s *subscriber) front() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return Event{}, false
	}
	return s.queue[0], true
}

func (s *subscriber) advance() {
	s.mu.Lock()
	s.queue[0] = Event{}
	s.queue = s.queue[1:]
	s.mu.Unlock()
}

// send delivers ev, or gives up when the subscriber ends.
func (s *subscriber) send(ctx context.Context, ev Event) bool {
	select {
	case s.out <- ev:
		return true
	case <-ctx.Done():
		return false
	case <-s.done:
		return false
	}
}

func (s *subscriber) run(ctx context.Context) {
	defer func() {
		s.bus.remove(s)
		close(s.out)
	}()
	for _, ev := range s.replay {
		if !s.send(ctx, ev) {
			return
		}
	}
	s.replay = nil
	for {
		if ev, ok := s.front(); ok {
			if !s.send(ctx, ev) {
				return
			}
			s.advance()
			continue
		}
		select {
		case <-s.wake:
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}
