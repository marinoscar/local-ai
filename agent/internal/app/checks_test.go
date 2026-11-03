package app

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/drill"
)

// Every doctor check the packages ship is registered, once, in the daemon's own registry (never the
// package-level DefaultRegistry, so two daemons in one process do not collide).
func TestDoctorChecksAreRegistered(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	got := map[string]bool{}
	for _, c := range d.Components().Checks.Checks() {
		got[c.ID] = true
	}
	var missing []string
	for _, want := range []string{
		doctor.CheckAgtConfig, doctor.CheckAgtStateWritable, doctor.CheckAgtStateIntact, doctor.CheckAgtAuditWritable,
		"net.serve_state", "svc.health", "svc.deep", "svc.kiwix_installed", drill.CheckLastResult, drill.CheckFreshness,
	} {
		if !got[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		var all []string
		for id := range got {
			all = append(all, id)
		}
		sort.Strings(all)
		t.Errorf("checks not registered: %v (registered: %v)", missing, all)
	}
	if len(doctor.DefaultRegistry.Checks()) != 0 {
		t.Errorf("the daemon registered checks in the global registry: %d", len(doctor.DefaultRegistry.Checks()))
	}
}

// The drill reporter stamps drill.watch_since on a node that never ran a drill, so the 45-day alert
// has an anchor; a later start keeps it.
func TestDrillReporterAnchorsTheFreshnessAlert(t *testing.T) {
	r := newRig(t)
	d := r.start()
	r.waitStarted()
	read := func() string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(d.Components().State.Snapshot().Drill, &m); err != nil {
			t.Fatalf("state.drill: %v (%s)", err, d.Components().State.Snapshot().Drill)
		}
		return string(m["watch_since"])
	}
	first := read()
	if first == "" {
		t.Fatal("drill.watch_since was not stamped at the first start")
	}
	d.Shutdown()

	r2 := *r
	r2.logs, r2.steps = &syncBuf{}, &stepLog{}
	r2.note = newNotifyListener(t, false)
	r2.env.Stdout, r2.env.OnStep, r2.env.Notifier = r2.logs, r2.steps.onStep, NewNotifier(r2.note.name)
	r2.env.SocketPath = r.path("run/localai/agent2.sock")
	d2 := r2.start()
	r2.waitStarted()
	var m map[string]json.RawMessage
	_ = json.Unmarshal(d2.Components().State.Snapshot().Drill, &m)
	if string(m["watch_since"]) != first {
		t.Errorf("the anchor moved: %s then %s", first, m["watch_since"])
	}
	// No alert for a node that has been watched for a second.
	if hasAlert(d2, doctor.DRL006) {
		t.Error("LAI-DRL-006 on a node that has just been anchored")
	}
}
