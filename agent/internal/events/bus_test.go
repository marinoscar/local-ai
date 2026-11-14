package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/logx"
)

var epoch = time.Date(2026, 11, 2, 15, 4, 5, 987000000, time.UTC)

func newBus(opts ...func(*Options)) *Bus {
	o := Options{Node: "node-a", Now: func() time.Time { return epoch }}
	for _, f := range opts {
		f(&o)
	}
	return New(o)
}

// pub publishes n model.loaded events (ids continue from the bus).
func pub(t *testing.T, b *Bus, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "model-a", LoadMS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// recv reads one event or fails after a second.
func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("channel closed, want an event")
		}
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

// collect reads the events a subscription must deliver: exactly want of them, however slowly the
// delivery goroutine is scheduled (it fails after 10 s, not after a pause), and then whatever else
// arrives within a short quiet window, so a test that expects "nothing more" still sees an extra
// event. Only the last part is time-based, and it can miss an extra event but never invent one.
//
// It replaces an idle-timeout drain: that stopped at the first 50 ms pause, which a loaded machine
// (-race, several packages at once) produces in the middle of a 1000-event replay.
func collect(t *testing.T, ch <-chan Event, want int) []Event {
	t.Helper()
	var out []Event
	deadline := time.After(10 * time.Second)
	for len(out) < want {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-deadline:
			t.Fatalf("got %d events, want %d (timed out)", len(out), want)
		}
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(50 * time.Millisecond):
			return out
		}
	}
}

func ids(evs []Event) []uint64 {
	out := make([]uint64, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func expectClosed(t *testing.T, ch <-chan Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("channel was not closed")
		}
	}
}

// settleGoroutines fails when the number of goroutines stays above base.
func settleGoroutines(t *testing.T, base int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("goroutine leak: %d running, baseline %d\n%s", runtime.NumGoroutine(), base, buf)
}

func TestPublishStampsEvents(t *testing.T) {
	b := newBus()
	ch, err := b.Subscribe(context.Background(), AllTypes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(TypeModelLoaded, "op_7k2m9q4r8t1c", ModelLoaded{Model: "model-a", LoadMS: 7100}); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(TypeKeyRotated, "", &KeyRotated{Fingerprint: "fp1"}); err != nil {
		t.Fatal(err)
	}
	e1, e2 := recv(t, ch), recv(t, ch)
	if e1.ID != 1 || e2.ID != 2 {
		t.Fatalf("ids = %d, %d; want 1, 2", e1.ID, e2.ID)
	}
	if e1.Node != "node-a" || e1.OpID != "op_7k2m9q4r8t1c" || e1.Type != TypeModelLoaded {
		t.Fatalf("event = %+v", e1)
	}
	if !e1.At.Equal(epoch.Truncate(time.Second)) {
		t.Fatalf("at = %v, want the clock truncated to the second", e1.At)
	}
	raw, err := json.Marshal(e1)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":1,"type":"model.loaded","at":"2026-11-02T15:04:05Z","node":"node-a","op_id":"op_7k2m9q4r8t1c","data":{"model":"model-a","load_ms":7100}}`
	if string(raw) != want {
		t.Fatalf("wire = %s\nwant   %s", raw, want)
	}
	raw2, _ := json.Marshal(e2)
	if want := `{"id":2,"type":"key.rotated","at":"2026-11-02T15:04:05Z","node":"node-a","data":{"fingerprint":"fp1"}}`; string(raw2) != want {
		t.Fatalf("wire = %s\nwant   %s", raw2, want)
	}
}

func TestPublishValidation(t *testing.T) {
	var nilLoaded *ModelLoaded
	tests := []struct {
		name string
		typ  string
		data any
		want error
	}{
		{"ok value", TypeModelLoaded, ModelLoaded{Model: "m"}, nil},
		{"ok pointer", TypeModelLoaded, &ModelLoaded{Model: "m"}, nil},
		{"unknown type", "model.exploded", ModelLoaded{}, ErrUnknownType},
		{"empty type", "", ModelLoaded{}, ErrUnknownType},
		{"resync is the bus's", TypeResync, ResyncData{Reason: ResyncGap}, ErrReservedType},
		{"shutdown is the handler's", TypeShutdown, struct{}{}, ErrReservedType},
		{"wrong struct", TypeModelLoaded, ModelUnloaded{Model: "m"}, ErrPayloadType},
		{"free-form map", TypeModelLoaded, map[string]any{"model": "m"}, ErrPayloadType},
		{"nil data", TypeModelLoaded, nil, ErrPayloadType},
		{"nil pointer", TypeModelLoaded, nilLoaded, ErrPayloadType},
		{"alert shared by raised and cleared", TypeAlertCleared, Alert{Code: "c"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBus()
			err := b.Publish(tc.typ, "", tc.data)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			c := b.Counters()
			if tc.want == nil && (c.Published != 1 || c.Rejected != 0 || b.LastID() != 1) {
				t.Fatalf("counters = %+v", c)
			}
			if tc.want != nil && (c.Published != 0 || c.Rejected != 1 || b.LastID() != 0) {
				t.Fatalf("a refused event must not take an id: %+v, last id %d", c, b.LastID())
			}
		})
	}
}

func TestRingKeepsExactlyTheLastThousand(t *testing.T) {
	b := newBus()
	pub(t, b, 1500)
	if got := b.LastID(); got != 1500 {
		t.Fatalf("last id = %d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 500 is the newest id that is not a gap: event 501 is the oldest the ring holds.
	ch, err := b.Subscribe(ctx, AllTypes(), 500)
	if err != nil {
		t.Fatal(err)
	}
	got := collect(t, ch, 1000)
	if len(got) != 1000 || got[0].ID != 501 || got[999].ID != 1500 {
		t.Fatalf("replay from 500: %d events, first %d, last %d; want 1000 events 501..1500", len(got), got[0].ID, got[len(got)-1].ID)
	}
	for i, e := range got {
		if e.ID != uint64(501+i) {
			t.Fatalf("replay not contiguous at %d: id %d", i, e.ID)
		}
	}
	// 499 is one too old.
	ch2, _ := b.Subscribe(ctx, AllTypes(), 499)
	got = collect(t, ch2, 1001)
	if len(got) != 1001 || got[0].Type != TypeResync || got[0].ID != 0 || got[1].ID != 501 {
		t.Fatalf("replay from 499: %d events, first %+v", len(got), got[0])
	}
}

func TestIDsStartAtOneAndRingOfOneEvent(t *testing.T) {
	b := newBus(func(o *Options) { o.Capacity = 1 })
	pub(t, b, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b.Subscribe(ctx, AllTypes(), 2)
	got := collect(t, ch, 1)
	if !reflect.DeepEqual(ids(got), []uint64{3}) {
		t.Fatalf("ids = %v, want [3]", ids(got))
	}
	ch, _ = b.Subscribe(ctx, AllTypes(), 1)
	got = collect(t, ch, 2)
	if !reflect.DeepEqual(ids(got), []uint64{0, 3}) || got[0].Type != TypeResync {
		t.Fatalf("ids = %v, want a gap resync then 3", ids(got))
	}
}

func resyncReason(t *testing.T, ev Event) string {
	t.Helper()
	if ev.Type != TypeResync {
		t.Fatalf("event %+v is not a resync", ev)
	}
	var d ResyncData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d.Reason
}

func TestReplayTable(t *testing.T) {
	const published = 10
	tests := []struct {
		name       string
		capacity   int
		lastID     uint64
		filter     string
		wantIDs    []uint64 // ids in order; 0 is a synthetic resync
		wantReason string   // reason of the first event when it is a resync
	}{
		{name: "no Last-Event-ID is live only", capacity: 100, lastID: 0, wantIDs: nil},
		{name: "up to date", capacity: 100, lastID: 10, wantIDs: nil},
		{name: "one missed", capacity: 100, lastID: 9, wantIDs: []uint64{10}},
		{name: "all missed but the first", capacity: 100, lastID: 1, wantIDs: []uint64{2, 3, 4, 5, 6, 7, 8, 9, 10}},
		{name: "evicted id yields a gap first", capacity: 4, lastID: 2, wantIDs: []uint64{0, 7, 8, 9, 10}, wantReason: ResyncGap},
		{name: "oldest buffered is not a gap", capacity: 4, lastID: 6, wantIDs: []uint64{7, 8, 9, 10}},
		{name: "id from a previous process", capacity: 100, lastID: 11, wantIDs: []uint64{0}, wantReason: ResyncRestart},
		{name: "far larger id", capacity: 100, lastID: 99999, wantIDs: []uint64{0}, wantReason: ResyncRestart},
		{name: "replay of a matching filter", capacity: 100, lastID: 8, filter: "model.loaded", wantIDs: []uint64{9, 10}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newBus(func(o *Options) { o.Capacity = tc.capacity })
			pub(t, b, published)
			f, err := ParseFilter(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, err := b.Subscribe(ctx, f, tc.lastID)
			if err != nil {
				t.Fatal(err)
			}
			got := collect(t, ch, len(tc.wantIDs))
			if !reflect.DeepEqual(ids(got), tc.wantIDs) && !(len(got) == 0 && len(tc.wantIDs) == 0) {
				t.Fatalf("ids = %v, want %v", ids(got), tc.wantIDs)
			}
			if tc.wantReason != "" {
				if r := resyncReason(t, got[0]); r != tc.wantReason {
					t.Fatalf("reason = %q, want %q", r, tc.wantReason)
				}
			}
		})
	}
}

func TestReplayIsFilteredAndAnEmptyRingIsARestart(t *testing.T) {
	b := newBus()
	_ = b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "m"})
	_ = b.Publish(TypeSyncDone, "", SyncDone{RunID: "r"})
	_ = b.Publish(TypeModeChanged, "", ModeChanged{From: "online", To: "offline"})
	_ = b.Publish(TypeSyncDone, "", SyncDone{RunID: "r2"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _ := ParseFilter("mode.*")
	ch, _ := b.Subscribe(ctx, f, 1)
	if got := ids(collect(t, ch, 1)); !reflect.DeepEqual(got, []uint64{3}) {
		t.Fatalf("filtered replay = %v, want [3]", got)
	}

	empty := newBus()
	ch, _ = empty.Subscribe(ctx, AllTypes(), 5)
	got := collect(t, ch, 1)
	if len(got) != 1 || resyncReason(t, got[0]) != ResyncRestart {
		t.Fatalf("a client from a previous process on a fresh bus must get resync restart, got %+v", got)
	}
}

func TestResyncBroadcastReachesEveryFilterAndIsReplayed(t *testing.T) {
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	narrow, _ := ParseFilter("sync.done")
	ch, _ := b.Subscribe(ctx, narrow, 0)
	none, _ := NewFilter()
	chNone, _ := b.Subscribe(ctx, none, 0)

	if err := b.Resync("bogus"); !errors.Is(err, ErrBadReason) {
		t.Fatalf("err = %v, want ErrBadReason", err)
	}
	if err := b.Resync(ResyncRestart); err != nil {
		t.Fatal(err)
	}
	for _, c := range []<-chan Event{ch, chNone} {
		ev := recv(t, c)
		if ev.ID != 1 || resyncReason(t, ev) != ResyncRestart {
			t.Fatalf("resync event = %+v", ev)
		}
	}
	// A client of the previous process, with an older id, finds it in its replay.
	_ = b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "m"})
	stale, _ := b.Subscribe(ctx, AllTypes(), 1)
	if got := ids(collect(t, stale, 1)); !reflect.DeepEqual(got, []uint64{2}) {
		t.Fatalf("ids = %v", got)
	}
}

func TestFilterParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr bool
	}{
		{"default is all", "", []string{"*"}, false},
		{"blank is all", "  ,, ", []string{"*"}, false},
		{"star is all", "*", []string{"*"}, false},
		{"exact", "mode.changed", []string{"mode.changed"}, false},
		{"wildcard", "model.*", []string{"model.*"}, false},
		{"multiple with spaces", " model.* , mode.changed ", []string{"mode.changed", "model.*"}, false},
		{"exact covered by wildcard is dropped", "model.loaded,model.*", []string{"model.*"}, false},
		{"narrow wildcard covered by a wider one", "model.l*,model.*", []string{"model.*"}, false},
		{"star in the middle", "mod*.loaded", nil, true},
		{"double star", "model.**", nil, true},
		{"upper case", "Model.*", nil, true},
		{"space inside", "model loaded", nil, true},
		{"newline", "model.*\nx", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ParseFilter(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got := f.Patterns(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("patterns = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterMatch(t *testing.T) {
	tests := []struct {
		name    string
		filter  string
		typ     string
		matches bool
	}{
		{"default all", "", TypeSyncDone, true},
		{"exact hit", "mode.changed", TypeModeChanged, true},
		{"exact miss", "mode.changed", TypeModeStep, false},
		{"exact is not a prefix", "mode", TypeModeChanged, false},
		{"wildcard hit", "model.*", TypeModelFailed, true},
		{"wildcard miss", "model.*", TypeModeChanged, false},
		{"wildcard needs the dot", "model.*", "modelx", false},
		{"multiple, first", "model.*,mode.changed", TypeModelLoaded, true},
		{"multiple, second", "model.*,mode.changed", TypeModeChanged, true},
		{"multiple, none", "model.*,mode.changed", TypeAlertRaised, false},
		{"star", "*", TypeKeyRotated, true},
		{"resync always passes", "alert.*", TypeResync, true},
		{"shutdown always passes", "alert.*", TypeShutdown, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ParseFilter(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if got := f.Match(tc.typ); got != tc.matches {
				t.Fatalf("Match(%q) = %v, want %v", tc.typ, got, tc.matches)
			}
		})
	}
}

func TestFilterRestrictToPortalTypes(t *testing.T) {
	tests := []struct {
		name string
		ask  string
		want []string
	}{
		{"star yields exactly the allowed set", "*", []string{"alert.*", "drive.*", "mode.*", "model.*", "service.state"}},
		{"default yields the allowed set", "", []string{"alert.*", "drive.*", "mode.*", "model.*", "service.state"}},
		{"allowed pattern kept", "model.*", []string{"model.*"}},
		{"exact inside an allowed prefix", "model.loaded", []string{"model.loaded"}},
		{"narrower wildcard", "model.l*", []string{"model.l*"}},
		{"exact allowed type", "service.state", []string{"service.state"}},
		{"forbidden dropped silently", "sync.*,model.*", []string{"model.*"}},
		{"only forbidden leaves nothing", "sync.*,cloud.*,key.*", []string{}},
		{"forbidden exact", "cloud.state", []string{}},
		{"wildcard wider than an exact allowed type", "service.*", []string{"service.state"}},
		{"s prefix reaches service.state but not sync.*", "s*", []string{"service.state"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := ParseFilter(tc.ask)
			if err != nil {
				t.Fatal(err)
			}
			got := f.Restrict(PortalTypes())
			if p := got.Patterns(); !reflect.DeepEqual(p, tc.want) {
				t.Fatalf("patterns = %v, want %v", p, tc.want)
			}
			for _, typ := range Types() {
				allowed := PortalTypes().Match(typ)
				if got.Match(typ) && !allowed {
					t.Fatalf("a portal filter matches %s, which the Portal may never see", typ)
				}
			}
		})
	}
	// Restricting by "everything" changes nothing.
	f, _ := ParseFilter("sync.*")
	if got := f.Restrict(AllTypes()).Patterns(); !reflect.DeepEqual(got, []string{"sync.*"}) {
		t.Fatalf("patterns = %v", got)
	}
}

func TestSubscriberFilterAppliesToLiveEvents(t *testing.T) {
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f, _ := ParseFilter("model.*,mode.changed")
	ch, _ := b.Subscribe(ctx, f, 0)
	_ = b.Publish(TypeSyncDone, "", SyncDone{RunID: "r"})
	_ = b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "m"})
	_ = b.Publish(TypeCloudState, "", CloudState{State: "linked"})
	_ = b.Publish(TypeModeChanged, "", ModeChanged{From: "online", To: "offline"})
	_ = b.Publish(TypeModeStep, "", ModeStep{Target: "offline", Step: "s", State: "done"})
	got := ids(collect(t, ch, 2))
	if !reflect.DeepEqual(got, []uint64{2, 4}) {
		t.Fatalf("ids = %v, want [2 4]", got)
	}
}

func TestSlowSubscriberIsDroppedWithoutBlockingPublishers(t *testing.T) {
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slow, _ := b.Subscribe(ctx, AllTypes(), 0) // never read
	fast, _ := b.Subscribe(ctx, AllTypes(), 0)

	got := make(chan uint64, 1000)
	go func() {
		for ev := range fast {
			got <- ev.ID
		}
	}()

	published := make(chan struct{})
	go func() {
		defer close(published)
		// Rounds of 100 events, each fully read by the healthy client before the next, so that
		// only the stalled one ever falls behind.
		for round := 0; round < 6; round++ {
			pub(t, b, 100)
			for i := 0; i < 100; i++ {
				select {
				case <-got:
				case <-time.After(5 * time.Second):
					t.Error("the healthy subscriber stopped receiving")
					return
				}
			}
		}
	}()
	select {
	case <-published:
	case <-time.After(20 * time.Second):
		t.Fatal("a stalled subscriber blocked the publisher")
	}
	expectClosed(t, slow)
	c := b.Counters()
	if c.DroppedClients != 1 || c.Published != 600 || c.Subscribers != 1 {
		t.Fatalf("counters = %+v, want 1 dropped client, 600 published, 1 subscriber", c)
	}
}

func TestSlowSubscriberLimitIsExactly256(t *testing.T) {
	for _, tc := range []struct {
		name      string
		events    int
		wantDrop  bool
		wantQueue int
	}{
		{"256 queued is fine", 256, false, 256},
		{"257th is too many", 257, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBus()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ch, _ := b.Subscribe(ctx, AllTypes(), 0)
			pub(t, b, tc.events)
			if dropped := b.Counters().DroppedClients == 1; dropped != tc.wantDrop {
				t.Fatalf("dropped = %v, want %v", dropped, tc.wantDrop)
			}
			if tc.wantDrop {
				expectClosed(t, ch)
				return
			}
			if got := collect(t, ch, tc.wantQueue); len(got) != tc.wantQueue {
				t.Fatalf("read %d events, want %d", len(got), tc.wantQueue)
			}
		})
	}
}

func TestSlowReaderThatCatchesUpIsKept(t *testing.T) {
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b.Subscribe(ctx, AllTypes(), 0)
	// 5 rounds of 200 events: each round fits the queue, and the reader drains between rounds.
	var next uint64 = 1
	for round := 0; round < 5; round++ {
		pub(t, b, 200)
		for i := 0; i < 200; i++ {
			if ev := recv(t, ch); ev.ID != next {
				t.Fatalf("id = %d, want %d", ev.ID, next)
			}
			next++
		}
	}
	if b.Counters().DroppedClients != 0 {
		t.Fatal("a reader that keeps up must not be dropped")
	}
}

func TestReplayDoesNotCountAgainstTheLiveLimit(t *testing.T) {
	b := newBus()
	pub(t, b, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b.Subscribe(ctx, AllTypes(), 1) // 999 events to replay, over the 256 live limit
	pub(t, b, 100)
	got := collect(t, ch, 999+100)
	if len(got) != 999+100 || got[len(got)-1].ID != 1100 {
		t.Fatalf("got %d events, last %d; want 1099 ending at 1100", len(got), got[len(got)-1].ID)
	}
	if b.Counters().DroppedClients != 0 {
		t.Fatal("a replay must not drop the client")
	}
}

func TestContextCancelEndsTheSubscription(t *testing.T) {
	b := newBus()
	base := runtime.NumGoroutine()
	var cancels []context.CancelFunc
	var chans []<-chan Event
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := b.Subscribe(ctx, AllTypes(), 0)
		if err != nil {
			t.Fatal(err)
		}
		cancels = append(cancels, cancel)
		chans = append(chans, ch)
	}
	pub(t, b, 5) // some subscribers have an event in flight that nobody reads
	if n := b.Counters().Subscribers; n != 20 {
		t.Fatalf("subscribers = %d", n)
	}
	for _, c := range cancels {
		c()
	}
	for _, ch := range chans {
		expectClosed(t, ch)
	}
	settleGoroutines(t, base)
	if n := b.Counters().Subscribers; n != 0 {
		t.Fatalf("subscribers = %d after cancel, want 0", n)
	}
	// Publishing with nobody left is fine.
	pub(t, b, 3)
}

func TestSubscribeWithACancelledContext(t *testing.T) {
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Subscribe(ctx, AllTypes(), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if b.Counters().Subscribers != 0 {
		t.Fatal("no subscriber must be registered")
	}
}

func TestCloseEndsEverySubscription(t *testing.T) {
	b := newBus()
	base := runtime.NumGoroutine()
	var chans []<-chan Event
	for i := 0; i < 10; i++ {
		ch, err := b.Subscribe(context.Background(), AllTypes(), 0)
		if err != nil {
			t.Fatal(err)
		}
		chans = append(chans, ch)
	}
	pub(t, b, 3)
	b.Close()
	b.Close() // idempotent
	for _, ch := range chans {
		expectClosed(t, ch)
	}
	settleGoroutines(t, base)
	if _, err := b.Subscribe(context.Background(), AllTypes(), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if err := b.Publish(TypeKeyRotated, "", KeyRotated{Fingerprint: "x"}); err != nil {
		t.Fatalf("publishing after Close must stay harmless, got %v", err)
	}
	if got := b.Counters().DroppedClients; got != 0 {
		t.Fatalf("Close is not a slow-client drop, dropped = %d", got)
	}
}

func TestFiftyConcurrentSubscribers(t *testing.T) {
	const (
		subscribers = 50
		publishers  = 4
		perPub      = 500
		total       = publishers * perPub
	)
	b := newBus(func(o *Options) { o.SubscriberBuffer = total }) // nobody may be dropped in this test
	base := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	results := make([][]uint64, subscribers)
	for i := 0; i < subscribers; i++ {
		ch, err := b.Subscribe(ctx, AllTypes(), 0)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ev := range ch {
				results[i] = append(results[i], ev.ID)
				if ev.ID == total {
					return
				}
			}
		}()
	}
	var pw sync.WaitGroup
	for p := 0; p < publishers; p++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			for i := 0; i < perPub; i++ {
				_ = b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "model-a", LoadMS: int64(i)})
			}
		}()
	}
	pw.Wait()
	wg.Wait()
	for i, r := range results {
		if len(r) != total {
			t.Fatalf("subscriber %d got %d events, want %d", i, len(r), total)
		}
		for j, id := range r {
			if id != uint64(j+1) {
				t.Fatalf("subscriber %d: position %d has id %d; order or completeness broken", i, j, id)
			}
		}
	}
	cancel()
	settleGoroutines(t, base)
	if c := b.Counters(); c.DroppedClients != 0 || c.Published != total {
		t.Fatalf("counters = %+v", c)
	}
}

// A client that reconnects while events are being published sees every id after its own exactly
// once and in order: the replay and the live stream join without a gap or a duplicate.
func TestReplayToLiveHandOverIsGapless(t *testing.T) {
	b := newBus(func(o *Options) { o.SubscriberBuffer = 5000 })
	pub(t, b, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan struct{})
	var pw sync.WaitGroup
	pw.Add(1)
	go func() {
		defer pw.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = b.Publish(TypeModelLoaded, "", ModelLoaded{Model: "m"})
				time.Sleep(10 * time.Microsecond)
			}
		}
	}()
	for round := 0; round < 20; round++ {
		last := b.LastID() - 10
		ch, err := b.Subscribe(ctx, AllTypes(), last)
		if err != nil {
			t.Fatal(err)
		}
		next := last + 1
		for i := 0; i < 200; i++ {
			ev := recv(t, ch)
			if ev.ID != next {
				t.Fatalf("round %d: id %d, want %d", round, ev.ID, next)
			}
			next++
		}
	}
	close(stop)
	pw.Wait()
}

func TestPublishNeverWaitsOnTheNetwork(t *testing.T) {
	// 50 subscribers that never read: Publish must return promptly for every event.
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 50; i++ {
		if _, err := b.Subscribe(ctx, AllTypes(), 0); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	pub(t, b, 2000)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("2000 publishes with 50 stalled subscribers took %v", d)
	}
	if c := b.Counters(); c.DroppedClients != 50 || c.Subscribers != 0 {
		t.Fatalf("counters = %+v, want all 50 dropped", c)
	}
}

// examples holds one valid value of every payload struct, with the fields filled the way a real
// publisher would.
func examples() map[string]any {
	pct := 83
	progress := 0.42
	return map[string]any{
		TypeModeChanged:         ModeChanged{From: "online", To: "offline", OpID: "op_7k2m9q4r8t1c", Warnings: []Warning{{Code: "LAI-AGT-003", Message: "a warning"}}},
		TypeModeStep:            ModeStep{Target: "offline", Step: "stop_tailscale", State: "failed", Code: "LAI-AGT-004"},
		TypeModelLoading:        ModelLoading{Model: "model-a", Unloading: []string{"model-b"}, OpID: "op_7k2m9q4r8t1c", Progress: &progress},
		TypeModelLoaded:         ModelLoaded{Model: "model-a", LoadMS: 7100},
		TypeModelUnloaded:       ModelUnloaded{Model: "model-a", Reason: UnloadEvicted},
		TypeModelFailed:         ModelFailed{Model: "model-a", Code: "LAI-AGT-007", Message: "did not fit", RolledBack: true, Restored: []string{"model-b"}},
		TypeModelDefaultChanged: ModelDefaultChanged{Model: "model-a", Previous: "model-b"},
		TypeSyncProgress:        SyncProgress{RunID: "run-20261102T031500Z-a1f3", Root: "ark-3f9c0a12", Item: "item-a", State: "downloading", BytesDone: 10, BytesTotal: 100, RateBPS: 5},
		TypeSyncItemDone:        SyncItemDone{RunID: "run-1", Root: "ark-3f9c0a12", Item: "item-a", Result: "quarantined", Version: "v1", Reason: "checksum", Code: "LAI-AGT-008"},
		TypeSyncDone:            SyncDone{RunID: "run-1", State: "done", Totals: json.RawMessage(`{"verified":3}`), Ready: map[string]string{"ark-3f9c0a12": "ready"}},
		TypeServiceState:        ServiceState{Name: "maps", Unit: "unit-a.service", From: "healthy", To: "unhealthy", Active: "active", Sub: "running", Healthy: false, Code: "LAI-AGT-009"},
		TypeDriveMounted:        DriveMounted{RootID: "ark-3f9c0a12", Mount: "/mnt/ark-a", State: "mounted", ReadOnly: false},
		TypeDriveUnmounted:      DriveUnmounted{RootID: "ark-3f9c0a12", SafeToUnplug: true},
		TypeAlertRaised:         Alert{Code: "LAI-AGT-010", Severity: "warning", Title: "A title", Fix: "A fix"},
		TypeAlertCleared:        Alert{Code: "LAI-AGT-010", Severity: "warning", Title: "A title", Fix: "A fix"},
		TypeCloudState:          CloudState{State: "linked", Previous: "offline", LastError: ""},
		TypeHardwarePower:       HardwarePower{Source: "battery", BatteryPercent: &pct, Charging: false},
		TypeKeyRotated:          KeyRotated{Fingerprint: "ab12…9xQz"},
		TypeUpdateState:         UpdateState{State: "available", From: "1.0.0", To: "1.1.0"},
		TypeDrillStarted:        DrillStarted{RunID: "drill-1", Trigger: "manual", Quick: true},
		TypeDrillCheckDone:      DrillCheckDone{RunID: "drill-1", ID: "check-a", Result: "warn", Code: "LAI-DRL-001"},
		TypeDrillDone:           DrillDone{RunID: "drill-1", Overall: "pass", SurvivalReady: true},
		TypeWorkloadProgress:    WorkloadProgress{OpID: "op_1", Name: "music", Phase: WorkloadLoading, Model: "m-a", Done: 1, Total: 2},
		TypeWorkloadDone:        WorkloadDone{OpID: "op_1", Name: "music", Result: WorkloadOK},
	}
}

func TestEveryTypeHasATypedExampleThatPublishesAndStaysClean(t *testing.T) {
	ex := examples()
	for _, typ := range Types() {
		if _, ok := ex[typ]; !ok {
			t.Errorf("event type %s has no example in examples(): add one so the secret check covers it", typ)
		}
	}
	if len(ex) != len(Types()) {
		t.Errorf("examples() has %d entries, the event table %d", len(ex), len(Types()))
	}
	b := newBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b.Subscribe(ctx, AllTypes(), 0)
	for _, typ := range Types() {
		t.Run(typ, func(t *testing.T) {
			if err := b.Publish(typ, "op_7k2m9q4r8t1c", ex[typ]); err != nil {
				t.Fatalf("the example does not match the registered payload: %v", err)
			}
			ev := recv(t, ch)
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			// Event data must never contain a secret: the log scrubber finds nothing to change.
			if got := logx.ScrubString(string(raw)); got != string(raw) {
				t.Fatalf("scrubber changed the event, so it carries something secret-shaped:\n%s\n%s", raw, got)
			}
			var back map[string]any
			if err := json.Unmarshal(raw, &back); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"id", "type", "at", "node", "op_id", "data"} {
				if _, ok := back[k]; !ok {
					t.Fatalf("wire event lacks %q: %s", k, raw)
				}
			}
		})
	}
}

func TestPayloadFieldNamesAreSnakeCase(t *testing.T) {
	for typ, ty := range payloads {
		for i := 0; i < ty.NumField(); i++ {
			tag := ty.Field(i).Tag.Get("json")
			if tag == "" {
				t.Errorf("%s.%s has no json tag", ty.Name(), ty.Field(i).Name)
				continue
			}
			name := tag
			if j := strings.IndexByte(tag, ','); j >= 0 {
				name = tag[:j]
			}
			for _, c := range name {
				if !(c >= 'a' && c <= 'z') && c != '_' {
					t.Errorf("%s (%s): json name %q is not snake_case", typ, ty.Name(), name)
				}
			}
		}
	}
}

func TestCountersAndLastID(t *testing.T) {
	b := newBus()
	if b.LastID() != 0 {
		t.Fatal("a new bus has no events")
	}
	pub(t, b, 3)
	_ = b.Resync(ResyncRestart)
	_ = b.Publish("nope", "", nil)
	c := b.Counters()
	if c.Published != 4 || c.Rejected != 1 || b.LastID() != 4 {
		t.Fatalf("counters = %+v, last id %d", c, b.LastID())
	}
	raw, _ := json.Marshal(c)
	if fmt.Sprint(string(raw)) != `{"published":4,"dropped_clients":0,"rejected":1,"subscribers":0}` {
		t.Fatalf("counters JSON = %s", raw)
	}
}
