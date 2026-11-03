package statusapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modes"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/state"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

func decode[T any](t testing.TB, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body is not the expected JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

type errBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// wantError checks the status and the doctor code of an error response.
func wantError(t testing.TB, rec *httptest.ResponseRecorder, status int, code string) errBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	e := decode[errBody](t, rec)
	if e.Error.Code != code {
		t.Fatalf("code %s, want %s: %s", e.Error.Code, code, rec.Body.String())
	}
	return e
}

func TestStatusFull(t *testing.T) {
	r := newRig(t, func(r *rig, d *statusapi.Deps) {
		d.Ark = fakeArk{statusapi.ArkInfo{
			Roots:        []statusapi.ArkRoot{{Path: "/srv/localai", Kind: "internal", Status: "yellow", UpdatedAt: t0.Add(-time.Hour)}},
			DriveMounted: false, SyncState: "idle", LastSuccessAt: t0.Add(-time.Hour),
		}}
		d.Cloud = fakeCloud{statusapi.CloudInfo{Enrolled: true, State: "linked", URL: "https://ai.marin.cr", LastHeartbeatAt: t0.Add(-10 * time.Second)}}
		d.Tailscale = fakeTailscale{modes.TailscaleStatus{Manage: true, Pattern: "B", Applied: true, BackendState: "Running"}}
		r.alerts.list = []doctor.Alert{{Code: doctor.HW012, Severity: doctor.Warning, Title: "Running on battery", Fix: "Plug in.", Since: t0.Add(-time.Hour), Details: map[string]any{"battery_percent": 64}}}
	})
	r.pres["/srv/localai|model-alpha-9b-q4"] = true
	r.rt.set(router.View{At: t0, Models: []router.ModelStatus{{ID: "model-alpha-9b-q4", Status: router.StatusLoaded}}})
	if err := r.st.MutateSync(func(s *state.State) error {
		s.Llama.Build = "f3586e66b-cuda"
		s.Connectivity.Value = state.ConnFull
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := r.get("/status", "op")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	st := decode[statusapi.Status](t, rec)

	n := st.Node
	if n.ID != "ai-n3" || n.DisplayName != "Node Three" || n.Hostname != "host-example" || n.AgentVersion != "0.1.0" {
		t.Errorf("node = %+v", n)
	}
	if n.Profile == nil || *n.Profile != "small-8gb" || n.Backend != "cuda" || n.LlamaMode != "router" {
		t.Errorf("node profile/backend/mode = %+v", n)
	}
	if n.LlamaBuild == nil || *n.LlamaBuild != "f3586e66b-cuda" || n.CatalogVersion == nil || *n.CatalogVersion != "2026.11.02-1" {
		t.Errorf("node build/catalog = %+v", n)
	}
	if n.UptimeS != 86400 || n.Time != "2026-11-02T15:04:05Z" || n.Counters != nil {
		t.Errorf("node uptime/time/counters = %+v", n)
	}
	if st.Mode.Value != "online" || st.Mode.Since == nil || st.Mode.Transition != nil {
		t.Errorf("mode = %+v", st.Mode)
	}
	if st.Connectivity.Value != "full" || st.Connectivity.CheckedAt == nil || *st.Connectivity.CheckedAt != "2026-11-02T15:04:00Z" {
		t.Errorf("connectivity = %+v", st.Connectivity)
	}
	h := st.Hardware
	if h.GPU == nil || *h.GPU != "GPU Example" || *h.VRAMTotalMiB != 8192 || *h.VRAMUsedMiB != 6791 || h.RAMTotalMiB != 14336 || h.RAMAvailableMiB != 5120 {
		t.Errorf("hardware = %+v", h)
	}
	if h.GPUTempC == nil || *h.GPUTempC != 61 || h.CPUTempC == nil || *h.CPUTempC != 72 {
		t.Errorf("temperatures = %v %v", h.GPUTempC, h.CPUTempC)
	}
	if h.Power.Source != "battery" || h.Power.BatteryPercent == nil || *h.Power.BatteryPercent != 64 || h.Power.Charging == nil || *h.Power.Charging {
		t.Errorf("power = %+v", h.Power)
	}
	if len(h.GPUs) != 1 || len(h.GPUs[0].Processes) != 1 || h.GPUs[0].Processes[0].PID != 4242 {
		t.Errorf("gpus = %+v", h.GPUs)
	}
	if len(st.Services) != 2 || st.Services[0].Name != "llama" || !st.Services[0].Healthy || st.Services[0].Since == nil || st.Services[1].Since != nil {
		t.Errorf("services = %+v", st.Services)
	}
	m := st.Models
	if m.Default == nil || *m.Default != "model-alpha-9b-q4" || len(m.Loaded) != 1 || m.Loaded[0] != "model-alpha-9b-q4" || m.Present != 1 || m.ModelsMax != 1 {
		t.Errorf("models = %+v", m)
	}
	if len(st.Ark.Roots) != 1 || st.Ark.Roots[0].Status != "yellow" || st.Ark.Drive.Mounted || st.Ark.Sync.State != "idle" {
		t.Errorf("ark = %+v", st.Ark)
	}
	if !st.Cloud.Enrolled || st.Cloud.State != "linked" || st.Cloud.URL == nil || st.Cloud.LastHeartbeatAt == nil {
		t.Errorf("cloud = %+v", st.Cloud)
	}
	if !st.Tailscale.Manage || st.Tailscale.Pattern != "B" || !st.Tailscale.Applied || st.Tailscale.BackendState == nil || *st.Tailscale.BackendState != "Running" {
		t.Errorf("tailscale = %+v", st.Tailscale)
	}
	if len(st.Alerts) != 1 || st.Alerts[0].Code != doctor.HW012 || st.Alerts[0].Details["battery_percent"] != float64(64) {
		t.Errorf("alerts = %+v", st.Alerts)
	}
}

// TestStatusArkMetrics: the readiness gauges of the ark view appear under ark.metrics (ark-sync.md
// section 7), and are absent while no view supplies them.
func TestStatusArkMetrics(t *testing.T) {
	r := newRig(t, func(r *rig, d *statusapi.Deps) {
		d.Ark = fakeArk{statusapi.ArkInfo{
			Roots: []statusapi.ArkRoot{{Path: "/srv/localai", Kind: "internal", Status: "yellow"}},
			Metrics: []statusapi.ArkMetric{
				{Name: "localai_ark_root_overall", Labels: map[string]string{"root": "internal"}, Value: 1},
				{Name: "localai_ark_root_free_bytes", Labels: map[string]string{"root": "internal"}, Value: 105300000000},
				{Name: "localai_ark_reverify_age_days_max", Labels: map[string]string{"root": "internal"}, Value: 12},
			},
		}}
	})
	st := decode[statusapi.Status](t, r.get("/status", "op"))
	if len(st.Ark.Metrics) != 3 || st.Ark.Metrics[0].Name != "localai_ark_root_overall" || st.Ark.Metrics[0].Labels["root"] != "internal" ||
		st.Ark.Metrics[1].Value != 105300000000 || st.Ark.Metrics[2].Value != 12 {
		t.Errorf("metrics = %+v", st.Ark.Metrics)
	}
	none := newRig(t, func(r *rig, d *statusapi.Deps) { d.Ark = fakeArk{statusapi.ArkInfo{}} })
	if body := none.get("/status", "op").Body.String(); strings.Contains(body, `"metrics"`) {
		t.Errorf("no metrics, no key: %s", body)
	}
}

// TestStatusEveryViewUnavailable: a node whose subsystems do not exist yet still answers 200 with the
// documented empty states (the issue's "renders with every view unavailable").
func TestStatusEveryViewUnavailable(t *testing.T) {
	r := newRig(t, nil)
	h := serve(t, statusapi.Deps{Config: r.cfg, Catalog: statusapi.NewCatalogStore(emptyLoaded(), time.Time{}), Refresher: r.refresh})
	rec := do(t, h, call{target: "/status", principal: "op"})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	st := decode[statusapi.Status](t, rec)
	if st.Node.ID != "ai-n3" || st.Node.CatalogVersion != nil || st.Node.LlamaBuild != nil || st.Node.UptimeS != 0 {
		t.Errorf("node = %+v", st.Node)
	}
	if st.Mode.Value != "offline" || st.Mode.Since != nil || st.Mode.Transition != nil {
		t.Errorf("mode = %+v", st.Mode)
	}
	if st.Connectivity.Value != state.ConnNotChecked || st.Connectivity.CheckedAt != nil || st.Connectivity.NoneSince != nil {
		t.Errorf("connectivity = %+v", st.Connectivity)
	}
	if st.Hardware.GPU != nil || st.Hardware.VRAMTotalMiB != nil || st.Hardware.Power.Source != "unknown" || len(st.Hardware.GPUs) != 0 {
		t.Errorf("hardware = %+v", st.Hardware)
	}
	if st.Services == nil || len(st.Services) != 0 || st.Alerts == nil || len(st.Alerts) != 0 {
		t.Errorf("services %v alerts %v", st.Services, st.Alerts)
	}
	m := st.Models
	if m.Default != nil || m.Loaded == nil || m.Loading == nil || m.Present != 0 || m.Loadable != 0 {
		t.Errorf("models = %+v", m)
	}
	if len(st.Ark.Roots) != 0 || st.Ark.Drive.Mounted || st.Ark.Sync.State != "idle" || st.Ark.Sync.LastSuccessAt != nil {
		t.Errorf("ark = %+v", st.Ark)
	}
	if st.Cloud.Enrolled || st.Cloud.State != "unenrolled" || st.Cloud.URL != nil {
		t.Errorf("cloud = %+v", st.Cloud)
	}
	// Without a manager the block reports the configuration: manage true, pattern B, nothing applied.
	if !st.Tailscale.Manage || st.Tailscale.Pattern != "B" || st.Tailscale.Applied || st.Tailscale.BackendState != nil {
		t.Errorf("tailscale = %+v", st.Tailscale)
	}
}

func TestStatusCounters(t *testing.T) {
	r := newRig(t, nil)
	cases := []struct {
		name, target string
		want         int
		counters     bool
	}{
		{"absent", "/status", 200, false},
		{"zero", "/status?counters=0", 200, false},
		{"one", "/status?counters=1", 200, true},
		{"invalid", "/status?counters=maybe", 400, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := r.get(c.target, "op")
			if rec.Code != c.want {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if c.want != 200 {
				e := wantError(t, rec, 400, api.CodeInvalidRequest)
				if e.Error.Details["field"] != "counters" {
					t.Errorf("details = %v", e.Error.Details)
				}
				return
			}
			st := decode[statusapi.Status](t, rec)
			if (st.Node.Counters != nil) != c.counters {
				t.Errorf("counters = %v", st.Node.Counters)
			}
			if c.counters && st.Node.Counters["model_loads_ok"] != float64(3) {
				t.Errorf("counters = %v", st.Node.Counters)
			}
		})
	}
}

func TestStatusAuthAndLanguage(t *testing.T) {
	r := newRig(t, func(r *rig, _ *statusapi.Deps) {
		r.alerts.list = []doctor.Alert{{Code: doctor.HW012, Severity: doctor.Warning, Title: "Running on battery", Fix: "Plug in.", Since: t0}}
	})
	wantError(t, r.get("/status", ""), 401, api.CodeUnauthenticated)
	wantError(t, r.get("/status", "portal"), 401, doctor.SVC110)

	en := decode[statusapi.Status](t, r.get("/status", "op"))
	es := decode[statusapi.Status](t, do(t, r.h, call{target: "/status", principal: "op", header: map[string]string{"Accept-Language": "es"}}))
	wantEN, wantFixEN, _ := doctor.Text(doctor.HW012, doctor.LangEN)
	wantES, wantFixES, _ := doctor.Text(doctor.HW012, doctor.LangES)
	if en.Alerts[0].Title != wantEN || en.Alerts[0].Fix != wantFixEN {
		t.Errorf("english alert = %+v", en.Alerts[0])
	}
	if es.Alerts[0].Title != wantES || es.Alerts[0].Fix != wantFixES || wantES == wantEN {
		t.Errorf("spanish alert = %+v", es.Alerts[0])
	}
}

func TestStatusModeTransition(t *testing.T) {
	r := newRig(t, func(r *rig, _ *statusapi.Deps) {
		r.mode.info.Transition = &modes.TransitionInfo{
			OpID: "op_3h8x2k1m0p7w", From: modes.Online, Target: modes.Survival, Step: modes.StepLinkStop, StartedAt: t0,
			Warnings: []modes.Warning{{Code: doctor.LLM020, Message: "The preferred model failed to start."}},
		}
	})
	st := decode[statusapi.Status](t, r.get("/status", "op"))
	tr := st.Mode.Transition
	if tr == nil || tr.OpID != "op_3h8x2k1m0p7w" || tr.Target != "survival" || tr.Step != modes.StepLinkStop || len(tr.Warnings) != 1 || tr.Warnings[0].Code != doctor.LLM020 {
		t.Errorf("transition = %+v", tr)
	}
}

// TestStatusForCloud: the state pushed to the cloud drops alerts[].details and gpus[].processes, is
// in English whatever the request language, and carries no counters.
func TestStatusForCloud(t *testing.T) {
	r := newRig(t, func(r *rig, _ *statusapi.Deps) {
		r.alerts.list = []doctor.Alert{{Code: doctor.HW012, Severity: doctor.Warning, Title: "x", Fix: "y", Since: t0, Details: map[string]any{"battery_percent": 64}}}
	})
	b := r.deps.Builder()
	local := b.Build(context.Background(), false, statusapi.BuildOptions{Lang: doctor.LangES, Counters: true})
	cloud := b.Build(context.Background(), true, statusapi.BuildOptions{Lang: doctor.LangES, Counters: true})

	if len(local.Hardware.GPUs[0].Processes) != 1 || local.Alerts[0].Details == nil || local.Node.Counters == nil {
		t.Fatalf("local body lost something: %+v", local)
	}
	if len(cloud.Hardware.GPUs) != 1 || cloud.Hardware.GPUs[0].Processes != nil {
		t.Errorf("cloud gpus = %+v", cloud.Hardware.GPUs)
	}
	if cloud.Alerts[0].Details != nil || cloud.Node.Counters != nil {
		t.Errorf("cloud alerts = %+v counters = %v", cloud.Alerts, cloud.Node.Counters)
	}
	wantEN, _, _ := doctor.Text(doctor.HW012, doctor.LangEN)
	if cloud.Alerts[0].Title != wantEN {
		t.Errorf("cloud alert title %q, want English %q", cloud.Alerts[0].Title, wantEN)
	}
	body, err := json.Marshal(cloud)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"processes", "details", "llama-server"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the cloud body contains %q: %s", leak, body)
		}
	}
}

// TestStatusRouteMetadata: the auth level and the deadline of every route (agent.md section 3.8).
func TestStatusRouteMetadata(t *testing.T) {
	var found int
	for _, rt := range statusapi.Routes(statusapi.Deps{}) {
		switch rt.Method + " " + rt.Pattern {
		case "GET /status", "GET /profile", "GET /catalog":
			if rt.Timeout != 5*time.Second || len(rt.Auth) != 1 || rt.Auth[0] != api.AuthOp {
				t.Errorf("%s %s: %v %v", rt.Method, rt.Pattern, rt.Timeout, rt.Auth)
			}
		case "PUT /profile":
			if rt.Timeout != 30*time.Second || rt.Auth[0] != api.AuthAdmin {
				t.Errorf("PUT /profile: %v %v", rt.Timeout, rt.Auth)
			}
		case "POST /catalog/refresh":
			if rt.Timeout != 120*time.Second || rt.Auth[0] != api.AuthOp {
				t.Errorf("POST /catalog/refresh: %v %v", rt.Timeout, rt.Auth)
			}
		default:
			t.Errorf("unexpected route %s %s", rt.Method, rt.Pattern)
			continue
		}
		found++
	}
	if found != 5 {
		t.Errorf("%d routes", found)
	}
}

func TestRegisterRefusesMissingDependencies(t *testing.T) {
	if err := statusapi.Register(api.NewRegistry(), statusapi.Deps{}); err == nil {
		t.Error("Register accepted empty deps")
	}
	r := newRig(t, nil)
	if err := statusapi.Register(api.NewRegistry(), statusapi.Deps{Config: r.cfg, Catalog: r.store}); err == nil {
		t.Error("Register accepted deps without a refresher")
	}
	if err := statusapi.Register(api.NewRegistry(), r.deps); err != nil {
		t.Error(err)
	}
}
