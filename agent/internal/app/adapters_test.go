package app

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/audit"
	"github.com/marinoscar/local-ai/agent/internal/cloud"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	hwfake "github.com/marinoscar/local-ai/agent/internal/hardware/fake"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
	svcfake "github.com/marinoscar/local-ai/agent/internal/services/fake"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

func TestLlamaUnitAdapter(t *testing.T) {
	tests := []struct {
		name        string
		st          services.UnitState
		wantRunning bool
		wantEnabled bool
	}{
		{"active and enabled", services.UnitState{ActiveState: "active", SubState: "running", UnitFileState: "enabled"}, true, true},
		{"activating counts as running: a start would change nothing", services.UnitState{ActiveState: "activating", UnitFileState: "enabled"}, true, true},
		{"reloading counts as running", services.UnitState{ActiveState: "reloading", UnitFileState: "enabled"}, true, true},
		{"inactive and enabled", services.UnitState{ActiveState: "inactive", SubState: "dead", UnitFileState: "enabled"}, false, true},
		{"failed", services.UnitState{ActiveState: "failed", UnitFileState: "enabled"}, false, true},
		{"inactive and disabled", services.UnitState{ActiveState: "inactive", UnitFileState: "disabled"}, false, false},
		{"no unit file", services.UnitState{}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := svcfake.New()
			tc.st.Unit = services.UnitLlama
			m.Set(tc.st)
			got, err := llamaUnit{m}.State(context.Background())
			if err != nil || got.Running != tc.wantRunning || got.Enabled != tc.wantEnabled {
				t.Errorf("State = %+v, %v; want running %v enabled %v", got, err, tc.wantRunning, tc.wantEnabled)
			}
		})
	}
	// Start and RestartLlama go to the llama unit only.
	m := svcfake.New()
	u := llamaUnit{m}
	if err := u.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := u.RestartLlama(context.Background()); err != nil {
		t.Fatal(err)
	}
	var verbs []string
	for _, c := range m.Calls() {
		if c.Unit != services.UnitLlama {
			t.Errorf("a call on %s", c.Unit)
		}
		verbs = append(verbs, string(c.Verb))
	}
	if !reflect.DeepEqual(verbs, []string{"start", "restart"}) {
		t.Errorf("verbs = %v", verbs)
	}
	// A failing manager's error comes back.
	m.Fail(services.VerbState, services.UnitLlama, errors.New("bus down"))
	if _, err := u.State(context.Background()); err == nil {
		t.Error("State must return the manager's error")
	}
}

func TestPowerSource(t *testing.T) {
	pct := func(n int) *int { return &n }
	tests := []struct {
		name   string
		power  hardware.Power
		wantOK bool
		want   int
		onBat  bool
	}{
		{"a desktop has no battery", hardware.Power{Source: hardware.SourceAC}, false, 0, false},
		{"a laptop on battery", hardware.Power{Source: hardware.SourceBattery, BatteryPercent: pct(41)}, true, 41, true},
		{"a laptop on the charger", hardware.Power{Source: hardware.SourceAC, BatteryPercent: pct(93), Charging: true}, true, 93, false},
	}
	for _, tc := range tests {
		snap := rigHardware()
		snap.Power = tc.power
		hw := hwfake.New(snap)
		st, ok := powerSource{h: hw}.Battery()
		if ok != tc.wantOK || st.Percent != tc.want || st.OnBattery != tc.onBat {
			t.Errorf("%s: Battery = %+v, %v", tc.name, st, ok)
		}
	}
}

type viewStub struct{ v router.View }

func (s viewStub) View() router.View { return s.v }

func TestLlamaActivity(t *testing.T) {
	lock := router.NewOpLock(router.NewOpID, time.Now)
	idle := viewStub{router.View{Models: []router.ModelStatus{{ID: "a", Status: router.StatusLoaded}}}}
	busy := viewStub{router.View{Models: []router.ModelStatus{{ID: "a", Status: router.StatusLoading}}}}
	got := func(a llamaActivity) bool {
		b, err := a.Busy(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got(llamaActivity{lock: lock, view: idle}) {
		t.Error("idle llama reported busy")
	}
	if !got(llamaActivity{lock: lock, view: busy}) {
		t.Error("a model that is loading is busy")
	}
	hold, err := lock.TryAcquire("load")
	if err != nil {
		t.Fatal(err)
	}
	if !got(llamaActivity{lock: lock, view: idle}) {
		t.Error("a held model-operations lock is busy")
	}
	hold.Release()
	if got(llamaActivity{lock: lock, view: idle}) {
		t.Error("released lock reported busy")
	}
	if got(llamaActivity{}) {
		t.Error("no sources: never busy")
	}
}

func TestGauges(t *testing.T) {
	g := &gauges{}
	g.Set("localai_drill_ok", nil, 1)
	g.Set("localai_drill_check", map[string]string{"id": "drl.fence", "result": "pass"}, 2)
	g.Set("localai_drill_ok", nil, 3)
	got := g.Counters()
	if got["localai_drill_ok"] != 3.0 || got["localai_drill_check{id=drl.fence,result=pass}"] != 2.0 || len(got) != 2 {
		t.Errorf("Counters = %v", got)
	}
	got["x"] = 1.0 // a copy
	if _, ok := g.Counters()["x"]; ok {
		t.Error("Counters must return a copy")
	}
}

func TestOwnedBlocks(t *testing.T) {
	load := func(text string) config.Config {
		cfg, probs := config.Parse([]byte("schema_version: 1\nnode: {id: n1}\nprofile: p\n"+text), nil)
		for _, p := range probs {
			if p.Severity == config.SeverityError {
				t.Fatalf("fixture: %+v", p)
			}
		}
		return cfg
	}
	tests := []struct {
		name    string
		text    string
		enabled bool
		threads int
	}{
		{"block absent: off (no unit to supervise)", "", false, 0},
		{"enabled with threads", "kiwix:\n  enabled: true\n  threads: 6\n", true, 6},
		{"block without the key counts as enabled", "kiwix:\n  threads: 2\n", true, 2},
		{"explicitly disabled", "kiwix:\n  enabled: false\n  threads: 8\n", false, 8},
	}
	for _, tc := range tests {
		cfg := load(tc.text)
		if got := blockEnabled(cfg, "kiwix"); got != tc.enabled {
			t.Errorf("%s: blockEnabled = %v", tc.name, got)
		}
		if got := kiwixThreads(cfg); got != tc.threads {
			t.Errorf("%s: kiwixThreads = %d", tc.name, got)
		}
	}
	if blockEnabled(load("whisper:\n  enabled: true\n"), "kiwix") {
		t.Error("another block's flag leaked")
	}
}

func TestStartsWithinAndPathsHelpers(t *testing.T) {
	if got := hasPath([]string{"a", "kiwix", "b"}, "kiwix"); !got {
		t.Error("hasPath")
	}
	if hasPath([]string{"kiwix.threads"}, "kiwix") {
		t.Error("hasPath is exact")
	}
	for _, tc := range []struct {
		paths  []string
		prefix string
		want   bool
	}{
		{[]string{"profile"}, "profile", true},
		{[]string{"profile_overrides"}, "profile", true},
		{[]string{"profile.x"}, "profile", true},
		{[]string{"profiler"}, "profile", false},
		{[]string{"catalog.channel"}, "catalog", true},
		{nil, "catalog", false},
	} {
		if got := hasPrefix(tc.paths, tc.prefix); got != tc.want {
			t.Errorf("hasPrefix(%v, %q) = %v", tc.paths, tc.prefix, got)
		}
	}
}

func TestLateBindings(t *testing.T) {
	var l lateLoader
	if err := l.LoadHeld(context.Background(), "m"); err == nil {
		t.Error("the loader must say the orchestrator is not built yet")
	}
	var v lazyViews
	if got := v.Cloud(); got.State != string(cloud.StateUnenrolled) || got.Enrolled {
		t.Errorf("an unset cloud view = %+v", got)
	}
}

// Alerts raised before the alert manager exists are kept and raised when it does; a clear removes a
// pending one; a code that cannot be raised is dropped with a warning.
func TestPendingAlerts(t *testing.T) {
	r := newRig(t)
	d := NewDaemon(r.env)
	d.ensureLogger()
	d.raise(doctor.AGT004, map[string]any{"path": "/x"})
	d.raiseSeverity(doctor.AGT014, doctor.Critical, nil)
	d.raise(doctor.SEC011, nil)
	d.clearAlert(doctor.SEC011)
	d.raise("LAI-XXX-999", nil)
	if len(d.pending) != 3 {
		t.Fatalf("pending = %+v", d.pending)
	}
	d.c.Doctor = doctor.NewManager(doctor.ManagerOptions{Registry: doctor.NewRegistry()})
	d.flushAlerts()
	active := map[string]doctor.Severity{}
	for _, a := range d.c.Doctor.Active() {
		active[a.Code] = a.Severity
	}
	if active[doctor.AGT014] != doctor.Critical || len(active) != 2 {
		t.Errorf("active after the flush = %v", active)
	}
	if _, ok := active[doctor.SEC011]; ok {
		t.Error("a cleared pending alert was raised")
	}
	// Once the manager exists, raise and clear go straight to it.
	d.raise(doctor.SEC011, nil)
	d.clearAlert(doctor.AGT004)
	if hasAlert(d, doctor.AGT004) || !hasAlert(d, doctor.SEC011) {
		t.Errorf("direct raise and clear: %+v", d.c.Doctor.Active())
	}
	// The PIN lockout reaches the same manager through auth.AlertFunc.
	authAlert(d)(doctor.SEC004, "warning", map[string]any{"locked": true})
	if !hasAlert(d, doctor.SEC004) {
		t.Error("authAlert did not raise")
	}
}

func TestHardwareChecksAdapter(t *testing.T) {
	svc := hardware.New(hardware.Options{})
	checks := hardwareChecks(svc)
	if len(checks) != 6 {
		t.Fatalf("%d checks, want one per group", len(checks))
	}
	for _, c := range checks {
		if !strings.HasPrefix(c.ID, "hw.") || c.Area != "HW" || !c.Fast {
			t.Errorf("check %+v", c)
		}
		if got := c.Run(context.Background()); len(got) != 0 {
			t.Errorf("%s reported %v before any sample", c.ID, got)
		}
	}
}

// The audit slot drops a record until the log exists and forwards it after.
func TestAuditSlot(t *testing.T) {
	slot := &auditSlot{}
	if err := slot.Write(audit.Event{Action: "key.show"}); err != nil {
		t.Errorf("a record before the log is dropped without an error: %v", err)
	}
	path := t.TempDir() + "/a.jsonl"
	l, err := audit.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	slot.set(l)
	if err := slot.Write(audit.Event{Action: "key.show", Result: audit.ResultOK, Actor: audit.System()}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "key.show") {
		t.Errorf("the record did not reach the log: %q %v", b, err)
	}
}

// kiwix.threads edited in node.yaml: the unit is rewritten with the new thread count and systemd is
// reloaded, never restarted (companion-services.md section 3.3).
func TestKiwixThreadsChangeRewritesTheUnit(t *testing.T) {
	r := newRig(t, func(r *rig) { r.writeNodeYAMLText(r.nodeYAML() + "kiwix:\n  enabled: true\n  threads: 4\n") })
	r.start()
	r.waitStarted()
	r.sysd.Reset()
	r.writeNodeYAMLText(r.nodeYAML() + "kiwix:\n  enabled: true\n  threads: 7\n")
	r.d.Components().Config.ReloadNow()
	unit := r.path("etc/systemd/system/localai-kiwix.service")
	deadline := time.Now().Add(30 * time.Second)
	for {
		b, err := os.ReadFile(unit)
		if err == nil && strings.Contains(string(b), "--threads=7") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the unit was not rewritten: %v\nlog:\n%s", err, r.logText())
		}
		runtime.Gosched()
	}
	// The file lands before the reload: wait for the reload too (the unit writer's last step).
	var reloaded, restarted bool
	for !reloaded {
		if time.Now().After(deadline) {
			t.Fatalf("no daemon-reload after the unit was written: %v", r.sysd.Calls())
		}
		for _, c := range r.sysd.Calls() {
			if c.Verb == services.VerbDaemonReload {
				reloaded = true
			}
			if c.Unit == services.UnitKiwix && (c.Verb == services.VerbStart || c.Verb == services.VerbStop || c.Verb == services.VerbRestart) {
				restarted = true
			}
		}
		runtime.Gosched()
	}
	if !reloaded || restarted {
		t.Errorf("daemon-reload %v, kiwix started or stopped %v: the writer reloads and never touches the unit", reloaded, restarted)
	}
}

// logging.level and the models.ini inputs are applied when node.yaml changes on disk.
func TestConfigChangeAppliesLiveSettings(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	d := r.d
	d.level.Set(-4)
	d.applyConfigChange(context.Background(), config.Change{Changed: []string{"logging.level"}, New: config.Config{Logging: config.LoggingBlock{Level: "warn"}}})
	if got := d.level.Level().String(); got != "WARN" {
		t.Errorf("level = %s after logging.level=warn", got)
	}
	// A level that is not valid is refused and changes nothing.
	d.applyConfigChange(context.Background(), config.Change{Changed: []string{"logging.level"}, New: config.Config{Logging: config.LoggingBlock{Level: "loud"}}})
	if got := d.level.Level().String(); got != "WARN" {
		t.Errorf("level = %s after an invalid value", got)
	}
	// A profile edit on disk regenerates models.ini; an edit through Store.Update does not (its
	// handler does it).
	rec := &recordingRegen{}
	d.sdeps.Regenerator = rec
	d.applyConfigChange(context.Background(), config.Change{Source: config.SourceReload, Changed: []string{"profile"}})
	d.applyConfigChange(context.Background(), config.Change{Source: config.SourceUpdate, Changed: []string{"profile"}})
	d.applyConfigChange(context.Background(), config.Change{Source: config.SourceReload, Changed: []string{"ui.language"}})
	if rec.n != 1 {
		t.Errorf("regenerations = %d, want exactly the one for the edit on disk", rec.n)
	}
}

type recordingRegen struct {
	mu sync.Mutex
	n  int
}

func (r *recordingRegen) Regenerate(context.Context) (statusapi.RegenResult, error) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return statusapi.RegenResult{}, nil
}
