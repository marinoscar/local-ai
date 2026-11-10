package app

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"encoding/json"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/ensure"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"net/http"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// The routes of the ensure engine over the daemon (agent.md section 3.8.33). What the engine decides is
// proved in internal/ensure; here the wiring: the routes are served, authenticated, and answer the shape of
// the contract on a node whose profile wants nothing the rig lacks.
func TestEnsureRoutesAreServedOverTheDaemon(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()

	t.Run("GET is the picture", func(t *testing.T) {
		rp := r.do(http.MethodGet, "/ensure", nil)
		if rp.status != http.StatusOK {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
		m := rp.json()
		for _, k := range []string{"state", "auto", "message", "next_action", "progress", "steps", "run", "dry_run", "checked_at"} {
			if _, ok := m[k]; !ok {
				t.Errorf("no %q in %s", k, rp.body)
			}
		}
		if m["auto"] != "on" {
			t.Errorf("auto %v: the default is on", m["auto"])
		}
		if _, ok := m["steps"].([]any); !ok {
			t.Errorf("steps is %T, want a list even when empty", m["steps"])
		}
	})
	t.Run("Spanish is chosen by Accept-Language", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, r.base()+"/ensure", nil)
		req.Header.Set("Authorization", "Bearer "+r.token())
		req.Header.Set("Accept-Language", "es")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := jsonDecode(resp, &m); err != nil {
			t.Fatal(err)
		}
		if msg, _ := m["message"].(string); !strings.HasPrefix(msg, "Completo") && !strings.HasPrefix(msg, "Necesita") && !strings.HasPrefix(msg, "Instalando") {
			t.Errorf("message %q", msg)
		}
	})
	t.Run("a dry run is 200 and changes nothing", func(t *testing.T) {
		rp := r.do(http.MethodPost, "/ensure", map[string]any{"dry_run": true})
		if rp.status != http.StatusOK || rp.json()["dry_run"] != true {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
	})
	t.Run("an empty body is a start; nothing to do is 200", func(t *testing.T) {
		rp := r.doAuth(http.MethodPost, "/ensure", nil, true)
		if rp.status != http.StatusOK && rp.status != http.StatusAccepted {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
	})
	t.Run("a confirmation needs admin*: an operator with a token is refused", func(t *testing.T) {
		rp := r.do(http.MethodPost, "/ensure", map[string]any{"confirm": []string{"whisper.build"}})
		if rp.status != http.StatusForbidden || errCode(rp) != doctor.SEC002 {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
		details := rp.json()["error"].(map[string]any)["details"].(map[string]any)
		if details["pin_required"] != true || details["reason"] != "confirm" {
			t.Errorf("details %v", details)
		}
	})
	t.Run("a dry run with a confirmation needs no PIN, and a confirmation of nothing waiting is 400", func(t *testing.T) {
		rp := r.do(http.MethodPost, "/ensure", map[string]any{"dry_run": true, "confirm": []string{"whisper.build"}})
		if rp.status != http.StatusBadRequest || errCode(rp) != doctor.AGT020 {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
		d := rp.json()["error"].(map[string]any)["details"].(map[string]any)
		if d["field"] != "confirm" || d["reason"] != "not_waiting" || d["step"] != "whisper.build" {
			t.Errorf("details %v", d)
		}
	})
	t.Run("a made-up step id and a bad body are 400", func(t *testing.T) {
		for _, body := range []map[string]any{{"dry_run": true, "confirm": []string{"rm -rf"}}, {"nope": 1}, {"confirm": []string{""}}} {
			if rp := r.do(http.MethodPost, "/ensure", body); rp.status != http.StatusBadRequest {
				t.Errorf("%v: %d %s", body, rp.status, rp.body)
			}
		}
	})
	t.Run("DELETE with nothing running is 200", func(t *testing.T) {
		rp := r.do(http.MethodDelete, "/ensure", nil)
		if rp.status != http.StatusOK {
			t.Fatalf("%d %s", rp.status, rp.body)
		}
	})
	t.Run("no token, no answer", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			if rp := r.doAuth(m, "/ensure", nil, false); rp.status != http.StatusUnauthorized {
				t.Errorf("%s: %d", m, rp.status)
			}
		}
	})
}

// The worker turns the events of the bus into passes, after their delays. The engine here wants nothing, so a
// pass only refreshes the doctor alerts: each refresh is a pass that ran.
func TestEnsureWorkerTurnsEventsIntoPasses(t *testing.T) {
	delays := ensureDelays
	ensureDelays.Start, ensureDelays.Sync, ensureDelays.Online, ensureDelays.Cfg = 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { ensureDelays = delays })

	var mu sync.Mutex
	passes := 0
	clock := &testClock{t: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)}
	j, _, err := ensure.OpenJournal(nil, filepath.Join(t.TempDir(), "j.json"), clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	svc := ensure.New(ensure.Deps{
		Settings: ensure.DefaultSettings,
		Wants:    func(context.Context) (ensure.Wants, error) { return ensure.Wants{}, nil },
		Planner:  &ensure.Planner{Clock: clock},
		Executor: &ensure.Executor{Clock: clock},
		Journal:  j, Clock: clock, PlanTTL: time.Nanosecond,
		Alert: func(code string, raise bool, _ map[string]any) {
			if code == doctor.KIT051 {
				mu.Lock()
				passes++
				mu.Unlock()
			}
		},
	})
	bus := events.New(events.Options{})
	d := &Daemon{c: &Components{Bus: bus}}
	w := &ensureWorker{s: &ensureStack{d: d, svc: svc}}
	if err := w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer w.Stop(context.Background())
	count := func() int { mu.Lock(); defer mu.Unlock(); return passes }
	wait := func(n int, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for count() < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if count() != n {
			t.Fatalf("%s: %d passes, want %d", what, count(), n)
		}
	}
	steady := func(n int, what string) {
		t.Helper()
		time.Sleep(80 * time.Millisecond)
		if count() != n {
			t.Fatalf("%s: %d passes, want still %d", what, count(), n)
		}
	}
	wait(1, "the start of the agent")
	publish := func(typ string, data any) {
		t.Helper()
		if err := bus.Publish(typ, "", data); err != nil {
			t.Fatal(err)
		}
	}
	publish(events.TypeSyncDone, events.SyncDone{RunID: "run-1", State: "skipped", Totals: json.RawMessage("{}"), Ready: map[string]string{}})
	steady(1, "a skipped sync run changed nothing")
	publish(events.TypeSyncDone, events.SyncDone{RunID: "run-2", State: "done", Totals: json.RawMessage("{}"), Ready: map[string]string{}})
	wait(2, "a sync run that ended")
	publish(events.TypeModeChanged, events.ModeChanged{From: "online", To: "offline", OpID: "op_1", Warnings: []events.Warning{}})
	steady(2, "going offline")
	publish(events.TypeModeChanged, events.ModeChanged{From: "offline", To: "online", OpID: "op_2", Warnings: []events.Warning{}})
	wait(3, "mode_online")
	ensureStackOnConfig(t, w.s, count, wait)
}

// ensureStackOnConfig: a change of the ensure block asks for a pass; a change elsewhere does not.
func ensureStackOnConfig(t *testing.T, s *ensureStack, count func() int, wait func(int, string)) {
	t.Helper()
	s.onConfigChange(config.Change{Changed: []string{"logging.level"}})
	time.Sleep(80 * time.Millisecond)
	if count() != 3 {
		t.Fatalf("a change of logging.level ran a pass: %d", count())
	}
	s.onConfigChange(config.Change{Changed: []string{"ensure.auto"}})
	wait(4, "a change of ensure.auto")
}

func jsonDecode(resp *http.Response, v any) error { return json.NewDecoder(resp.Body).Decode(v) }

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}
