package cloud

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

// heartbeatBodyOf decodes the nth heartbeat the fake received (0-based).
func heartbeatBodyOf(t *testing.T, r *rig, n int) map[string]any {
	t.Helper()
	hbs := r.srv.RequestsFor(fake.RouteHeartbeat)
	if n >= len(hbs) {
		t.Fatalf("only %d heartbeats", len(hbs))
	}
	var m map[string]any
	if err := hbs[n].JSON(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestHeartbeatCadenceIs15SecondsOnTheFakeClock(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	for i := 2; i <= 4; i++ {
		r.step(15 * time.Second)
		r.waitCount(fake.RouteHeartbeat, i)
	}
	// Nothing is sent early: 14 s later no timer was due, so the count did not move; 1 s more and it does.
	r.waitTimers(2) // the heartbeat's and the state loop's
	n := r.srv.Count(fake.RouteHeartbeat)
	r.clk.Advance(14 * time.Second)
	if r.srv.Count(fake.RouteHeartbeat) != n {
		t.Error("a heartbeat was sent before the interval")
	}
	r.step(time.Second)
	r.waitCount(fake.RouteHeartbeat, n+1)
}

func TestHeartbeatIntervalFollowsTheHandshake(t *testing.T) {
	r := newRig(t)
	r.srv.Handshake.HeartbeatIntervalS = 30
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.waitTimers(2)
	r.clk.Advance(15 * time.Second)
	if r.srv.Count(fake.RouteHeartbeat) != 1 {
		t.Fatal("heartbeat at 15 s although the cloud asked for 30 s")
	}
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 2)
}

func TestHeartbeatBodyHasSixteenExtraKeysAndTheFixedShape(t *testing.T) {
	r := newRig(t)
	r.status.edit(func(s *statusapiStatus) {
		s.Ark.Sync.LastSuccessAt = str("2026-11-02T03:00:00Z")
		s.Ark.Drive.Mounted = false
	})
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	body := heartbeatBodyOf(t, r, 0)
	if body["status"] != "online" || body["concurrency"] != float64(1) {
		t.Fatalf("body %v", body)
	}
	vitals := body["vitals"].(map[string]any)
	for _, k := range []string{"cpuPercent", "rssBytes", "stateDirFreeBytes", "stateDirTotalBytes", "slotsUsed", "slotsTotal", "uptimeSeconds", "cliVersion", "counters", "extra"} {
		if _, ok := vitals[k]; !ok {
			t.Errorf("vitals.%s missing", k)
		}
	}
	extra := vitals["extra"].(map[string]any)
	if len(extra) != 16 {
		t.Fatalf("extra has %d keys: %v", len(extra), extra)
	}
	re := regexp.MustCompile(`^[a-z][a-z0-9_.]{1,47}$`)
	for k, v := range extra {
		if !re.MatchString(k) {
			t.Errorf("key %q breaks the EAB pattern", k)
		}
		if v == nil {
			t.Errorf("%s is null", k)
		}
	}
	want := map[string]float64{
		"localai.models_loaded": 1, "localai.vram_used_mib": 6791, "localai.vram_total_mib": 8192, "localai.sync_running": 0,
		"localai.alerts_active": 1, "localai.gpu_temp_c": 61, "localai.gpu_util_pct": 3, "localai.cpu_temp_c": 72,
		"localai.ram_available_mib": 5120, "localai.models_loadable": 4, "localai.llama_up": 1, "localai.ark_internal_free_gib": 273,
		"localai.ark_drive_mounted": 0, "localai.sync_age_h": 12, "localai.alerts_critical": 0, "localai.catalog_version": 20261102.1,
	}
	for k, v := range want {
		if extra[k] != v {
			t.Errorf("%s = %v, want %v", k, extra[k], v)
		}
	}
	if vitals["cliVersion"] != "aiagent/0.1.0" || vitals["slotsTotal"] != float64(1) {
		t.Errorf("vitals %v", vitals)
	}
	caps := body["capabilities"].(map[string]any)["localai"].(map[string]any)
	if caps["profile"] != "small-8gb" || caps["serve_pattern"] != "none" || caps["backend"] != "cuda" {
		t.Errorf("capabilities %v", caps)
	}
	if _, ok := caps["state"]; ok {
		t.Error("the reduced state is sent although the cloud has a state endpoint")
	}
}

func TestHeartbeatSendsHostPowerAndEventsOnlyAfterNodeHostVitals(t *testing.T) {
	r := newRig(t)
	r.srv.ServerFeatures = nil // register answers no node_host_vitals
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	first := heartbeatBodyOf(t, r, 0)
	v := first["vitals"].(map[string]any)
	if _, ok := v["host"]; ok {
		t.Error("host sent before node_host_vitals")
	}
	if _, ok := v["power"]; ok {
		t.Error("power sent before node_host_vitals")
	}
	if _, ok := first["events"]; ok {
		t.Error("events sent before node_host_vitals")
	}

	// The cloud advertises the feature in a heartbeat answer; the next heartbeat carries host and power.
	r.srv.Locked(func() { r.srv.ServerFeatures = []string{"node_host_vitals"} })
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 2)
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 3)
	third := heartbeatBodyOf(t, r, 2)["vitals"].(map[string]any)
	host, ok := third["host"].(map[string]any)
	if !ok || host["cpuCount"] != float64(12) || host["osRelease"] != "Test OS 1.0" || host["arch"] != "x86_64" {
		t.Fatalf("host %v", third["host"])
	}
	power, ok := third["power"].(map[string]any)
	if !ok || power["source"] != "ac" || power["batteryPercent"] != float64(100) || power["charging"] != false {
		t.Fatalf("power %v", third["power"])
	}

	// A power change is an event of the next heartbeat.
	snap := baseHardware()
	snap.Power.Source = hardware.SourceBattery
	r.hw.set(snap)
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 4)
	evs, ok := heartbeatBodyOf(t, r, 3)["events"].([]any)
	if !ok || len(evs) != 1 {
		t.Fatalf("events %v", heartbeatBodyOf(t, r, 3)["events"])
	}
	ev := evs[0].(map[string]any)
	if ev["type"] != "power.source_changed" || ev["severity"] != "info" || ev["attrs"].(map[string]any)["source"] != "battery" {
		t.Errorf("event %v", ev)
	}
}

func TestHeartbeatCarriesOurEventsFromTheBus(t *testing.T) {
	r := newRig(t)
	quickState(r)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.waitCount(fake.RouteState, 1)
	r.waitTimers(2)
	_ = r.alerts.Raise(doctor.HW005, nil)
	// The event feed queues the event and then wakes the state loop, whose debounce timer (5 s) is the
	// proof that the event is queued.
	if err := r.clk.WaitDue(r.ctx, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 2)
	evs, _ := heartbeatBodyOf(t, r, 1)["events"].([]any)
	found := false
	for _, e := range evs {
		m := e.(map[string]any)
		if m["type"] == "localai.alert.raised" && m["attrs"].(map[string]any)["code"] == doctor.HW005 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no alert event in %v", evs)
	}
}

func TestHeartbeatRequeuesEventsOfAFailedHeartbeat(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	snap := baseHardware()
	snap.Power.Source = hardware.SourceBattery
	r.hw.set(snap)
	r.srv.Script(fake.RouteHeartbeat, fake.Reply{Drop: true})
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 2) // dropped: the event goes back to the queue
	r.step(BackoffMin)
	r.waitCount(fake.RouteHeartbeat, 3)
	evs, _ := heartbeatBodyOf(t, r, 2)["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["type"] != "power.source_changed" {
		t.Fatalf("events after the failure: %v", evs)
	}
}

func TestHeartbeat400MentioningExtraDropsExtraForTheProcessLife(t *testing.T) {
	r := newRig(t)
	r.srv.RejectExtra = true
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 2) // the refused one and the immediate resend
	hbs := r.srv.RequestsFor(fake.RouteHeartbeat)
	var first, second map[string]any
	_ = hbs[0].JSON(&first)
	_ = hbs[1].JSON(&second)
	if _, ok := first["vitals"].(map[string]any)["extra"]; !ok {
		t.Fatal("the first heartbeat carried no extra")
	}
	v2 := second["vitals"].(map[string]any)
	if _, ok := v2["extra"]; ok || v2["cpuPercent"] == nil {
		t.Fatalf("the resend must keep vitals and drop extra: %v", v2)
	}
	if !r.alertActive(doctor.CLD055) {
		t.Error("LAI-CLD-055 not raised")
	}
	// Later heartbeats never send extra again.
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 3)
	var third map[string]any
	_ = r.srv.RequestsFor(fake.RouteHeartbeat)[2].JSON(&third)
	if _, ok := third["vitals"].(map[string]any)["extra"]; ok {
		t.Error("extra came back")
	}
	if r.link.State() != StateLinked {
		t.Errorf("state %s", r.link.State())
	}
}

func TestHeartbeatOther400DropsVitalsForTheProcessLife(t *testing.T) {
	r := newRig(t)
	r.srv.RejectVitals = true
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 2)
	var second map[string]any
	_ = r.srv.RequestsFor(fake.RouteHeartbeat)[1].JSON(&second)
	if _, ok := second["vitals"]; ok {
		t.Fatalf("vitals still sent: %v", second)
	}
	if second["status"] != "online" || second["capabilities"] == nil {
		t.Errorf("the resend lost its basics: %v", second)
	}
	var found bool
	for _, a := range r.alerts.Active() {
		if a.Code == doctor.CLD055 && string(a.Severity) == "warning" {
			found = true
		}
	}
	if !found {
		t.Error("LAI-CLD-055 was not raised as a warning")
	}
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 3)
}

func TestThreeHeartbeatFailuresMoveToBackoffAndSuccessReturns(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.Script(fake.RouteHeartbeat, fake.Reply{Drop: true}, fake.Reply{Drop: true}, fake.Reply{Drop: true})
	r.step(15 * time.Second) // failure 1
	r.waitCount(fake.RouteHeartbeat, 2)
	r.step(BackoffMin) // failure 2
	r.waitCount(fake.RouteHeartbeat, 3)
	if r.link.State() != StateLinked {
		t.Fatalf("state %s after two failures", r.link.State())
	}
	r.step(BackoffMin) // failure 3
	r.waitCount(fake.RouteHeartbeat, 4)
	r.waitState(StateBackoff)
	if r.link.CloudStatus().LastError == nil {
		t.Error("no last_error in backoff")
	}
	if r.link.Counters().Wire().HeartbeatFailures != 3 {
		t.Errorf("heartbeatFailures %d", r.link.Counters().Wire().HeartbeatFailures)
	}
	r.step(BackoffMin) // success
	r.waitCount(fake.RouteHeartbeat, 5)
	r.waitState(StateLinked)
	// The counter reached the cloud in the next heartbeat's vitals.
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 6)
	c := heartbeatBodyOf(t, r, 5)["vitals"].(map[string]any)["counters"].(map[string]any)
	if c["heartbeatFailures"] != float64(3) {
		t.Errorf("counters %v", c)
	}
}

func TestFiveMinutesOfFailuresRaiseCLD053AndSuccessClearsIt(t *testing.T) {
	r := newRig(t, withRand(upperBound))
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.Forever(fake.RouteHeartbeat, fake.Reply{Status: 503})
	// Failures wait 10, 20, 40, 80, 160 s (the top of each window): the sixth failure comes 310 s after the first.
	delays := []time.Duration{15 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second, 160 * time.Second}
	for i, d := range delays {
		r.step(d)
		r.waitCount(fake.RouteHeartbeat, i+2)
	}
	r.waitState(StateBackoff)
	r.waitAlert(doctor.CLD053, true) // raised by the sixth failure, 310 s after the first
	r.srv.Clear(fake.RouteHeartbeat)
	r.step(300 * time.Second)
	r.waitState(StateLinked)
	r.waitAlert(doctor.CLD053, false)
}

func TestHeartbeat429HonoursALongerRetryAfter(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.Script(fake.RouteHeartbeat, fake.Reply{Status: 429, Header: map[string]string{"Retry-After": "120"}})
	r.step(15 * time.Second)
	r.waitCount(fake.RouteHeartbeat, 2)
	// The jittered delay is 5 s but the server asked for 120 s, which wins.
	if err := r.clk.WaitDue(r.ctx, 120*time.Second); err != nil {
		t.Fatal(err)
	}
	if d, _ := r.clk.NextDelay(); d != 120*time.Second {
		t.Fatalf("next wake-up in %v, want the Retry-After of 120 s", d)
	}
	r.clk.Advance(119 * time.Second)
	if r.srv.Count(fake.RouteHeartbeat) != 2 {
		t.Fatal("a heartbeat was sent before Retry-After")
	}
	r.step(time.Second)
	r.waitCount(fake.RouteHeartbeat, 3)
}

func TestHeartbeat401RevokesAndAHourlyProbeRecovers(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.Revoke(testCredential)
	r.step(15 * time.Second)
	r.waitState(StateRevoked)
	r.waitAlert(doctor.CLD051, true)
	regs := r.srv.Count(fake.RouteRegister)
	hbs := r.srv.Count(fake.RouteHeartbeat)
	// Revoked: no loops, only the hourly register probe.
	r.waitTimers(1)
	r.clk.Advance(30 * time.Minute)
	if r.srv.Count(fake.RouteRegister) != regs || r.srv.Count(fake.RouteHeartbeat) != hbs {
		t.Fatal("the link called the cloud while revoked, before the hourly probe")
	}
	r.step(30 * time.Minute) // the probe: still revoked
	r.waitCount(fake.RouteRegister, regs+1)
	r.waitState(StateRevoked)
	r.srv.Unrevoke(testCredential)
	r.step(time.Hour)
	r.waitCount(fake.RouteRegister, regs+2)
	r.waitState(StateLinked)
	r.waitAlert(doctor.CLD051, false)
}

func TestHeartbeat404RegistersAgainAndTakesTheNewServerRow(t *testing.T) {
	r := newRig(t)
	first := r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.DeleteNode("ai-n3") // an admin deleted the row (or EAB pruned it)
	r.step(15 * time.Second)
	r.waitCount(fake.RouteRegister, 2)
	r.waitState(StateLinked)
	second := r.link.CloudStatus()
	if second.ServerNodeID == nil || *second.ServerNodeID == first.ServerNodeID {
		t.Fatalf("the node kept its old server id: %v", second.ServerNodeID)
	}
	data, _ := readFile(r.linkPath)
	if !strings.Contains(data, *second.ServerNodeID) || strings.Contains(data, first.ServerNodeID) {
		t.Errorf("link.json was not replaced: %s", data)
	}
	// Heartbeats go to the new id.
	r.waitCount(fake.RouteHeartbeat, 3)
	last := r.srv.RequestsFor(fake.RouteHeartbeat)
	if !strings.Contains(last[len(last)-1].Path, *second.ServerNodeID) {
		t.Errorf("heartbeat path %s", last[len(last)-1].Path)
	}
}

func TestHeartbeat403RegistersOnceThenCLD054(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.srv.Script(fake.RouteHeartbeat, fake.Reply{Status: 403}, fake.Reply{Status: 403})
	r.step(15 * time.Second)
	r.waitCount(fake.RouteRegister, 2) // the first 403: register again
	r.waitCount(fake.RouteHeartbeat, 3)
	r.waitAlert(doctor.CLD054, true) // the second 403 after registering again
	if r.srv.Count(fake.RouteRegister) != 2 {
		t.Errorf("register calls %d: a second 403 must not register again", r.srv.Count(fake.RouteRegister))
	}
}

func TestRegister400IsCLD054AndRetriesInAnHour(t *testing.T) {
	r := newRig(t)
	r.srv.Script(fake.RouteRegister, fake.Err(400, "", ""))
	res := r.enroll()
	if res.State != "backoff" {
		t.Fatalf("answer %+v", res)
	}
	if !r.alertActive(doctor.CLD054) {
		t.Fatal("LAI-CLD-054 not raised")
	}
	r.waitTimers(1)
	if d, _ := r.clk.NextDelay(); d != time.Hour {
		t.Fatalf("retry in %v, want 1 h", d)
	}
	r.clk.Advance(time.Hour)
	r.waitState(StateLinked)
	if r.alertActive(doctor.CLD054) {
		t.Error("LAI-CLD-054 not cleared once linked")
	}
}

func TestRegisterRetriesWithJitteredBackoffAndRetryAfter(t *testing.T) {
	r := newRig(t, withCredentialFile(), withRand(upperBound))
	r.srv.Script(fake.RouteRegister,
		fake.Reply{Status: 503}, fake.Reply{Status: 502},
		fake.Reply{Status: 429, Header: map[string]string{"Retry-After": "90"}})
	if err := r.link.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.waitCount(fake.RouteRegister, 1)
	r.waitTimers(1)
	if d, _ := r.clk.NextDelay(); d != 10*time.Second {
		t.Errorf("after 1 failure: %v, want the top of [5 s, 10 s]", d)
	}
	r.clk.Advance(10 * time.Second)
	r.waitCount(fake.RouteRegister, 2)
	r.waitTimers(1)
	if d, _ := r.clk.NextDelay(); d != 20*time.Second {
		t.Errorf("after 2 failures: %v, want 20 s", d)
	}
	r.clk.Advance(20 * time.Second)
	r.waitCount(fake.RouteRegister, 3)
	r.waitTimers(1)
	// 3 failures: the window is [5, 40] s but Retry-After says 90 s and wins.
	if d, _ := r.clk.NextDelay(); d != 90*time.Second {
		t.Errorf("after the 429: %v, want 90 s", d)
	}
	r.clk.Advance(90 * time.Second)
	r.waitState(StateLinked)
}

func TestClockSkewOverFiveMinutesRaisesAGT012(t *testing.T) {
	r := newRig(t)
	r.srv.Handshake.ServerTime = func() time.Time { return r.clk.Now().Add(6 * time.Minute) }
	r.enroll()
	if !r.alertActive(doctor.AGT012) {
		t.Fatal("LAI-AGT-012 not raised for 6 minutes of skew")
	}
	r.srv.Handshake.ServerTime = func() time.Time { return r.clk.Now().Add(4 * time.Minute) }
	r.recheckHandshakeNow()
	if r.alertActive(doctor.AGT012) {
		t.Error("LAI-AGT-012 not cleared at 4 minutes of skew")
	}
}

func TestSameNodeIDFromAnotherMachineRaisesCLD061(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	// Another machine registered as ai-n3 and heartbeated 10 s ago.
	r.srv.SetLastHeartbeat("ai-n3", "host-b", r.clk.Now().Add(-10*time.Second))
	// Register again (a 404 forces it): the answer shows the other machine's hostname and a fresh beat.
	r.srv.Script(fake.RouteHeartbeat, fake.Err(404, "", ""))
	r.step(15 * time.Second)
	r.waitCount(fake.RouteRegister, 2)
	r.waitState(StateLinked)
	if !r.alertActive(doctor.CLD061) {
		t.Fatal("LAI-CLD-061 not raised")
	}
	// The link itself carries on (last writer wins).
	r.waitCount(fake.RouteHeartbeat, 3)
}

func TestCloudAllowChangeRegistersAgainWithTheNewTypes(t *testing.T) {
	r := newRig(t)
	r.enroll()
	r.waitCount(fake.RouteHeartbeat, 1)
	r.rewriteNodeYAML("  allow: [status.refresh, model.load]\n")
	r.cfg.ReloadNow()
	r.waitCount(fake.RouteRegister, 2)
	var body struct {
		EligibleTypes []string `json:"eligibleTypes"`
	}
	if err := r.srv.RequestsFor(fake.RouteRegister)[1].JSON(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.EligibleTypes, ",") != "localai.status.refresh,localai.model.load" {
		t.Fatalf("eligibleTypes %v", body.EligibleTypes)
	}
	r.waitState(StateLinked)
}

// With the cloud answering 503 forever the link only backs off: the status view that /status uses is
// served from memory, shows the state, and the link never blocks a stop or touches the mode.
func TestLocalFeaturesDoNotWaitForAnUnreachableCloud(t *testing.T) {
	r := newRig(t, withCredentialFile())
	for _, route := range []string{fake.RouteRegister, fake.RouteHeartbeat, fake.RouteState, fake.RouteKey, fake.RouteConfig, fake.RouteClaim} {
		r.srv.Forever(route, fake.Reply{Status: 503})
	}
	if err := r.link.Start(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.waitCount(fake.RouteRegister, 1)
	builder := statusapi.NewStatusBuilder(statusapi.StatusSources{Cloud: r.link, Now: r.clk.Now})
	for i := 0; i < 8; i++ {
		r.waitTimers(1)
		r.clk.Advance(300 * time.Second)
		st := builder.Build(r.ctx, false, statusapi.BuildOptions{})
		if !st.Cloud.Enrolled || st.Cloud.State != "registering" || st.Cloud.URL == nil {
			t.Fatalf("status.cloud while the cloud is down: %+v", st.Cloud)
		}
	}
	if r.srv.Count(fake.RouteRegister) < 2 {
		t.Error("the link did not keep retrying")
	}
	r.online.Store(false)
	r.stop() // returns although every call fails
	if r.link.State() != StateStopped {
		t.Errorf("state %s", r.link.State())
	}
	if st := builder.Build(r.ctx, false, statusapi.BuildOptions{}); st.Cloud.State != "stopped" {
		t.Errorf("status.cloud after the stop: %+v", st.Cloud)
	}
}
