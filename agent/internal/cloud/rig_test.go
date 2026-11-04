package cloud

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/cloud/fake"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/events"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/state"
	statefake "github.com/marinoscar/local-ai/agent/internal/state/fake"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

var t0 = time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)

// testCredential and testKey are built at run time so that no credential-looking text is in the source.
var (
	testCredential = "nod_" + strings.Repeat("x7Kq", 8)
	testKey        = strings.Repeat("ab12", 8) + strings.Repeat("cd34", 8)
)

// clockAdapter lets the fake clock satisfy Clock (its NewTimer returns *fake.Timer).
type clockAdapter struct{ c *fake.Clock }

func (a clockAdapter) Now() time.Time                 { return a.c.Now() }
func (a clockAdapter) NewTimer(d time.Duration) Timer { return a.c.NewTimer(d) }

// fakeStatus is a StatusSource that returns a status the test edits.
type fakeStatus struct {
	mu sync.Mutex
	st statusapi.Status
}

func (f *fakeStatus) Build(context.Context, bool, statusapi.BuildOptions) statusapi.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeStatus) edit(fn func(*statusapi.Status)) {
	f.mu.Lock()
	fn(&f.st)
	f.mu.Unlock()
}

func str(s string) *string   { return &s }
func i64(n int64) *int64     { return &n }
func f64(n float64) *float64 { return &n }
func intp(n int) *int        { return &n }

func baseStatus() statusapi.Status {
	temp := 61.0
	return statusapi.Status{
		Node: statusapi.NodeBlock{
			ID: "ai-n3", DisplayName: "Node Three", Hostname: "host-a", Profile: str("small-8gb"), AgentVersion: "0.1.0", Backend: "cuda",
			LlamaMode: "router", LlamaBuild: str("f3586e66b-cuda"), CatalogVersion: str("2026.11.02-1"), Time: "2026-11-02T15:00:00Z",
		},
		Mode:         statusapi.ModeBlock{Value: "online"},
		Connectivity: statusapi.ConnectivityBlock{Value: "full"},
		Hardware: statusapi.HardwareBlock{
			GPU: str("GPU Example"), VRAMTotalMiB: i64(8192), VRAMUsedMiB: i64(6791), RAMTotalMiB: 14336, RAMAvailableMiB: 5120,
			GPUTempC: &temp, CPUTempC: f64(72),
			Power: statusapi.PowerBlock{Source: "ac", BatteryPercent: intp(100)},
			GPUs:  []statusapi.GPUBlock{{Index: 0, Vendor: "nvidia", Name: "GPU Example", VRAMTotalMiB: 8192, VRAMUsedMiB: 6791, TempC: &temp, UtilPct: intp(3)}},
		},
		Services:  []statusapi.ServiceRow{{Name: "llama", Unit: "localai-llama.service", Active: "active", Sub: "running", Healthy: true}},
		Models:    statusapi.ModelsBlock{Default: str("qwen3.5-9b-q4km"), Loaded: []string{"qwen3.5-9b-q4km"}, Loading: []string{}, Present: 2, Loadable: 4, ModelsMax: 1},
		Ark:       statusapi.ArkBlock{Roots: []statusapi.ArkRootRow{}, Sync: statusapi.ArkSyncRow{State: "idle"}},
		Cloud:     statusapi.CloudBlock{State: "unenrolled"},
		Tailscale: statusapi.TailscaleBlock{Manage: true, Pattern: "B", Applied: true, BackendState: str("Running")},
		Alerts:    []statusapi.AlertRow{{Code: "LAI-HW-005", Severity: "warning", Title: "Battery is low"}},
	}
}

type rig struct {
	t      *testing.T
	ctx    context.Context
	srv    *fake.Server
	clk    *fake.Clock
	link   *Link
	cfg    *config.Store
	st     *state.Store
	bus    *events.Bus
	alerts *doctor.Manager
	status *fakeStatus
	models *fakeModels
	ark    *fakeArk
	drill  *fakeDrill
	hw     *fakeHW
	ws     *watchState
	logs   *syncBuf
	dir    string
	online atomic.Bool
	deps   Deps

	credPath, linkPath, keyPath, nodePath string
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type rigOptions struct {
	allow      []string
	noKeyFile  bool
	offline    bool
	credential bool // write the credential file before the link starts
	mutateDeps func(*Deps)
	rand       RandDuration
}

type rigOpt func(*rigOptions)

func withoutKeyFile() rigOpt         { return func(o *rigOptions) { o.noKeyFile = true } }
func withOffline() rigOpt            { return func(o *rigOptions) { o.offline = true } }
func withCredentialFile() rigOpt     { return func(o *rigOptions) { o.credential = true } }
func withDeps(f func(*Deps)) rigOpt  { return func(o *rigOptions) { o.mutateDeps = f } }
func withRand(f RandDuration) rigOpt { return func(o *rigOptions) { o.rand = f } }

// upperBound makes the jitter take the top of its window.
func upperBound(lo, hi time.Duration) time.Duration { return hi }

// newRig wires a Link to the fake cloud, a real config store on a temp node.yaml, a real state store
// on an in-memory filesystem, a real event bus and doctor alert manager, and the manual clock.
func newRig(t *testing.T, opts ...rigOpt) *rig {
	t.Helper()
	var o rigOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	r := &rig{t: t, ctx: ctx, dir: t.TempDir(), logs: &syncBuf{}}
	r.srv = fake.New(t)
	r.clk = fake.NewClock(t0)
	r.srv.Now = r.clk.Now // the cloud and the node agree on the time unless a test says otherwise
	// The state loop's timers are long by default, so the tests that step the heartbeat or the backoff
	// see only that loop's timer; the state push tests set the real intervals before they enroll.
	r.srv.Handshake.StateMinIntervalS, r.srv.Handshake.StateMaxIntervalS = 600, 3600
	r.online.Store(!o.offline)
	r.bus = events.New(events.Options{Node: "ai-n3"})
	t.Cleanup(r.bus.Close)
	r.alerts = doctor.NewManager(doctor.ManagerOptions{Bus: r.bus, Now: r.clk.Now})

	nodeYAML := "schema_version: 1\nnode:\n  id: ai-n3\n  display_name: \"Node Three\"\nprofile: small-8gb\ncloud:\n  url: " + r.srv.URL + "\n"
	if len(o.allow) > 0 {
		nodeYAML += "  allow: [" + strings.Join(o.allow, ", ") + "]\n"
	}
	nodePath := filepath.Join(r.dir, "node.yaml")
	r.nodePath = nodePath
	if err := os.WriteFile(nodePath, []byte(nodeYAML), 0o640); err != nil {
		t.Fatal(err)
	}
	r.cfg = config.Open(config.Options{Path: nodePath})

	var err error
	r.st, err = state.Open(filepath.Join(r.dir, "state.json"), state.Options{FS: statefake.New(), Now: r.clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.st.Close() })

	r.credPath = filepath.Join(r.dir, "cloud-credential")
	r.linkPath = filepath.Join(r.dir, "cloud", "link.json")
	r.keyPath = filepath.Join(r.dir, "api-key")
	if !o.noKeyFile {
		if err := os.WriteFile(r.keyPath, []byte(testKey+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if o.credential {
		if err := os.WriteFile(r.credPath, []byte(testCredential+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r.status = &fakeStatus{st: baseStatus()}
	r.models, r.ark, r.drill = &fakeModels{}, &fakeArk{}, &fakeDrill{}
	r.hw = &fakeHW{snap: baseHardware()}
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelDebug)
	logger := slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: lv}))

	r.ws = &watchState{Store: r.st, ch: make(chan struct{})}
	r.deps = Deps{
		Config: r.cfg, State: r.ws, Status: r.status,
		Credential: CredentialStore{Path: r.credPath}, LinkFile: LinkStore{Path: r.linkPath},
		Snapshot: SnapshotOptions{Models: r.models, Ark: r.ark, Sync: nil, Drill: r.drill},
		Hardware: r.hw, Host: &fakeHost{}, Keys: FileKeySource{Path: r.keyPath, State: r.st},
		Events: r.bus, Bus: r.bus, Alerts: r.alerts,
		Online: r.online.Load, Clock: clockAdapter{r.clk}, HTTP: r.srv.Client(), Version: "0.1.0",
		Hostname:  func() (string, error) { return "host-a", nil },
		StartedAt: t0.Add(-time.Hour), Logger: logger,
		// Jitter always takes the lower bound, so a test knows the delays.
		Rand: func(lo, hi time.Duration) time.Duration { return lo },
	}
	if o.rand != nil {
		r.deps.Rand = o.rand
	}
	if o.mutateDeps != nil {
		o.mutateDeps(&r.deps)
	}
	r.link, err = New(r.deps)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.link.Shutdown(c)
	})
	return r
}

// watchState decorates the state store so a test can wait for a mutation instead of polling.
type watchState struct {
	*state.Store
	mu sync.Mutex
	ch chan struct{}
}

func (w *watchState) Mutate(fn func(*state.State) error) error {
	err := w.Store.Mutate(fn)
	w.mu.Lock()
	close(w.ch)
	w.ch = make(chan struct{})
	w.mu.Unlock()
	return err
}

func (w *watchState) changed() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

type fakeModels struct {
	mu   sync.Mutex
	view statusapi.ModelsView
}

func (f *fakeModels) Models(string) statusapi.ModelsView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

type fakeArk struct {
	mu sync.Mutex
	r  ArkReport
}

func (f *fakeArk) Ark() ArkReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.r
}

type fakeDrill struct {
	mu sync.Mutex
	d  *DrillReport
}

func (f *fakeDrill) Drill() *DrillReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.d
}

type fakeHW struct {
	mu   sync.Mutex
	snap hardware.Snapshot
}

func (f *fakeHW) Snapshot() hardware.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeHW) set(s hardware.Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
}

func baseHardware() hardware.Snapshot {
	bat := 100
	return hardware.Snapshot{
		SampledAt: t0, Self: hardware.Self{CPUPercent: 1.2, RSSMiB: 40},
		Power: hardware.Power{Source: hardware.SourceAC, BatteryPercent: &bat},
		Disks: []hardware.Disk{
			{Path: "state-dir", Role: hardware.RoleState, TotalBytes: 342_000_000_000, FreeBytes: 293_000_000_000},
			{Path: "content-root", Role: hardware.RoleInternalRoot, TotalBytes: 342_000_000_000, FreeBytes: 273 << 30},
		},
	}
}

type fakeHost struct{}

func (*fakeHost) Host() HostVitals {
	return HostVitals{CPUPercent: 7.5, CPUCount: 12, LoadAvg1: 0.4, LoadAvg5: 0.5, LoadAvg15: 0.6,
		MemTotalBytes: 15032385536, MemFreeBytes: 1073741824, MemAvailableBytes: 5368709120, OSRelease: "Test OS 1.0", Arch: "x86_64", UptimeSeconds: 172800}
}

// ---------------------------------------------------------------------------------------------------
// Waiting helpers: nothing here sleeps; each blocks on an event or a channel, with the test's context
// as the safety net.

// enroll enrolls the link with the fake and fails the test on an error.
func (r *rig) enroll() api.CloudEnrolled {
	r.t.Helper()
	res, err := r.link.Enroll(r.ctx, r.srv.URL, testCredential)
	if err != nil {
		r.t.Fatalf("enroll: %v", err)
	}
	return res
}

// waitState blocks until the link is in want.
func (r *rig) waitState(want State) {
	r.t.Helper()
	filter, _ := events.NewFilter(events.TypeCloudState)
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	ch, err := r.bus.Subscribe(ctx, filter, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	for r.link.State() != want {
		select {
		case <-ch:
		case <-ctx.Done():
			r.t.Fatalf("waiting for state %s: still %s (%v)", want, r.link.State(), ctx.Err())
		}
	}
}

// waitCount blocks until the fake has seen n requests on route.
func (r *rig) waitCount(route string, n int) {
	r.t.Helper()
	if err := r.srv.WaitCount(r.ctx, route, n); err != nil {
		r.t.Fatal(err)
	}
}

// waitHeartbeatApplied blocks until the fake has received the nth heartbeat and the heartbeat loop has
// applied the answer. waitCount alone is not enough before a test acts on the link: the fake logs a
// request before it answers it, so the client may still be applying the answer (heartbeatOK moves
// backoff to linked, and calls the claim loop's Probe while disabled) when waitCount returns, and that
// can undo what the test did in between (issue #237). The loop arms its next timer only after
// heartbeatOnce has returned, so a timer due within disabledInterval (the longest period the loop
// waits) is the proof that the answer was applied; nothing sleeps. It counts on the other loops'
// timers being longer than that, which holds in the default rig (the state loop waits 600 s) but not
// after quickState.
func (r *rig) waitHeartbeatApplied(n int) {
	r.t.Helper()
	r.waitCount(fake.RouteHeartbeat, n)
	if err := r.clk.WaitDue(r.ctx, disabledInterval); err != nil {
		r.t.Fatalf("waiting for heartbeat %d to be applied (next timer %v): %v", n, func() any { d, _ := r.clk.NextDelay(); return d }(), err)
	}
}

// waitTimers blocks until the code under test has armed n timers.
func (r *rig) waitTimers(n int) {
	r.t.Helper()
	if err := r.clk.WaitPending(r.ctx, n); err != nil {
		r.t.Fatalf("waiting for %d timers (have %d): %v", n, r.clk.Pending(), err)
	}
}

// waitProbeArmed waits (in real time) until exactly one timer is pending and it is a full hour away: the
// revoked link's hourly register probe, armed and not yet advanced.
func (r *rig) waitProbeArmed() {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := r.clk.NextDelay(); ok && r.clk.Pending() == 1 && d == time.Hour {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	d, _ := r.clk.NextDelay()
	r.t.Fatalf("the hourly probe was never armed alone: %d timers pending, next %v", r.clk.Pending(), d)
}

// step waits until some timer is due within d and then moves the clock by d: one tick of whichever
// loop is about to wake.
func (r *rig) step(d time.Duration) {
	r.t.Helper()
	if err := r.clk.WaitDue(r.ctx, d); err != nil {
		r.t.Fatalf("no timer due within %v (next %v): %v", d, func() any { n, _ := r.clk.NextDelay(); return n }(), err)
	}
	r.clk.Advance(d)
}

// statusapiStatus is the status body the fake source edits.
type statusapiStatus = statusapi.Status

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}

// recheckHandshakeNow runs the handshake again from the test goroutine.
func (r *rig) recheckHandshakeNow() {
	r.t.Helper()
	if err := r.link.handshake(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

// rewriteNodeYAML replaces node.yaml with the rig's one plus extra lines inside the cloud block.
func (r *rig) rewriteNodeYAML(extraCloudLines string) {
	r.t.Helper()
	body := "schema_version: 1\nnode:\n  id: ai-n3\n  display_name: \"Node Three\"\nprofile: small-8gb\ncloud:\n  url: " + r.srv.URL + "\n" + extraCloudLines
	if err := os.WriteFile(r.nodePath, []byte(body), 0o640); err != nil {
		r.t.Fatal(err)
	}
}

// waitAlert blocks until the doctor holds (or, with present false, no longer holds) an alert for code.
func (r *rig) waitAlert(code string, present bool) {
	r.t.Helper()
	filter, _ := events.NewFilter("alert.*")
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	ch, err := r.bus.Subscribe(ctx, filter, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	for r.alertActive(code) != present {
		select {
		case <-ch:
		case <-ctx.Done():
			r.t.Fatalf("waiting for alert %s present=%v: %v", code, present, ctx.Err())
		}
	}
}

// alertActive reports whether the doctor holds an alert for code.
func (r *rig) alertActive(code string) bool {
	for _, a := range r.alerts.Active() {
		if a.Code == code {
			return true
		}
	}
	return false
}

func (r *rig) stop() {
	r.t.Helper()
	if err := r.link.Stop(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

func modelLoaded(id string) events.ModelLoaded { return events.ModelLoaded{Model: id, LoadMS: 100} }

func serviceState() events.ServiceState {
	return events.ServiceState{Name: "llama", Unit: "localai-llama.service", From: "ok", To: "ok", Active: "active", Sub: "running", Healthy: true}
}

func fakeReply(status int) fake.Reply { return fake.Reply{Status: status} }

func fakeStale() fake.Reply { return fake.Err(409, doctor.CLD008, "stale_seq") }

// badRequestWithPath is a 400 whose details carry a Zod issue path and a value the agent must not log.
func badRequestWithPath(path ...string) fake.Reply {
	p := make([]any, len(path))
	for i, s := range path {
		p[i] = s
	}
	return fake.Reply{Status: 400, Body: map[string]any{"error": map[string]any{
		"message": "Bad Request",
		"details": map[string]any{"code": doctor.CLD008, "issues": []any{map[string]any{"path": p, "received": "SECRET-VALUE"}}},
	}}}
}
