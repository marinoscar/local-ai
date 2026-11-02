package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/config/fake"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/logx"
)

var eventsEpoch = time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)

// sseFrame is one parsed frame of the stream: a comment (": ping") or an event.
type sseFrame struct {
	comment string
	id      string
	event   string
	data    string
}

// sseStream reads a response body as SSE frames on a channel that closes at EOF.
type sseStream struct {
	resp   *http.Response
	frames chan sseFrame
	cancel context.CancelFunc
}

func openStream(t *testing.T, url string, hdr map[string]string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := hdr["X-Test-Principal"]; !ok {
		req.Header.Set("X-Test-Principal", "bearer")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	apitest.RecordStream(t, resp) // status and headers must match GET /events of openapi.yaml
	st := &sseStream{resp: resp, frames: make(chan sseFrame, 4096), cancel: cancel}
	go func() {
		defer close(st.frames)
		sc := bufio.NewScanner(resp.Body)
		var cur sseFrame
		has := false
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if has {
					st.frames <- cur
				}
				cur, has = sseFrame{}, false
			case strings.HasPrefix(line, ":"):
				st.frames <- sseFrame{comment: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				cur.id, has = line[4:], true
			case strings.HasPrefix(line, "event: "):
				cur.event, has = line[7:], true
			case strings.HasPrefix(line, "data: "):
				cur.data, has = line[6:], true
			}
		}
	}()
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return st
}

func (s *sseStream) next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if !ok {
			t.Fatal("stream ended, want a frame")
		}
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a frame")
	}
	return sseFrame{}
}

// expectEnd waits for the stream to end (EOF).
func (s *sseStream) expectEnd(t *testing.T) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-s.frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not end")
		}
	}
}

// expectQuiet fails if a frame arrives within a short while.
func (s *sseStream) expectQuiet(t *testing.T) {
	t.Helper()
	select {
	case f, ok := <-s.frames:
		if ok {
			t.Fatalf("unexpected frame %+v", f)
		}
		t.Fatal("stream ended unexpectedly")
	case <-time.After(150 * time.Millisecond):
	}
}

// eventsHarness is a real server (TCP and socket) with GET /events registered on a fresh bus.
type eventsHarness struct {
	srv   *Server
	bus   *events.Bus
	clock *fake.Clock
	logs  *syncBuf
	url   string
}

func newEventsHarness(t *testing.T, mod ...func(*EventsOptions)) *eventsHarness {
	t.Helper()
	h := &eventsHarness{
		bus:   events.New(events.Options{Node: "node-a", Now: func() time.Time { return eventsEpoch }}),
		clock: fake.NewClock(eventsEpoch),
	}
	opts := EventsOptions{
		Bus:      h.bus,
		Logger:   newTestLogger(&syncBuf{}),
		Clock:    h.clock,
		Stopping: func() <-chan struct{} { return h.srv.Stopping() },
	}
	for _, m := range mod {
		m(&opts)
	}
	h.bus = opts.Bus
	reg := NewRegistry()
	if err := RegisterEvents(reg, opts); err != nil {
		t.Fatal(err)
	}
	var logs *syncBuf
	h.srv, _, logs = listenServer(t, func(o *Options) { o.Registry = reg })
	h.logs = logs
	h.url = "http://" + h.srv.TCPAddr().String() + BasePath + "/events"
	t.Cleanup(h.bus.Close)
	return h
}

func (h *eventsHarness) publishLoaded(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := h.bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "model-a", LoadMS: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// waitSubscribers waits until the bus has n subscribers (the handler subscribes after the headers).
func (h *eventsHarness) waitSubscribers(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.bus.Counters().Subscribers == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscribers = %d, want %d", h.bus.Counters().Subscribers, n)
}

func TestEventsRouteIsRegisteredAsSpecified(t *testing.T) {
	rt, err := EventsRoute(EventsOptions{Bus: events.New(events.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Method != "GET" || rt.Path() != "/api/v1/events" || rt.Timeout != 0 || rt.Mutating() || rt.GateExempt {
		t.Fatalf("route = %+v", rt)
	}
	if !reflect.DeepEqual(rt.Auth, []Level{AuthOp, AuthPortal}) {
		t.Fatalf("auth = %v, want op and portal", rt.Auth)
	}
	if _, err := EventsRoute(EventsOptions{}); err == nil {
		t.Fatal("a route without a bus must be refused")
	}
	reg := NewRegistry()
	if err := RegisterEvents(reg, EventsOptions{Bus: events.New(events.Options{})}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterEvents(reg, EventsOptions{Bus: events.New(events.Options{})}); err == nil {
		t.Fatal("registering twice must fail")
	}
}

func TestEventsStreamHeadersAndWireFormat(t *testing.T) {
	h := newEventsHarness(t)
	st := openStream(t, h.url, nil)
	for k, want := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-store",
		"X-Accel-Buffering": "no",
	} {
		if got := st.resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if st.resp.StatusCode != 200 {
		t.Fatalf("status = %d", st.resp.StatusCode)
	}
	h.waitSubscribers(t, 1)
	if err := h.bus.Publish(events.TypeModelLoaded, "op_7k2m9q4r8t1c", events.ModelLoaded{Model: "model-a", LoadMS: 7100}); err != nil {
		t.Fatal(err)
	}
	f := st.next(t)
	want := sseFrame{
		id:    "1",
		event: "model.loaded",
		data:  `{"id":1,"type":"model.loaded","at":"2026-11-02T15:04:05Z","node":"node-a","op_id":"op_7k2m9q4r8t1c","data":{"model":"model-a","load_ms":7100}}`,
	}
	if f != want {
		t.Fatalf("frame = %+v\nwant    %+v", f, want)
	}
}

func TestEventsRawFrameBytes(t *testing.T) {
	ev := events.Event{ID: 1042, Type: events.TypeModelLoaded, At: eventsEpoch, Node: "ai-n3", OpID: "op_7k2m9q4r8t1c",
		Data: json.RawMessage(`{"model":"model-a","load_ms":7100}`)}
	got, err := frameOf(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := "id: 1042\nevent: model.loaded\ndata: {\"id\":1042,\"type\":\"model.loaded\",\"at\":\"2026-11-02T15:04:05Z\",\"node\":\"ai-n3\",\"op_id\":\"op_7k2m9q4r8t1c\",\"data\":{\"model\":\"model-a\",\"load_ms\":7100}}\n\n"
	if string(got) != want {
		t.Fatalf("frame:\n%q\nwant:\n%q", got, want)
	}
	// resync: payload only, and no id line for the synthetic one
	synth, _ := frameOf(events.Event{Type: events.TypeResync, Data: json.RawMessage(`{"reason":"gap"}`)})
	if string(synth) != "event: resync\ndata: {\"reason\":\"gap\"}\n\n" {
		t.Fatalf("synthetic resync frame = %q", synth)
	}
	ring, _ := frameOf(events.Event{ID: 7, Type: events.TypeResync, Data: json.RawMessage(`{"reason":"restart"}`)})
	if string(ring) != "id: 7\nevent: resync\ndata: {\"reason\":\"restart\"}\n\n" {
		t.Fatalf("ring resync frame = %q", ring)
	}
	if string(shutdownFrame) != "event: shutdown\ndata: {}\n\n" {
		t.Fatalf("shutdown frame = %q", shutdownFrame)
	}
}

func TestEventsTypeFilter(t *testing.T) {
	publishAll := func(t *testing.T, h *eventsHarness) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(h.bus.Publish(events.TypeSyncDone, "", events.SyncDone{RunID: "r"}))
		must(h.bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "m"}))
		must(h.bus.Publish(events.TypeModeChanged, "", events.ModeChanged{From: "online", To: "offline"}))
		must(h.bus.Publish(events.TypeModeStep, "", events.ModeStep{Target: "offline", Step: "s", State: "done"}))
		must(h.bus.Publish(events.TypeAlertRaised, "", events.Alert{Code: "c"}))
		must(h.bus.Publish(events.TypeKeyRotated, "", events.KeyRotated{Fingerprint: "f"}))
	}
	tests := []struct {
		name  string
		query string
		want  []string
	}{
		{"default is all", "", []string{"sync.done", "model.loaded", "mode.changed", "mode.step", "alert.raised", "key.rotated"}},
		{"exact", "?types=mode.changed", []string{"mode.changed"}},
		{"wildcard", "?types=model.*", []string{"model.loaded"}},
		{"multiple in one parameter", "?types=model.*,mode.changed", []string{"model.loaded", "mode.changed"}},
		{"repeated parameter", "?types=model.*&types=alert.*", []string{"model.loaded", "alert.raised"}},
		{"star", "?types=*", []string{"sync.done", "model.loaded", "mode.changed", "mode.step", "alert.raised", "key.rotated"}},
		{"no match", "?types=cloud.*", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newEventsHarness(t)
			st := openStream(t, h.url+tc.query, nil)
			h.waitSubscribers(t, 1)
			publishAll(t, h)
			var got []string
			for range tc.want {
				got = append(got, st.next(t).event)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events = %v, want %v", got, tc.want)
			}
			st.expectQuiet(t)
		})
	}
}

func TestEventsInvalidTypesIs400(t *testing.T) {
	h := newEventsHarness(t)
	for _, q := range []string{"?types=mod*.loaded", "?types=Model.*", "?types=model%20loaded", "?types=a**"} {
		t.Run(q, func(t *testing.T) {
			req, _ := http.NewRequest("GET", h.url+q, nil)
			req.Header.Set("X-Test-Principal", "bearer")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 400 {
				t.Fatalf("status = %d: %s", resp.StatusCode, body)
			}
			var e errBody
			if err := json.Unmarshal(body, &e); err != nil || e.Error.Code != CodeInvalidRequest || e.Error.Details["param"] != "types" {
				t.Fatalf("body = %s (%v)", body, err)
			}
		})
	}
	if h.bus.Counters().Subscribers != 0 {
		t.Fatal("a refused request must not subscribe")
	}
}

func TestEventsAuth(t *testing.T) {
	h := newEventsHarness(t)
	tests := []struct {
		principal string
		status    int
	}{
		{"bearer", 200}, {"session", 200}, {"socket", 200}, {"admin", 200}, {"portal", 200}, {"", 401},
	}
	for _, tc := range tests {
		t.Run("principal "+tc.principal, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", h.url, nil)
			req.Header.Set("X-Test-Principal", tc.principal)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func TestEventsReplayAndResync(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		lastID   string
		want     []sseFrame // id and event only
	}{
		{"no header: live only", 100, "", nil},
		{"buffered id replays what is missing", 100, "3", []sseFrame{{id: "4", event: "model.loaded"}, {id: "5", event: "model.loaded"}}},
		{"up to date replays nothing", 100, "5", nil},
		{"evicted id: gap first, no id line, then the buffer", 2, "1", []sseFrame{{event: "resync"}, {id: "4", event: "model.loaded"}, {id: "5", event: "model.loaded"}}},
		{"id of a previous process: restart", 100, "42", []sseFrame{{event: "resync"}}},
		{"garbage header is ignored", 100, "banana", nil},
		{"negative header is ignored", 100, "-3", nil},
		{"huge header is ignored", 100, "99999999999999999999999", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h2 := newEventsHarness(t, func(o *EventsOptions) {
				o.Bus = events.New(events.Options{Node: "node-a", Capacity: tc.capacity})
			})
			h2.publishLoaded(t, 5)
			hdr := map[string]string{}
			if tc.lastID != "" {
				hdr["Last-Event-ID"] = tc.lastID
			}
			st := openStream(t, h2.url, hdr)
			var got []sseFrame
			for range tc.want {
				f := st.next(t)
				got = append(got, sseFrame{id: f.id, event: f.event})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("frames = %+v, want %+v", got, tc.want)
			}
			st.expectQuiet(t)
		})
	}
}

func TestEventsResyncReasons(t *testing.T) {
	h := newEventsHarness(t)
	h.publishLoaded(t, 3)
	st := openStream(t, h.url, map[string]string{"Last-Event-ID": "100"})
	f := st.next(t)
	if f.event != "resync" || f.id != "" || f.data != `{"reason":"restart"}` {
		t.Fatalf("frame = %+v", f)
	}
}

func TestEventsReconnectWithLastEventID(t *testing.T) {
	h := newEventsHarness(t)
	st := openStream(t, h.url, nil)
	h.waitSubscribers(t, 1)
	h.publishLoaded(t, 3)
	var last string
	for i := 0; i < 3; i++ {
		last = st.next(t).id
	}
	if last != "3" {
		t.Fatalf("last id = %s", last)
	}
	st.cancel() // the client goes away
	h.waitSubscribers(t, 0)

	h.publishLoaded(t, 4) // missed while disconnected
	st2 := openStream(t, h.url, map[string]string{"Last-Event-ID": last})
	for want := 4; want <= 7; want++ {
		if f := st2.next(t); f.id != fmt.Sprint(want) {
			t.Fatalf("replayed id %s, want %d", f.id, want)
		}
	}
	h.publishLoaded(t, 1) // live after the replay, no gap and no duplicate
	if f := st2.next(t); f.id != "8" {
		t.Fatalf("live id %s, want 8", f.id)
	}
	st2.expectQuiet(t)
}

func TestEventsPortalSeesOnlyItsTypes(t *testing.T) {
	publishEverything := func(t *testing.T, h *eventsHarness) {
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(h.bus.Publish(events.TypeSyncProgress, "", events.SyncProgress{RunID: "r"}))
		must(h.bus.Publish(events.TypeSyncItemDone, "", events.SyncItemDone{RunID: "r"}))
		must(h.bus.Publish(events.TypeSyncDone, "", events.SyncDone{RunID: "r"}))
		must(h.bus.Publish(events.TypeCloudState, "", events.CloudState{State: "linked"}))
		must(h.bus.Publish(events.TypeKeyRotated, "", events.KeyRotated{Fingerprint: "f"}))
		must(h.bus.Publish(events.TypeHardwarePower, "", events.HardwarePower{Source: "ac"}))
		must(h.bus.Publish(events.TypeUpdateState, "", events.UpdateState{State: "idle"}))
		must(h.bus.Publish(events.TypeDrillDone, "", events.DrillDone{RunID: "d"}))
		must(h.bus.Publish(events.TypeModeChanged, "", events.ModeChanged{From: "online", To: "survival"}))
		must(h.bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "m"}))
		must(h.bus.Publish(events.TypeServiceState, "", events.ServiceState{Name: "maps"}))
		must(h.bus.Publish(events.TypeDriveMounted, "", events.DriveMounted{RootID: "r"}))
		must(h.bus.Publish(events.TypeAlertRaised, "", events.Alert{Code: "c"}))
	}
	allowed := []string{"mode.changed", "model.loaded", "service.state", "drive.mounted", "alert.raised"}
	for _, q := range []string{"", "?types=*", "?types=sync.*,cloud.*,key.*,*", "?types=sync.*,mode.*,model.*,service.state,drive.*,alert.*,key.rotated"} {
		t.Run("types"+q, func(t *testing.T) {
			h := newEventsHarness(t)
			st := openStream(t, h.url+q, map[string]string{"X-Test-Principal": "portal"})
			h.waitSubscribers(t, 1)
			publishEverything(t, h)
			var got []string
			for range allowed {
				got = append(got, st.next(t).event)
			}
			if !reflect.DeepEqual(got, allowed) {
				t.Fatalf("portal got %v, want %v", got, allowed)
			}
			st.expectQuiet(t) // nothing else, ever
		})
	}
	t.Run("asking only for forbidden types yields nothing, not an error", func(t *testing.T) {
		h := newEventsHarness(t)
		st := openStream(t, h.url+"?types=sync.*,cloud.*,key.*", map[string]string{"X-Test-Principal": "portal"})
		if st.resp.StatusCode != 200 {
			t.Fatalf("status = %d", st.resp.StatusCode)
		}
		h.waitSubscribers(t, 1)
		publishEverything(t, h)
		st.expectQuiet(t)
	})
	t.Run("an operator is not restricted", func(t *testing.T) {
		h := newEventsHarness(t)
		st := openStream(t, h.url+"?types=sync.*,cloud.*", nil)
		h.waitSubscribers(t, 1)
		publishEverything(t, h)
		var got []string
		for i := 0; i < 4; i++ {
			got = append(got, st.next(t).event)
		}
		if !reflect.DeepEqual(got, []string{"sync.progress", "sync.item_done", "sync.done", "cloud.state"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("replay is restricted too", func(t *testing.T) {
		h := newEventsHarness(t)
		publishEverything(t, h)
		st := openStream(t, h.url, map[string]string{"X-Test-Principal": "portal", "Last-Event-ID": "1"})
		var got []string
		for range allowed {
			got = append(got, st.next(t).event)
		}
		if !reflect.DeepEqual(got, allowed) {
			t.Fatalf("portal replay got %v, want %v", got, allowed)
		}
		st.expectQuiet(t)
	})
}

func TestEventsHeartbeat(t *testing.T) {
	h := newEventsHarness(t)
	st := openStream(t, h.url, nil)
	h.waitSubscribers(t, 1)
	waitTickers(t, h.clock, 1)

	st.expectQuiet(t)
	h.clock.Advance(14 * time.Second)
	st.expectQuiet(t) // 14 s: not yet
	h.clock.Advance(1 * time.Second)
	if f := st.next(t); f.comment != "ping" {
		t.Fatalf("frame = %+v, want a ping comment", f)
	}
	h.clock.Advance(15 * time.Second)
	if f := st.next(t); f.comment != "ping" {
		t.Fatalf("frame = %+v, want a second ping", f)
	}
	// events and pings interleave
	h.publishLoaded(t, 1)
	if f := st.next(t); f.event != "model.loaded" {
		t.Fatalf("frame = %+v", f)
	}
	// the ticker stops with the stream
	st.cancel()
	h.waitSubscribers(t, 0)
	waitTickers(t, h.clock, 0)
}

func waitTickers(t *testing.T, c *fake.Clock, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c.Tickers() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tickers = %d, want %d", c.Tickers(), n)
}

func TestEventsDefaultHeartbeatIs15Seconds(t *testing.T) {
	if EventsHeartbeat != 15*time.Second {
		t.Fatalf("heartbeat = %v", EventsHeartbeat)
	}
	var seen time.Duration
	c := fake.NewClock(eventsEpoch)
	rt, _ := EventsRoute(EventsOptions{Bus: events.New(events.Options{}), Clock: recordingClock{c, &seen}})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { rt.Handler.ServeHTTP(rec, req); close(done) }()
	waitTickers(t, c, 1)
	cancel()
	<-done
	if seen != 15*time.Second {
		t.Fatalf("ticker period = %v, want 15s", seen)
	}
}

type recordingClock struct {
	*fake.Clock
	period *time.Duration
}

func (r recordingClock) NewTicker(d time.Duration) config.Ticker {
	*r.period = d
	return r.Clock.NewTicker(d)
}

func TestEventsClientDisconnectEndsTheStream(t *testing.T) {
	h := newEventsHarness(t)
	base := runtime.NumGoroutine()
	var streams []*sseStream
	for i := 0; i < 5; i++ {
		streams = append(streams, openStream(t, h.url, nil))
	}
	h.waitSubscribers(t, 5)
	h.publishLoaded(t, 3)
	for _, st := range streams[:3] {
		st.cancel() // close the connection mid-stream
	}
	h.waitSubscribers(t, 2)
	for _, st := range streams[3:] {
		st.cancel()
	}
	h.waitSubscribers(t, 0)
	waitTickers(t, h.clock, 0)
	// Server connection goroutines wind down too.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base+4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > base+4 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines = %d after disconnects, baseline %d\n%s", n, base, buf[:runtime.Stack(buf, true)])
	}
	if c := h.bus.Counters(); c.DroppedClients != 0 {
		t.Fatalf("a disconnect is not a slow-client drop: %+v", c)
	}
}

func TestEventsShutdownSendsShutdownThenCloses(t *testing.T) {
	h := newEventsHarness(t)
	var streams []*sseStream
	for i := 0; i < 3; i++ {
		streams = append(streams, openStream(t, h.url, nil))
	}
	h.waitSubscribers(t, 3)
	h.publishLoaded(t, 1)
	for _, st := range streams {
		if f := st.next(t); f.event != "model.loaded" {
			t.Fatalf("frame = %+v", f)
		}
	}

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped <- h.srv.Stop(ctx)
	}()
	for i, st := range streams {
		f := st.next(t)
		if f.event != "shutdown" || f.data != "{}" || f.id != "" {
			t.Fatalf("stream %d: frame = %+v, want event: shutdown with data {}", i, f)
		}
		st.expectEnd(t)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v (open streams must not hold the shutdown up)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return while event streams were open")
	}
	h.waitSubscribers(t, 0)
}

func TestEventsRequestWhileStoppingIs503(t *testing.T) {
	h := newEventsHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	rec := do(h.srv.Handler(), req{target: "/api/v1/events", host: h.srv.TCPAddr().String(), header: map[string]string{"X-Test-Principal": "bearer"}})
	expectError(t, rec, http.StatusServiceUnavailable, CodeStarting)
	if h.bus.Counters().Subscribers != 0 {
		t.Fatal("no subscription may be left behind")
	}
}

func TestEventsRequestAfterBusClosedIs503(t *testing.T) {
	h := newEventsHarness(t)
	h.bus.Close()
	rec := do(h.srv.Handler(), req{target: "/api/v1/events", host: h.srv.TCPAddr().String(), header: map[string]string{"X-Test-Principal": "bearer"}})
	expectError(t, rec, http.StatusServiceUnavailable, CodeStarting)
}

func TestEventsIsNotHeldBackWhileStarting(t *testing.T) {
	// A GET: neither the mutating-route gate nor the /health starting flag applies.
	h := newEventsHarness(t)
	h.srv.SetMutationsOpen(false)
	h.srv.SetStarting(true)
	st := openStream(t, h.url, nil)
	if st.resp.StatusCode != 200 {
		t.Fatalf("status = %d while starting", st.resp.StatusCode)
	}
}

func TestEventsHeadAnswersHeadersOnly(t *testing.T) {
	h := newEventsHarness(t)
	req, _ := http.NewRequest("HEAD", h.url, nil)
	req.Header.Set("X-Test-Principal", "bearer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if h.bus.Counters().Subscribers != 0 {
		t.Fatal("HEAD must not subscribe")
	}
}

func TestEventsStreamSurvivesTheRouteDeadlineAndKeepsConnectionsAlive(t *testing.T) {
	// The stream outlives any per-route deadline: the handler sets none and clears the connection's.
	h := newEventsHarness(t)
	st := openStream(t, h.url, nil)
	h.waitSubscribers(t, 1)
	time.Sleep(300 * time.Millisecond)
	h.publishLoaded(t, 1)
	if f := st.next(t); f.event != "model.loaded" {
		t.Fatalf("frame = %+v", f)
	}
}

// stallWriter is a ResponseWriter whose Write blocks like a socket whose client stopped reading.
type stallWriter struct {
	mu       sync.Mutex
	hdr      http.Header
	deadline time.Time
	release  chan struct{}
	started  chan struct{}
	once     sync.Once
	written  []byte
	flushes  int
}

func newStallWriter() *stallWriter {
	return &stallWriter{hdr: http.Header{}, release: make(chan struct{}), started: make(chan struct{})}
}

func (w *stallWriter) Header() http.Header { return w.hdr }
func (w *stallWriter) WriteHeader(int)     {}
func (w *stallWriter) Flush()              { w.mu.Lock(); w.flushes++; w.mu.Unlock() }

func (w *stallWriter) SetWriteDeadline(t time.Time) error {
	w.mu.Lock()
	w.deadline = t
	w.mu.Unlock()
	return nil
}

var errStalled = errors.New("write deadline exceeded")

func (w *stallWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	w.mu.Lock()
	dl := w.deadline
	w.mu.Unlock()
	var timeout <-chan time.Time
	if !dl.IsZero() {
		timeout = time.After(time.Until(dl))
	}
	select {
	case <-w.release:
		w.mu.Lock()
		w.written = append(w.written, p...)
		w.mu.Unlock()
		return len(p), nil
	case <-timeout:
		return 0, errStalled
	}
}

func runEvents(ctx context.Context, t *testing.T, opts EventsOptions, w http.ResponseWriter) (done chan struct{}) {
	t.Helper()
	rt, err := EventsRoute(opts)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)
	done = make(chan struct{})
	go func() { rt.Handler.ServeHTTP(w, r); close(done) }()
	return done
}

func TestEventsSlowClientIsDroppedAndPublishersNeverBlock(t *testing.T) {
	bus := events.New(events.Options{Node: "node-a"})
	logs := &syncBuf{}
	w := newStallWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runEvents(ctx, t, EventsOptions{
		Bus:          bus,
		Clock:        fake.NewClock(eventsEpoch),
		WriteTimeout: time.Minute, // the bus decides here, not the write deadline
		Logger:       newTestLogger(logs),
	}, w)

	deadline := time.Now().Add(3 * time.Second)
	for bus.Counters().Subscribers != 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	// The first event reaches the handler and blocks in Write (the client stopped reading); 256
	// more fill the queue; the next one is one too many.
	_ = bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "model-a"})
	select {
	case <-w.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler never wrote the first event")
	}
	published := make(chan struct{})
	go func() {
		defer close(published)
		for i := 0; i < 299; i++ {
			_ = bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "model-a", LoadMS: int64(i)})
		}
	}()
	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("a client that stopped reading blocked the publishers")
	}
	if c := bus.Counters(); c.DroppedClients != 1 || c.Subscribers != 0 {
		t.Fatalf("counters = %+v, want the slow client dropped", c)
	}
	close(w.release) // let the blocked Write return: the handler must now notice and end
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the handler of a dropped client did not return")
	}
	if !strings.Contains(logs.String(), "too slow") {
		t.Fatalf("no notice of the dropped client in the log: %s", logs.String())
	}
}

func TestEventsStalledWriteIsCutOffByTheWriteDeadline(t *testing.T) {
	bus := events.New(events.Options{Node: "node-a"})
	w := newStallWriter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runEvents(ctx, t, EventsOptions{
		Bus:          bus,
		Clock:        fake.NewClock(eventsEpoch),
		WriteTimeout: 100 * time.Millisecond,
	}, w)
	deadline := time.Now().Add(3 * time.Second)
	for bus.Counters().Subscribers != 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	_ = bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "model-a"})
	select {
	case <-done: // the write timed out, the handler returned, the subscription ended
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled write held the handler beyond the write timeout")
	}
	deadline = time.Now().Add(3 * time.Second)
	for bus.Counters().Subscribers != 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := bus.Counters().Subscribers; n != 0 {
		t.Fatalf("subscribers = %d after the stalled client was cut off", n)
	}
	if c := bus.Counters(); c.DroppedClients != 0 {
		t.Fatalf("the write deadline, not the bus, ends this one: %+v", c)
	}
}

func TestEventsFiftyConcurrentClients(t *testing.T) {
	h := newEventsHarness(t)
	const clients, total = 50, 192 // 4 publishers x 48; under the 256 queue limit, so nobody may be dropped
	streams := make([]*sseStream, clients)
	for i := range streams {
		streams[i] = openStream(t, h.url, nil)
	}
	h.waitSubscribers(t, clients)

	// Four publishers at once. The total stays under the 256-event queue limit, so no client may be
	// dropped however slowly the race detector runs the 50 connections; the drop policy has its own
	// tests.
	var pw sync.WaitGroup
	for p := 0; p < 4; p++ {
		pw.Add(1)
		go func() {
			defer pw.Done()
			for i := 0; i < total/4; i++ {
				_ = h.bus.Publish(events.TypeModelLoaded, "", events.ModelLoaded{Model: "model-a", LoadMS: int64(i)})
			}
		}()
	}
	var wg sync.WaitGroup
	errs := make(chan string, clients)
	for i, st := range streams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for want := 1; want <= total; want++ {
				select {
				case f, ok := <-st.frames:
					if !ok {
						errs <- fmt.Sprintf("client %d: stream ended at %d", i, want)
						return
					}
					if f.id != fmt.Sprint(want) {
						errs <- fmt.Sprintf("client %d: id %s, want %d", i, f.id, want)
						return
					}
				case <-time.After(5 * time.Second):
					errs <- fmt.Sprintf("client %d: timed out at %d", i, want)
					return
				}
			}
		}()
	}
	pw.Wait()
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if c := h.bus.Counters(); c.DroppedClients != 0 {
		t.Fatalf("counters = %+v", c)
	}
}

func newTestLogger(w io.Writer) *slog.Logger {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	return logx.New(w, lv)
}
