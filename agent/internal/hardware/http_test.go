package hardware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/api/apitest"
	"github.com/marinoscar/local-ai/agent/internal/execx"
	"github.com/marinoscar/local-ai/agent/internal/hardware"
	"github.com/marinoscar/local-ai/agent/internal/hardware/fake"
)

// operatorAuth authenticates a request that carries X-Test-Principal: op.
var operatorAuth = api.AuthFunc(func(r *http.Request, _ *api.Route) (api.Principal, error) {
	if r.Header.Get("X-Test-Principal") == "op" {
		return api.Principal{Kind: api.KindOperator, Credential: api.CredBearer, Via: "bearer"}, nil
	}
	return api.Anonymous(), nil
})

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func sample(at time.Time, used int64) hardware.Snapshot {
	return hardware.Snapshot{
		SampledAt: at,
		GPUs: []hardware.GPU{{
			Index: 0, Vendor: hardware.VendorNVIDIA, Name: "GPU Example", Backend: hardware.BackendCUDA,
			VRAMTotalMiB: 8192, VRAMUsedMiB: used, Throttling: []string{}, Processes: []hardware.GPUProcess{},
		}},
		RAM: hardware.RAM{TotalMiB: 14336, AvailableMiB: 5120},
		// The sampler hands out Snapshot.Clone, whose slices are never nil; the fixture must be as
		// complete as a real snapshot because the response is checked against openapi.yaml.
		Disks:   []hardware.Disk{},
		Power:   hardware.Power{Source: hardware.SourceAC},
		Thermal: hardware.Thermal{Zones: []hardware.ThermalZone{}},
		CPU:     hardware.CPU{Model: "CPU Example", Cores: 12},
	}
}

// server builds the real API server (full middleware chain) with GET /hardware registered.
func server(t *testing.T, src hardware.Sampler, clk *clock) http.Handler {
	t.Helper()
	reg := api.NewRegistry()
	if err := hardware.Register(reg, src, hardware.HandlerOptions{Now: clk.Now}); err != nil {
		t.Fatal(err)
	}
	s, err := api.New(api.Options{Registry: reg, Auth: operatorAuth, Version: "test", Mode: func() string { return "online" }})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

// get serves one request and records the response against openapi.yaml (agent.md section 4.14): the
// body must match the Hardware schema and an error must be an ErrorEnvelope.
func get(t testing.TB, h http.Handler, target string, principal string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = "127.0.0.1:7070"
	if principal != "" {
		r.Header.Set("X-Test-Principal", principal)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	apitest.RecordResult(t, r, rec)
	return rec
}

func TestRegisterDeclaresTheRoute(t *testing.T) {
	reg := api.NewRegistry()
	f := fake.New(hardware.Snapshot{})
	if err := hardware.Register(reg, f, hardware.HandlerOptions{}); err != nil {
		t.Fatal(err)
	}
	routes := reg.Routes()
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	rt := routes[0]
	if rt.Method != http.MethodGet || rt.Path() != "/api/v1/hardware" || len(rt.Auth) != 1 || rt.Auth[0] != api.AuthOp || rt.Timeout != 10*time.Second || rt.Mutating() {
		t.Errorf("route = %+v", rt)
	}
	if err := hardware.Register(reg, f, hardware.HandlerOptions{}); err == nil {
		t.Error("registering the route twice must fail")
	}
}

func TestGetHardwareRequiresOperatorAndReturnsTheSnapshot(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)}
	f := fake.New(sample(clk.Now(), 6791))
	h := server(t, f, clk)

	if rec := get(t, h, "/api/v1/hardware", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous: status %d, want 401", rec.Code)
	}
	rec := get(t, h, "/api/v1/hardware", "op")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}
	var body struct {
		SampledAt time.Time `json:"sampled_at"`
		GPUs      []struct {
			Name        string `json:"name"`
			VRAMUsedMiB int64  `json:"vram_used_mib"`
		} `json:"gpus"`
		RAM struct {
			TotalMiB int64 `json:"total_mib"`
		} `json:"ram"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.SampledAt.Equal(clk.Now()) || len(body.GPUs) != 1 || body.GPUs[0].VRAMUsedMiB != 6791 || body.RAM.TotalMiB != 14336 {
		t.Errorf("body = %s", rec.Body)
	}
	if f.Refreshes() != 0 {
		t.Errorf("a plain GET forced %d samples", f.Refreshes())
	}
}

func TestFreshIsRateLimitedToOnePerTwoSeconds(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)}
	f := fake.New(sample(clk.Now(), 100))
	f.QueueRefresh(sample(clk.Now(), 200), sample(clk.Now(), 300))
	h := server(t, f, clk)

	used := func(target string) int64 {
		t.Helper()
		rec := get(t, h, target, "op")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", target, rec.Code, rec.Body)
		}
		var b struct {
			GPUs []struct {
				VRAMUsedMiB int64 `json:"vram_used_mib"`
			} `json:"gpus"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b.GPUs[0].VRAMUsedMiB
	}

	if got := used("/api/v1/hardware?fresh=1"); got != 200 || f.Refreshes() != 1 {
		t.Fatalf("first fresh: used=%d refreshes=%d", got, f.Refreshes())
	}
	clk.Advance(1500 * time.Millisecond)
	if got := used("/api/v1/hardware?fresh=1"); got != 200 || f.Refreshes() != 1 {
		t.Errorf("inside the window the cached view is served without a sample: used=%d refreshes=%d", got, f.Refreshes())
	}
	if got := used("/api/v1/hardware"); got != 200 || f.Refreshes() != 1 {
		t.Errorf("plain GET: used=%d refreshes=%d", got, f.Refreshes())
	}
	clk.Advance(600 * time.Millisecond) // 2.1 s after the forced sample
	if got := used("/api/v1/hardware?fresh=1"); got != 300 || f.Refreshes() != 2 {
		t.Errorf("after the window: used=%d refreshes=%d", got, f.Refreshes())
	}
	if used("/api/v1/hardware?fresh=0"); f.Refreshes() != 2 {
		t.Errorf("fresh=0 forced a sample")
	}
}

func TestFreshRejectsUnknownValues(t *testing.T) {
	clk := &clock{t: time.Now()}
	h := server(t, fake.New(sample(clk.Now(), 1)), clk)
	for _, q := range []string{"fresh=yes", "fresh=true", "fresh=2", "fresh=-1"} {
		rec := get(t, h, "/api/v1/hardware?"+q, "op")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
			continue
		}
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if e.Error.Code != api.CodeInvalidRequest {
			t.Errorf("%s: code %q", q, e.Error.Code)
		}
	}
}

func TestUnsampledSamplerIsAskedToSampleOnce(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 2, 15, 4, 0, 0, time.UTC)}
	f := fake.New(hardware.Snapshot{}) // never sampled
	f.QueueRefresh(sample(clk.Now(), 42))
	h := server(t, f, clk)
	rec := get(t, h, "/api/v1/hardware", "op")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"vram_used_mib":42`) || f.Refreshes() != 1 {
		t.Errorf("status %d refreshes %d body %s", rec.Code, f.Refreshes(), rec.Body)
	}
}

func TestGetHardwareAnswersWithinItsDeadlineWithAHungNvidiaSmi(t *testing.T) {
	hung := hungRunner{}
	svc := hardware.New(hardware.Options{
		FS:       hungFS(t),
		Exec:     hung,
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Statfs:   func(string) (int64, int64, error) { return 100, 50, nil },
		// nvidia-smi hangs; every exec has its own 60 ms deadline.
		ExecTimeout: 60 * time.Millisecond,
	})
	clk := &clock{t: time.Now()}
	h := server(t, svc, clk)
	start := time.Now()
	rec := get(t, h, "/api/v1/hardware?fresh=1", "op")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("answered after %v with a hung nvidia-smi", d)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var b struct {
		RAM struct {
			TotalMiB int64 `json:"total_mib"`
		} `json:"ram"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if b.RAM.TotalMiB != 2 {
		t.Errorf("the other probes must still answer: %s", rec.Body)
	}
}

// hungRunner blocks every program until its deadline.
type hungRunner struct{}

func (hungRunner) Run(ctx context.Context, _ execx.Cmd) (execx.Result, error) {
	<-ctx.Done()
	return execx.Result{}, ctx.Err()
}

// hungFS is a node with the NVIDIA driver loaded and a little RAM.
func hungFS(testing.TB) fstest.MapFS {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	return fstest.MapFS{
		"proc/driver/nvidia/version": f("NVRM version: example\n"),
		"proc/meminfo":               f("MemTotal: 2048 kB\nMemAvailable: 1024 kB\n"),
		"proc/cpuinfo":               f("processor : 0\nmodel name : Example CPU\n"),
		"proc/loadavg":               f("0.10 0.10 0.10 1/1 1\n"),
	}
}
