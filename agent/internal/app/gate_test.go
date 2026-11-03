package app

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/state"
)

// While step 12 is held, steps 1 to 11 have run in order, READY=1 has been sent, /health answers with
// starting: true, a mutating route answers 503 LAI-AGT-024 with Retry-After: 5, reads still work and
// the gate is closed. When step 12 ends the gate opens, and step 17 clears starting.
func TestGateIsClosedUntilStep12Ends(t *testing.T) {
	r := newRig(t)
	release := r.steps.holdStep(12)
	d := r.start()

	order := r.steps.began()
	for i, want := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		if i >= len(order) || order[i] != want {
			t.Fatalf("steps began in the order %v, want 1 to 11 first", order)
		}
	}
	if got := r.note.next(t); got != "READY=1" {
		t.Fatalf("first sd_notify message = %q, want READY=1 (it does not wait for step 12)", got)
	}
	if !strings.HasPrefix(r.token(), "lat_") {
		t.Error("the agent token was not created")
	}
	r.requireAlert(doctor.SEC011)
	r.requireNoAlert(doctor.AGT011)

	h := r.doAuth("GET", "/health", nil, false)
	if h.status != 200 || h.json()["ok"] != true || h.json()["starting"] != true || h.json()["mode"] != "online" {
		t.Fatalf("GET /health during startup = %d %s", h.status, h.body)
	}
	load := r.do("POST", "/models/"+modelAlpha+"/load", map[string]any{"wait": true})
	if load.status != http.StatusServiceUnavailable || errCode(load) != doctor.AGT024 || load.header.Get("Retry-After") != "5" {
		t.Fatalf("a mutating route during startup = %d %s (Retry-After %q), want 503 LAI-AGT-024 with Retry-After 5",
			load.status, load.body, load.header.Get("Retry-After"))
	}
	if st := r.do("GET", "/status", nil); st.status != 200 {
		t.Errorf("GET /status during startup = %d", st.status)
	}
	select {
	case <-d.GateOpened():
		t.Fatal("the gate opened while step 12 was held")
	case <-d.Started():
		t.Fatal("step 17 ran while step 12 was held")
	default:
	}

	release()
	r.waitStarted()
	select {
	case <-d.GateOpened():
	default:
		t.Error("the gate did not open when step 12 ended")
	}
	if h := r.doAuth("GET", "/health", nil, false); h.json()["starting"] != false {
		t.Errorf("GET /health after startup: %s", h.body)
	}
	r.waitStepEnd(14) // the restore holds the model-operations lock: a load meanwhile is 409 LAI-AGT-022
	if res := r.do("POST", "/models/"+modelGamma+"/load", map[string]any{"wait": true, "unload_others": true}); res.status != 200 {
		t.Errorf("a load after the gate opened = %d %s", res.status, res.body)
	}
	// The gate opens when step 12 ends, success or failure: a reconcile that fails still opens it.
	if st := d.Components().State.Snapshot(); st.Mode != state.ModeOnline {
		t.Errorf("mode = %q", st.Mode)
	}
}

// A reconcile that fails (the unit directory is missing) still opens the gate (agent.md section 4.1
// step 12: "success or failure").
func TestGateOpensWhenTheReconcileFails(t *testing.T) {
	r := newRig(t, func(r *rig) {
		// A file where the systemd unit directory should be: the unit cannot be written.
		_ = os.RemoveAll(r.path("etc/systemd"))
		r.write("etc/systemd", "not a directory", 0o644)
	})
	d := r.start()
	r.waitStarted()
	select {
	case <-d.GateOpened():
	default:
		t.Fatal("the gate stayed closed after a failed reconcile")
	}
	var s12 *StepResult
	for _, s := range d.Steps() {
		if s.N == 12 {
			s := s
			s12 = &s
		}
	}
	if s12 == nil || s12.Err == nil {
		t.Errorf("step 12 should have recorded the failure: %+v", s12)
	}
	if h := r.doAuth("GET", "/health", nil, false); h.status != 200 {
		t.Errorf("GET /health = %d", h.status)
	}
	if res := r.do("POST", "/models/"+modelAlpha+"/unload", map[string]any{}); res.status == http.StatusServiceUnavailable {
		t.Errorf("a mutating route is still refused: %s", res.body)
	}
}
