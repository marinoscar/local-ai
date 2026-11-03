//go:build integration

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	routerfake "github.com/marinoscar/local-ai/agent/internal/router/fake"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// The integration scenarios of agent.md section 9.2 that the wiring of this issue can prove: the real
// startup, over the real HTTP stack, against the fakes of the rig. Run them with
//
//	go test -race -tags integration ./internal/app/...

// waitEvent waits for an event of the bus that satisfies pred. The wait ends when the event is
// published (the subscription is a channel), never after a fixed time.
func (r *rig) waitEvent(pattern string, pred func(events.Event) bool) events.Event {
	r.t.Helper()
	f, err := events.NewFilter(pattern)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch, err := r.d.Components().Bus.Subscribe(ctx, f, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				r.t.Fatalf("the event stream ended before the event %s", pattern)
			}
			if pred(ev) {
				return ev
			}
		case <-ctx.Done():
			r.t.Fatalf("no event %s satisfied the wait; log:\n%s", pattern, r.logText())
		}
	}
}

func modeStepEvent(step, st string) func(events.Event) bool {
	return func(ev events.Event) bool {
		var d events.ModeStep
		return json.Unmarshal(ev.Data, &d) == nil && d.Step == step && d.State == st
	}
}

// Scenario 1: a fresh start. The token is created, the listeners are up, /health answers, the mutating
// routes are closed (503 LAI-AGT-024, Retry-After 5) until step 12 has ended, READY=1 does not wait
// for it, and then the models are restored.
func TestIntegrationFreshStart(t *testing.T) {
	r := newRig(t)
	release := r.steps.holdStep(12)
	d := r.start()

	// Steps 1 to 11 have run and step 12 is held: the daemon is up, but not open.
	order := r.steps.began()
	for i, want := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		if i >= len(order) || order[i] != want {
			t.Fatalf("steps began in the order %v, want 1..11 first", order)
		}
	}
	if got := r.note.next(t); got != "READY=1" {
		t.Fatalf("first sd_notify message = %q, want READY=1 (it must not wait for step 12)", got)
	}
	tok := r.token()
	if !strings.HasPrefix(tok, "lat_") {
		t.Errorf("the agent token %q was not created", tok)
	}
	if fi, err := os.Stat(r.path("etc/localai/agent-token")); err != nil || fi.Mode().Perm()&0o007 != 0 {
		t.Errorf("agent-token mode: %v %v", fi, err)
	}
	if !hasAlert(d, "LAI-SEC-011") {
		t.Errorf("a new agent token must raise LAI-SEC-011; alerts: %+v", d.Components().Doctor.Active())
	}

	h := r.doAuth("GET", "/health", nil, false)
	if h.status != 200 || h.json()["ok"] != true || h.json()["starting"] != true || h.json()["mode"] != "online" {
		t.Fatalf("GET /health during startup = %d %s", h.status, h.body)
	}
	load := r.do("POST", "/models/"+modelAlpha+"/load", map[string]any{"wait": true})
	if load.status != http.StatusServiceUnavailable || errCode(load) != doctor.AGT024 || load.header.Get("Retry-After") != "5" {
		t.Fatalf("a mutating route during startup = %d %s (Retry-After %q), want 503 LAI-AGT-024 with Retry-After 5",
			load.status, load.body, load.header.Get("Retry-After"))
	}
	select {
	case <-d.GateOpened():
		t.Fatal("the gate opened while step 12 was held")
	default:
	}
	if st := r.do("GET", "/status", nil); st.status != 200 {
		t.Errorf("GET /status during startup = %d", st.status)
	}

	release()
	r.waitStarted()
	h = r.doAuth("GET", "/health", nil, false)
	if h.json()["starting"] != false {
		t.Errorf("GET /health after startup: %s", h.body)
	}

	// The default model is restored (step 14); a load through the API now works.
	r.waitEvent("model.loaded", func(ev events.Event) bool { return strings.Contains(string(ev.Data), modelAlpha) })
	if got := r.llama.Status(modelAlpha); got != routerfake.Loaded {
		t.Errorf("%s is %q after the restore, want loaded", modelAlpha, got)
	}
	res := r.do("POST", "/models/"+modelGamma+"/load", map[string]any{"wait": true, "unload_others": true})
	if res.status != 200 {
		t.Fatalf("POST /models/%s/load = %d %s", modelGamma, res.status, res.body)
	}
	if r.llama.Status(modelGamma) != routerfake.Loaded || r.llama.Status(modelAlpha) == routerfake.Loaded {
		t.Errorf("after the load: gamma %q, alpha %q", r.llama.Status(modelGamma), r.llama.Status(modelAlpha))
	}

	// The messages of systemd: READY once, then the watchdog is not needed yet.
	for _, m := range d.env.Notifier.Sent() {
		if m == "READY=1" {
			continue
		}
		if m == "STOPPING=1" {
			t.Error("STOPPING=1 before the shutdown")
		}
	}

	// A mode transition over the real stack: online to offline and back.
	mt := r.do("POST", "/mode", map[string]any{"target": "offline", "wait": true})
	if mt.status != 200 {
		t.Fatalf("POST /mode offline = %d %s", mt.status, mt.body)
	}
	if got := r.do("GET", "/mode", nil).json()["mode"]; got != "offline" {
		t.Errorf("GET /mode = %v, want offline", got)
	}
	if got := r.d.Components().State.Snapshot(); got.Mode != state.ModeOffline || got.ModeTransition != nil {
		t.Errorf("state after the transition: mode %q, transition %v", got.Mode, got.ModeTransition)
	}

	// Shutdown: STOPPING=1, the listeners go, llama is not touched.
	rep := d.Shutdown()
	if !r.sawMessage("STOPPING=1") {
		t.Errorf("sd_notify messages %v lack STOPPING=1", d.env.Notifier.Sent())
	}
	if rep.Duration >= ShutdownBudget {
		t.Errorf("the shutdown took %v", rep.Duration)
	}
	if _, err := os.Stat(r.d.Components().API.SocketPath()); err == nil {
		t.Error("the socket file is still there after the shutdown")
	}
	for _, c := range r.sysd.Calls() {
		if c.Unit == services.UnitLlama && (c.Verb == services.VerbStop || c.Verb == services.VerbRestart) {
			t.Errorf("shutdown touched llama: %+v", c)
		}
	}
	if r.llama.Status(modelGamma) != routerfake.Loaded {
		t.Errorf("llama stopped serving %s during the agent's shutdown", modelGamma)
	}
}

func (r *rig) sawMessage(m string) bool {
	for _, got := range r.d.env.Notifier.Sent() {
		if got == m {
			return true
		}
	}
	return false
}

// errCode is details.code of the error envelope, or the code field of its body.
func errCode(rp reply) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rp.body, &e)
	return e.Error.Code
}

// An agent restart restores the models the operator had chosen: the default is set to a model other
// than the profile's, the router and the agent both restart, and the restart loads that model.
func TestIntegrationRestartRestoresModels(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	r.waitEvent("model.loaded", func(ev events.Event) bool { return strings.Contains(string(ev.Data), modelAlpha) })

	if res := r.do("POST", "/models/"+modelGamma+"/load", map[string]any{"wait": true, "unload_others": true}); res.status != 200 {
		t.Fatalf("load gamma: %d %s", res.status, res.body)
	}
	if res := r.do("PUT", "/models/default", map[string]any{"id": modelGamma}); res.status != 200 {
		t.Fatalf("set the default: %d %s", res.status, res.body)
	}
	r.d.Shutdown()

	// The machine restarts: llama too, so nothing is loaded when the new agent comes up.
	r.llama.Restart()
	if r.llama.Status(modelGamma) == routerfake.Loaded {
		t.Fatal("the fake did not restart")
	}
	r2 := *r
	r2.logs = &syncBuf{}
	r2.steps = &stepLog{}
	r2.note = newNotifyListener(t, false)
	r2.env.Stdout, r2.env.OnStep, r2.env.Notifier = r2.logs, r2.steps.onStep, NewNotifier(r2.note.name)
	r2.env.SocketPath = filepath.Join(r.root, "run", "localai", "agent2.sock")
	r2.start()
	r2.waitStarted()
	r2.waitEvent("model.loaded", func(ev events.Event) bool { return strings.Contains(string(ev.Data), modelGamma) })
	if r.llama.Status(modelGamma) != routerfake.Loaded || r.llama.Status(modelAlpha) == routerfake.Loaded {
		t.Errorf("after the restart: gamma %q, alpha %q", r.llama.Status(modelGamma), r.llama.Status(modelAlpha))
	}
	if st := r2.d.Components().State.Snapshot(); st.Models.Default == nil || *st.Models.Default != modelGamma {
		t.Errorf("the restart lost the operator's default model: %v", st.Models.Default)
	}
}

// A survival request that arrives during startup is queued: persisted as state.pending_mode, answered
// 202 with queued: true, and run when step 12 has ended.
func TestIntegrationSurvivalQueuedDuringStartup(t *testing.T) {
	r := newRig(t)
	release := r.steps.holdStep(12)
	d := r.start()
	rp := r.doAuth("POST", "/mode/survival", map[string]any{}, false)
	if rp.status != http.StatusAccepted || rp.json()["queued"] != true || rp.json()["target"] != "survival" || rp.json()["op_id"] == "" {
		t.Fatalf("POST /mode/survival during startup = %d %s, want 202 queued", rp.status, rp.body)
	}
	if pm := d.Components().State.Snapshot().PendingMode; pm == nil || *pm != state.ModeSurvival {
		t.Fatalf("pending_mode = %v, want survival", pm)
	}
	release()
	r.waitStarted()
	r.waitEvent("mode.changed", func(ev events.Event) bool { return strings.Contains(string(ev.Data), `"survival"`) })
	snap := d.Components().State.Snapshot()
	if snap.Mode != state.ModeSurvival || snap.PendingMode != nil || snap.ModeTransition != nil {
		t.Errorf("state after the queued transition: mode %q pending %v transition %v", snap.Mode, snap.PendingMode, snap.ModeTransition)
	}
}

// Scenario 3: the agent is killed in the middle of survival_models and restarts: the transition
// resumes at the recorded step and completes. The kill is the in-process equivalent of kill -9: the
// first agent's state filesystem dies (every later write fails and is lost), its router stops
// answering, and a second agent starts on a copy of what was durable.
func TestIntegrationKillDuringSurvivalModelsResumes(t *testing.T) {
	fsA := statefake.New()
	r := newRig(t, withEnv(func(e *Env) { e.StateFS = fsA }))
	r.start()
	r.waitStarted()
	r.waitEvent("model.loaded", func(ev events.Event) bool { return strings.Contains(string(ev.Data), modelAlpha) })

	// Park the transition in survival_models: the router answers nothing from now on.
	r.llama.Delay(time.Hour)
	rp := r.doAuth("POST", "/mode/survival", map[string]any{}, false)
	if rp.status != http.StatusAccepted {
		t.Fatalf("POST /mode/survival = %d %s", rp.status, rp.body)
	}
	r.waitEvent("mode.step", modeStepEvent("survival_models", "started"))

	// kill -9: nothing written from here on survives.
	fsA.CrashAfter(0)
	var durable map[string][]byte = map[string][]byte{}
	for _, name := range fsA.Names(filepath.Dir(r.path("var/lib/localai/state.json"))) {
		if b, ok := fsA.Contents(filepath.Join(filepath.Dir(r.path("var/lib/localai/state.json")), name)); ok {
			durable[name] = b
		}
	}
	var persisted state.State
	if err := json.Unmarshal(durable["state.json"], &persisted); err != nil || persisted.ModeTransition == nil ||
		persisted.ModeTransition.Step != "survival_models" || persisted.ModeTransition.Target != state.ModeSurvival {
		t.Fatalf("the durable state does not hold the recorded step: %v %+v", err, persisted.ModeTransition)
	}

	// The node comes back: a fresh llama (nothing loaded) and a new agent on the durable state.
	llama2 := routerfake.New(r.key)
	t.Cleanup(llama2.Close)
	for _, id := range []string{modelAlpha, modelGamma} {
		llama2.Add(routerfake.Model{ID: id, Source: "preset"})
	}
	fsB := statefake.New()
	for name, b := range durable {
		fsB.Seed(filepath.Join(filepath.Dir(r.path("var/lib/localai/state.json")), name), b)
	}
	r.writeNodeYAMLText(r.nodeYAMLFor(llama2))
	b := *r
	b.llama, b.logs, b.steps, b.note = llama2, &syncBuf{}, &stepLog{}, newNotifyListener(t, false)
	b.env.Stdout, b.env.OnStep, b.env.Notifier = b.logs, b.steps.onStep, NewNotifier(b.note.name)
	b.env.StateFS = fsB
	b.env.SocketPath = filepath.Join(r.root, "run", "localai", "agent-b.sock")
	b.start()
	b.waitStarted()
	b.waitEvent("mode.changed", func(ev events.Event) bool { return strings.Contains(string(ev.Data), `"survival"`) })

	snap := b.d.Components().State.Snapshot()
	if snap.Mode != state.ModeSurvival || snap.ModeTransition != nil {
		t.Errorf("after the resume: mode %q, transition %+v", snap.Mode, snap.ModeTransition)
	}
	if llama2.Status(modelAlpha) != routerfake.Loaded {
		t.Errorf("the resumed survival transition did not load %s: %q", modelAlpha, llama2.Status(modelAlpha))
	}
	if !b.logs.has("resuming a mode transition", nil) && !strings.Contains(b.logText(), doctor.AGT007) {
		t.Errorf("the resume is not logged (LAI-AGT-007):\n%s", b.logText())
	}
}

func portOf(u string) string { return u[strings.LastIndex(u, ":")+1:] }

func (r *rig) nodeYAMLFor(l *routerfake.Router) string {
	return strings.ReplaceAll(r.nodeYAML(), "llama: "+r.llamaPort(), "llama: "+portOf(l.URL()))
}
