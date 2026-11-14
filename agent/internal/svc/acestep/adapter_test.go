package acestep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/catalog"
	"github.com/marinoscar/local-ai/agent/internal/config"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/services"
)

// ---- fakes ----

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) NewTicker(time.Duration) config.Ticker { panic("not used") }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeUnits is systemd for one unit. After a start the unit is active; startFails makes Start fail;
// exitAfter makes the process exit after that many State calls; stopHangs keeps it active.
type fakeUnits struct {
	mu         sync.Mutex
	active     bool
	startErr   error
	stopErr    error
	stopHangs  bool
	exitAfter  int
	stateCalls int
	calls      []string
}

func (u *fakeUnits) State(_ context.Context, unit string) (services.UnitState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if unit != UnitName {
		panic("unexpected unit " + unit)
	}
	u.stateCalls++
	if u.active && u.exitAfter > 0 && u.stateCalls >= u.exitAfter {
		u.active = false
		return services.UnitState{Unit: unit, ActiveState: services.ActiveFailed, ExecMainStatus: 9}, nil
	}
	if u.active {
		return services.UnitState{Unit: unit, ActiveState: services.ActiveActive, SubState: "running", MainPID: 4242}, nil
	}
	return services.UnitState{Unit: unit, ActiveState: services.ActiveInactive, SubState: "dead"}, nil
}

func (u *fakeUnits) Start(_ context.Context, unit string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, "start")
	if u.startErr != nil {
		return u.startErr
	}
	u.active, u.stateCalls = true, 0
	return nil
}

func (u *fakeUnits) Stop(_ context.Context, unit string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, "stop")
	if u.stopErr != nil {
		return u.stopErr
	}
	if !u.stopHangs {
		u.active = false
	}
	return nil
}

func (u *fakeUnits) log() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return strings.Join(u.calls, ",")
}

// fakeHealth answers from the unit: loading for readyAfter probes, then ready (or the fixed status).
type fakeHealth struct {
	mu         sync.Mutex
	units      *fakeUnits
	model      string
	readyAfter int
	status     string
	n          int
}

func (h *fakeHealth) Health(context.Context) (Health, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.units.mu.Lock()
	active := h.units.active
	h.units.mu.Unlock()
	if !active {
		return Health{}, errors.New("connection refused")
	}
	h.n++
	v := int64(5100)
	if h.status != "" {
		msg := "out of memory"
		return Health{Status: h.status, ModelID: h.model, Error: &msg}, nil
	}
	if h.n <= h.readyAfter {
		return Health{Status: HealthLoading, ModelID: h.model}, nil
	}
	return Health{Status: HealthReady, ModelID: h.model, Device: "cuda", DType: "fp16", VRAMMiB: &v}, nil
}

type rig struct {
	units  *fakeUnits
	health *fakeHealth
	clk    *clock
	envs   []Env
	held   int64
	a      *Adapter
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{units: &fakeUnits{}, clk: &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}}
	r.health = &fakeHealth{units: r.units, model: "music-a", readyAfter: 2}
	a, err := NewAdapter(Options{
		Units: r.units, Health: r.health,
		Env:       func(e Env) error { r.envs = append(r.envs, e); return e.Validate() },
		EnvBase:   func() Env { return Env{Port: 8082, Device: "cuda"} },
		Installed: func() error { return nil },
		GPUHeld: func(context.Context) (int64, bool) {
			return r.held, true
		},
		Clock: r.clk, Sleep: func(_ context.Context, d time.Duration) { r.clk.advance(d) },
	})
	if err != nil {
		t.Fatal(err)
	}
	r.a = a
	return r
}

func startReq(id string) router.EngineStart {
	return router.EngineStart{Model: &catalog.Model{ID: id, Engine: catalog.EngineAcestep}, Dir: "/srv/localai/music/" + id, Timeout: 30 * time.Second}
}

func codeOf(t *testing.T, err error) router.Code {
	t.Helper()
	var ee *router.EngineError
	if !errors.As(err, &ee) {
		t.Fatalf("want an *EngineError, got %T %v", err, err)
	}
	return ee.Code
}

// ---- tests ----

func TestStartWritesTheEnvAndWaitsForReady(t *testing.T) {
	r := newRig(t)
	if err := r.a.Start(context.Background(), startReq("music-a")); err != nil {
		t.Fatal(err)
	}
	if len(r.envs) != 1 || r.envs[0].ModelID != "music-a" || r.envs[0].ModelDir != "/srv/localai/music/music-a" || r.envs[0].Port != 8082 {
		t.Errorf("env: %+v", r.envs)
	}
	if s := r.a.Status(); s.State != StateReady || s.ModelID != "music-a" || s.VRAMMiB != 5100 {
		t.Errorf("status: %+v", s)
	}
	got, err := r.a.Resident(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "music-a" || !got[0].Ready || got[0].VRAMMiB != 5100 {
		t.Errorf("resident: %+v, %v", got, err)
	}
	if c := r.a.Cached(); len(c) != 1 || !c[0].Ready {
		t.Errorf("cached: %+v", c)
	}
}

func TestStartFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*rig)
		code  router.Code
	}{
		{"the unit does not start", func(r *rig) { r.units.startErr = errors.New("job failed") }, router.CodeEngineStart},
		{"the process is killed while it starts", func(r *rig) { r.units.exitAfter = 2; r.health.readyAfter = 100 }, router.CodeEngineStart},
		{"never ready", func(r *rig) { r.health.readyAfter = 1 << 30 }, router.CodeEngineNotReady},
		{"the shim reports an error", func(r *rig) { r.health.status = HealthError }, router.CodeEngineStart},
		{"a bad model directory", func(r *rig) {}, router.CodeEngineStart},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			c.setup(r)
			req := startReq("music-a")
			if c.name == "a bad model directory" {
				req.Dir = "relative/dir"
			}
			err := r.a.Start(context.Background(), req)
			if got := codeOf(t, err); got != c.code {
				t.Fatalf("code = %s, want %s (%v)", got, c.code, err)
			}
			if s := r.a.Status(); s.State != StateFailed {
				t.Errorf("status: %+v", s)
			}
		})
	}
}

func TestKilledProcessIsNoticedByResident(t *testing.T) {
	r := newRig(t)
	if err := r.a.Start(context.Background(), startReq("music-a")); err != nil {
		t.Fatal(err)
	}
	r.units.mu.Lock()
	r.units.active = false // the OOM killer
	r.units.mu.Unlock()
	got, err := r.a.Resident(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("resident: %+v, %v", got, err)
	}
	if s := r.a.Status(); s.State != StateStopped || s.ModelID != "" {
		t.Errorf("status: %+v", s)
	}
}

func TestResidentLearnsTheModelAfterAnAgentRestart(t *testing.T) {
	r := newRig(t)
	r.units.active = true
	r.health.readyAfter = 0
	got, err := r.a.Resident(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "music-a" || !got[0].Ready {
		t.Fatalf("resident: %+v, %v", got, err)
	}
}

func TestStartReplacesARunningModel(t *testing.T) {
	r := newRig(t)
	r.units.active = true
	if err := r.a.Start(context.Background(), startReq("music-a")); err != nil {
		t.Fatal(err)
	}
	if got := r.units.log(); got != "stop,start" {
		t.Errorf("unit calls: %s", got)
	}
}

func TestStop(t *testing.T) {
	t.Run("stops and frees", func(t *testing.T) {
		r := newRig(t)
		_ = r.a.Start(context.Background(), startReq("music-a"))
		if err := r.a.Stop(context.Background(), "music-a"); err != nil {
			t.Fatal(err)
		}
		if s := r.a.Status(); s.State != StateStopped {
			t.Errorf("status: %+v", s)
		}
	})
	t.Run("nothing runs", func(t *testing.T) {
		r := newRig(t)
		if err := r.a.Stop(context.Background(), ""); err != nil || r.units.log() != "" {
			t.Fatalf("stop: %v, calls %q", err, r.units.log())
		}
	})
	for _, c := range []struct {
		name  string
		setup func(*rig)
	}{
		{"the stop job fails", func(r *rig) { r.units.stopErr = errors.New("job failed") }},
		{"the process will not die", func(r *rig) { r.units.stopHangs = true }},
		{"graphics memory not freed", func(r *rig) { r.held = 4000 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			_ = r.a.Start(context.Background(), startReq("music-a"))
			c.setup(r)
			err := r.a.Stop(context.Background(), "music-a")
			if got := codeOf(t, err); got != router.CodeEngineNotFreed {
				t.Fatalf("code = %s", got)
			}
			if s := r.a.Status(); s.State != StateStopped || s.Error == "" {
				t.Errorf("status: %+v", s)
			}
		})
	}
}

func TestEnvFile(t *testing.T) {
	e := Env{ModelID: "acestep-v15-turbo", ModelDir: "/srv/localai/music/acestep-v15-turbo"}
	want := "# Generated by aiagent. Do not edit. Rewritten before every start of localai-music.\n" +
		"MUSIC_PORT=8082\nMUSIC_MODEL_ID=acestep-v15-turbo\nMUSIC_MODEL_DIR=/srv/localai/music/acestep-v15-turbo\n" +
		"MUSIC_WORK_DIR=/var/lib/localai/music/work\nMUSIC_DEVICE=cuda\nMUSIC_DTYPE=auto\nMUSIC_MAX_QUEUE=4\n"
	if got := string(RenderEnv(e)); got != want {
		t.Errorf("RenderEnv:\n%s\nwant\n%s", got, want)
	}
	for name, bad := range map[string]Env{
		"newline":  {ModelID: "a\nB=1", ModelDir: "/x"},
		"relative": {ModelID: "a", ModelDir: "x"},
		"device":   {ModelID: "a", ModelDir: "/x", Device: "rocm"},
		"dtype":    {ModelID: "a", ModelDir: "/x", DType: "int4"},
		"port":     {ModelID: "a", ModelDir: "/x", Port: 80},
	} {
		if bad.Validate() == nil {
			t.Errorf("%s: Validate accepted %+v", name, bad)
		}
	}
	dir := t.TempDir()
	f := EnvFile{Path: filepath.Join(dir, "music.env"), UID: -1, GID: -1}
	if err := f.Write(e); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Path)
	fi, _ := os.Stat(f.Path)
	if string(b) != want || fi.Mode().Perm() != 0o640 {
		t.Errorf("file %q mode %v", b, fi.Mode())
	}
}

func TestUnitMatchesTheGoldenFile(t *testing.T) {
	golden := filepath.Join("testdata", "localai-music.service.golden")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, RenderUnit(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	got := string(RenderUnit())
	if got != string(want) {
		t.Errorf("the unit differs from %s", golden)
	}
	for _, must := range []string{"User=localai", "--host 127.0.0.1", "IPAddressDeny=any", "NoNewPrivileges=yes", "ProtectSystem=strict", "EnvironmentFile=/etc/localai/music.env"} {
		if !strings.Contains(got, must) {
			t.Errorf("the unit lacks %q", must)
		}
	}
	if strings.Contains(got, "[Install]") {
		t.Error("the unit must not be installable at boot")
	}
}

type reloader struct{ n int }

func (r *reloader) DaemonReload(context.Context) error { r.n++; return nil }

func TestUnitWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), UnitName)
	rl := &reloader{}
	w := &UnitWriter{Path: path, UID: -1, GID: -1, Systemd: rl}
	if written, err := w.Write(context.Background()); err != nil || !written {
		t.Fatalf("first write: %v %v", written, err)
	}
	if written, err := w.Write(context.Background()); err != nil || written {
		t.Fatalf("second write: %v %v", written, err)
	}
	if rl.n != 2 {
		t.Errorf("daemon-reload ran %d times", rl.n)
	}
}

func TestRuntimeInstalled(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "python")
	if RuntimeInstalled(nil, py) == nil {
		t.Error("a missing interpreter counts as installed")
	}
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if RuntimeInstalled(nil, py) == nil {
		t.Error("a file that is not executable counts as installed")
	}
	_ = os.Chmod(py, 0o755)
	if err := RuntimeInstalled(nil, py); err != nil {
		t.Error(err)
	}
}
