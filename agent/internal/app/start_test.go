package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/services"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
)

// alerts returns the codes of the active alerts with their severity.
func (r *rig) alerts() map[string]doctor.Severity {
	r.t.Helper()
	out := map[string]doctor.Severity{}
	for _, a := range r.d.Components().Doctor.Active() {
		out[a.Code] = a.Severity
	}
	return out
}

func (r *rig) requireAlert(code string) doctor.Severity {
	r.t.Helper()
	sev, ok := r.alerts()[code]
	if !ok {
		r.t.Fatalf("no alert %s; active: %v\nlog:\n%s", code, r.alerts(), r.logText())
	}
	return sev
}

func (r *rig) requireNoAlert(code string) {
	r.t.Helper()
	if _, ok := r.alerts()[code]; ok {
		r.t.Fatalf("unexpected alert %s; active: %v", code, r.alerts())
	}
}

// socketGet calls GET path of the API over the unix socket, the way aictl does.
func (r *rig) socketGet(path string) reply {
	r.t.Helper()
	cl := &http.Client{Transport: unixTransport(r.d.Components().API.SocketPath()), Timeout: 10 * time.Second}
	resp, err := cl.Get("http://agent" + api.BasePath + path)
	if err != nil {
		r.t.Fatalf("GET %s over the socket: %v", path, err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: []byte(b.String())}
}

// The order of agent.md section 4.1: steps 1 to 11 in sequence, then 12, 13, 15 and 16 concurrently,
// 14 only after 12 has ended, and 17 last of the steps that gate it (12, 15, 16).
func TestStartupStepOrder(t *testing.T) {
	r := newRig(t)
	r.start()
	r.waitStarted()
	r.waitStepEnd(14)

	r.steps.mu.Lock()
	events := append([]StepEvent(nil), r.steps.events...)
	r.steps.mu.Unlock()
	index := func(n int, end bool) int {
		for i, e := range events {
			if e.N == n && e.End == end {
				return i
			}
		}
		t.Fatalf("step %d (end %v) never happened: %+v", n, end, events)
		return -1
	}
	for n := 1; n < 11; n++ {
		if index(n, true) > index(n+1, false) {
			t.Errorf("step %d ended after step %d began", n, n+1)
		}
	}
	for _, n := range []int{12, 13, 15, 16} {
		if index(11, true) > index(n, false) {
			t.Errorf("step %d began before step 11 ended", n)
		}
	}
	if index(12, true) > index(14, false) {
		t.Error("step 14 (restore models) began before step 12 (reconcile llama) ended")
	}
	for _, n := range []int{12, 15, 16} {
		if index(n, true) > index(17, false) {
			t.Errorf("step 17 began before step %d ended", n)
		}
	}
	// agent.started carries the version, the mode, the node id, the duration and every step's duration.
	var started map[string]any
	for _, l := range r.logs.lines() {
		if l["msg"] == "agent.started" {
			started = l
		}
	}
	if started == nil {
		t.Fatalf("no agent.started line:\n%s", r.logText())
	}
	for _, k := range []string{"version", "mode", "node", "duration_ms", "steps_ms"} {
		if _, ok := started[k]; !ok {
			t.Errorf("agent.started lacks %q: %v", k, started)
		}
	}
	if started["node"] != rigNode || started["version"] != "0.1.0-test" {
		t.Errorf("agent.started = %v", started)
	}
	if steps, _ := started["steps_ms"].(map[string]any); len(steps) < 14 {
		t.Errorf("agent.started lists %d step durations: %v", len(steps), steps)
	}
}

// waitStepEnd waits until step n has ended (the steps run concurrently with the test).
func (r *rig) waitStepEnd(n int) {
	r.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r.steps.mu.Lock()
		for _, e := range r.steps.events {
			if e.N == n && e.End {
				r.steps.mu.Unlock()
				return
			}
		}
		r.steps.mu.Unlock()
		runtime.Gosched()
	}
	r.t.Fatalf("step %d did not end", n)
}

// Not root: fatal, exit 1, LAI-AGT-011, and nothing was touched.
func TestNotRootIsFatal(t *testing.T) {
	r := newRig(t, withEnv(func(e *Env) { e.Geteuid = func() int { return 1000 } }))
	_, err := r.tryStart()
	var fe *FatalError
	if !errors.As(err, &fe) || fe.Code != doctor.AGT011 {
		t.Fatalf("Start = %v, want a FatalError with %s", err, doctor.AGT011)
	}
	if !r.logs.has("not running as root", map[string]any{"code": doctor.AGT011}) {
		t.Errorf("LAI-AGT-011 is not logged:\n%s", r.logText())
	}
	if r.d.Components().API != nil || r.d.Components().State != nil {
		t.Error("a subsystem was built although the agent is not root")
	}
	if _, err := os.Stat(r.path("var/lib/localai/state.json")); err == nil {
		t.Error("state.json was written")
	}
	if len(r.note.msgs) != 0 {
		t.Error("sd_notify messages were sent")
	}
	// Serve returns the same error and its text names the code, for the exit message.
	e2 := r.env
	if err := Serve(context.Background(), e2); !errors.As(err, &fe) {
		t.Errorf("Serve = %v", err)
	}
}

// A damaged start still serves survival: every case boots, answers /health and /portal/status, raises
// the documented alert and accepts POST /mode/survival (agent.md section 4.1, "survival must remain
// reachable even with broken config").
func TestDegradedStarts(t *testing.T) {
	tests := []struct {
		name  string
		opts  func(t *testing.T) []rigOption
		alert string
		sev   doctor.Severity // "" = any
		mode  string          // expected /health mode, "" = online
		extra func(t *testing.T, r *rig)
	}{
		{name: "node.yaml missing", alert: doctor.AGT001, opts: func(*testing.T) []rigOption { return []rigOption{noNodeYAML()} },
			extra: func(t *testing.T, r *rig) {
				if got := r.d.Components().Config.Get().Node.ID; got != "unconfigured" {
					t.Errorf("node.id = %q, want unconfigured", got)
				}
			}},
		{name: "node.yaml invalid", alert: doctor.AGT002, opts: func(*testing.T) []rigOption {
			return []rigOption{withNodeYAML("schema_version: 1\nports: [this, is, not, a, mapping]\n")}
		}},
		{name: "state.json and its backup are both corrupt", alert: doctor.AGT003, sev: doctor.Critical, mode: "offline",
			opts: func(*testing.T) []rigOption {
				return []rigOption{func(r *rig) {
					r.write("var/lib/localai/state.json", "{ not json", 0o640)
					r.write("var/lib/localai/state.json.bak", "also not json", 0o640)
				}}
			},
			extra: func(t *testing.T, r *rig) {
				matches, _ := filepath.Glob(r.path("var/lib/localai/state.json.corrupt-*"))
				if len(matches) != 1 {
					t.Errorf("the corrupt file was not kept: %v", matches)
				}
			}},
		{name: "state.json corrupt, backup good", alert: doctor.AGT003, sev: doctor.Warning,
			opts: func(*testing.T) []rigOption {
				return []rigOption{func(r *rig) {
					r.write("var/lib/localai/state.json", "{ not json", 0o640)
					r.write("var/lib/localai/state.json.bak", `{"schema_version":1,"mode":"online"}`, 0o640)
				}}
			}},
		{name: "state directory not writable", alert: doctor.AGT004, opts: func(*testing.T) []rigOption {
			fs := statefake.New()
			fs.Hook = func(op statefake.Op) error {
				if op.Kind != statefake.KindRead && op.Kind != statefake.KindReadDir && op.Kind != statefake.KindExists {
					return syscall.EROFS
				}
				return nil
			}
			return []rigOption{withEnv(func(e *Env) { e.StateFS = fs })}
		}},
		{name: "audit log cannot be opened", alert: doctor.AGT013, opts: func(*testing.T) []rigOption {
			return []rigOption{func(r *rig) { r.mkdir("var/lib/localai/audit.jsonl") }}
		}},
		{name: "catalog directory missing", alert: doctor.CAT001, opts: func(*testing.T) []rigOption {
			return []rigOption{func(r *rig) { _ = os.RemoveAll(r.path("etc/localai/catalog")) }}
		}},
		{name: "a secret file with a loose mode", alert: doctor.SEC009, opts: func(*testing.T) []rigOption {
			return []rigOption{func(r *rig) { _ = os.Chmod(r.path("etc/localai/api-key"), 0o644) }}
		},
			extra: func(t *testing.T, r *rig) {
				if fi, err := os.Stat(r.path("etc/localai/api-key")); err != nil || fi.Mode().Perm() != 0o600 {
					t.Errorf("api-key mode after startup: %v %v, want 0600", fi, err)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tc.opts(t)...)
			r.start()
			r.waitStarted()
			sev := r.requireAlert(tc.alert)
			if tc.sev != "" && sev != tc.sev {
				t.Errorf("%s severity = %s, want %s", tc.alert, sev, tc.sev)
			}
			want := tc.mode
			if want == "" {
				want = "online"
			}
			h := r.doAuth("GET", "/health", nil, false)
			if h.status != 200 || h.json()["ok"] != true || h.json()["mode"] != want {
				t.Errorf("GET /health = %d %s, want ok and mode %s", h.status, h.body, want)
			}
			if p := r.doAuth("GET", "/portal/status", nil, false); p.status != 200 {
				t.Errorf("GET /portal/status = %d %s", p.status, p.body)
			}
			sv := r.doAuth("POST", "/mode/survival", map[string]any{}, false)
			if sv.status != http.StatusAccepted && sv.status != http.StatusOK {
				t.Errorf("POST /mode/survival = %d %s, want it accepted", sv.status, sv.body)
			}
			if tc.extra != nil {
				tc.extra(t, r)
			}
		})
	}
}

// The restart-loop rule: more than 4 starts within the last hour (this one included) raises
// LAI-AGT-014, the newest 10 are kept, and the alert clears on a start that finds 4 or fewer.
func TestRestartLoopDetection(t *testing.T) {
	now := time.Now().UTC()
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	tests := []struct {
		name   string
		before []time.Time // starts recorded by earlier runs
		alert  bool
		kept   int
	}{
		{"first start", nil, false, 1},
		{"four starts in the hour, this is the fifth", []time.Time{ago(50), ago(40), ago(30), ago(20)}, true, 5},
		{"three earlier starts: the fourth is fine", []time.Time{ago(50), ago(40), ago(30)}, false, 4},
		{"old starts do not count", []time.Time{ago(600), ago(500), ago(400), ago(300), ago(200), ago(61)}, false, 7},
		{"twelve starts keep the newest ten", func() []time.Time {
			var s []time.Time
			for i := 12; i >= 1; i-- {
				s = append(s, ago(i))
			}
			return s
		}(), true, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, func(r *rig) {
				if tc.before == nil {
					return
				}
				b, _ := json.Marshal(state.State{SchemaVersion: 1, Mode: state.ModeOnline, Agent: state.AgentInfo{RecentStarts: tc.before}})
				r.write("var/lib/localai/state.json", string(b), 0o640)
			})
			r.start()
			r.waitStarted()
			if got := hasAlert(r.d, doctor.AGT014); got != tc.alert {
				t.Errorf("LAI-AGT-014 raised = %v, want %v", got, tc.alert)
			}
			starts := r.d.Components().State.Snapshot().Agent.RecentStarts
			if len(starts) != tc.kept {
				t.Errorf("recent_starts holds %d entries, want %d", len(starts), tc.kept)
			}
			if n := len(starts); n > 0 && now.Sub(starts[n-1]) > time.Minute {
				t.Errorf("the newest start %v is not this one", starts[n-1])
			}
		})
	}
	if got := startsWithin([]time.Time{now, now.Add(-time.Hour), now.Add(-59 * time.Minute)}, now, time.Hour); got != 2 {
		t.Errorf("startsWithin counted %d, want 2 (the window is half open)", got)
	}
}

// The listeners: a taken TCP port leaves the socket (LAI-AGT-006), a socket that cannot be made leaves
// TCP (LAI-AGT-005), and both failing is fatal.
func TestListenerFailures(t *testing.T) {
	t.Run("TCP port taken: the socket still serves", func(t *testing.T) {
		taken, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer taken.Close()
		r := newRig(t, withEnv(func(e *Env) { e.TCPAddr = taken.Addr().String() }))
		r.start()
		r.waitStarted()
		r.requireAlert(doctor.AGT006)
		if h := r.socketGet("/health"); h.status != 200 {
			t.Errorf("GET /health over the socket = %d", h.status)
		}
		if r.d.Components().API.TCPAddr() != nil {
			t.Error("the TCP listener is up on a taken port")
		}
		// The watchdog's probe works over the socket when TCP is down.
		if err := r.d.healthOverTransport(context.Background()); err != nil {
			t.Errorf("the health probe over the socket: %v", err)
		}
	})
	t.Run("socket cannot be made: TCP still serves", func(t *testing.T) {
		r := newRig(t, withEnv(func(e *Env) {
			e.SocketHooks.LookupGID = func(string) (int, error) { return 0, errors.New("no such group") }
		}))
		r.start()
		r.waitStarted()
		r.requireAlert(doctor.AGT005)
		if h := r.doAuth("GET", "/health", nil, false); h.status != 200 {
			t.Errorf("GET /health over TCP = %d", h.status)
		}
		if err := r.d.healthOverTransport(context.Background()); err != nil {
			t.Errorf("the health probe over TCP: %v", err)
		}
	})
	t.Run("both fail: fatal, exit 1", func(t *testing.T) {
		taken, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer taken.Close()
		r := newRig(t, withEnv(func(e *Env) {
			e.TCPAddr = taken.Addr().String()
			e.SocketHooks.LookupGID = func(string) (int, error) { return 0, errors.New("no such group") }
		}))
		_, err = r.tryStart()
		var fe *FatalError
		if !errors.As(err, &fe) {
			t.Fatalf("Start = %v, want a FatalError", err)
		}
		if fe.Code != doctor.AGT005 && fe.Code != doctor.AGT006 {
			t.Errorf("fatal code = %s", fe.Code)
		}
		if r.d.Started() == nil {
			t.Fatal("no Started channel")
		}
		select {
		case <-r.d.Started():
			t.Error("a daemon whose listeners failed reached step 17")
		default:
		}
		if len(r.note.msgs) != 0 {
			t.Errorf("READY=1 must not be sent when both listeners failed; got %v", r.d.env.Notifier.Sent())
		}
	})
}

// systemd's D-Bus unavailable falls back to systemctl: the backend is chosen once at startup.
func TestSystemdFallsBackToSystemctl(t *testing.T) {
	r := newRig(t, withEnv(func(e *Env) {
		e.Systemd = nil
		e.DialBus = func(context.Context) (services.Bus, error) { return nil, errors.New("no system bus") }
	}))
	d := NewDaemon(r.env)
	d.ensureLogger()
	m, backend := d.newSystemd(context.Background())
	if backend != services.BackendSystemctl {
		t.Errorf("backend = %q, want %q when D-Bus cannot be dialed", backend, services.BackendSystemctl)
	}
	if m == nil || len(d.closers) != 1 {
		t.Error("the fallback Manager is not tracked for shutdown")
	}
	// An injected Manager is used as is.
	r2 := newRig(t)
	d2 := NewDaemon(r2.env)
	d2.ensureLogger()
	if m, backend := d2.newSystemd(context.Background()); m != r2.sysd || backend != "injected" {
		t.Errorf("the injected Manager was not used: %v %q", m, backend)
	}
}

// READY=1, the cadence of WATCHDOG=1 on the daemon's clock, and nothing else but STATUS lines.
func TestSdNotifyReadyAndWatchdogCadence(t *testing.T) {
	clock := cfgfakeClock()
	r := newRig(t, withEnv(func(e *Env) { e.Clock = clock }))
	r.start()
	r.waitStarted()
	if got := r.note.next(t); got != "READY=1" {
		t.Fatalf("first message = %q", got)
	}
	for i := 0; i < 3; i++ {
		clock.Advance(DefaultWatchdogEvery)
		var msg string
		for msg = r.note.next(t); strings.HasPrefix(msg, "STATUS="); msg = r.note.next(t) {
		}
		if msg != "WATCHDOG=1" {
			t.Fatalf("ping %d = %q, want WATCHDOG=1 every %v", i, msg, DefaultWatchdogEvery)
		}
	}
}
