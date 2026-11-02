package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/doctor"
)

// doctorFixture is a registry of fake checks covering several areas, both fast and slow.
func doctorFixture(t *testing.T) *doctor.Registry {
	t.Helper()
	reg := doctor.NewRegistry()
	add := func(id, area string, fast bool, res ...doctor.Result) {
		t.Helper()
		if err := reg.Add(doctor.Check{ID: id, Area: area, Fast: fast, Run: func(context.Context) []doctor.Result { return res }}); err != nil {
			t.Fatal(err)
		}
	}
	add("agt.config", "AGT", true)
	add("hw.battery", "HW", true, doctor.Result{Code: doctor.HW012, Details: map[string]any{"battery_percent": 64}})
	add("hw.smart", "HW", false, doctor.Result{Code: doctor.HW008})
	add("llm.router_health", "LLM", true)
	add("net.loopback_only", "NET", false)
	add("net.dns", "NET", false, doctor.Result{Code: doctor.NET013})
	return reg
}

func newDoctorServer(t *testing.T, reg *doctor.Registry) *Server {
	t.Helper()
	s, _ := newTestServer(t, func(o *Options) {
		if err := RegisterDoctor(o.Registry, DoctorOptions{
			Registry: reg,
			Now:      func() time.Time { return time.Date(2026, 11, 2, 8, 0, 30, 0, time.UTC) },
		}); err != nil {
			t.Fatal(err)
		}
	})
	return s
}

func getDoctor(t *testing.T, s *Server, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]string{"X-Test-Principal": "bearer"}
	for k, v := range hdr {
		h[k] = v
	}
	return do(s.Handler(), req{target: "/api/v1/doctor" + target, header: h})
}

func decodeDoctor(t *testing.T, rec *httptest.ResponseRecorder) DoctorResponse {
	t.Helper()
	recordOK(t, rec) // the body must match DoctorReport of openapi.yaml (agent.md section 4.14)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	var out DoctorResponse
	dec := json.NewDecoder(strings.NewReader(rec.Body.String()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("body is not a DoctorResponse: %v\n%s", err, rec.Body.String())
	}
	return out
}

func checksOf(rep DoctorResponse) []string {
	var out []string
	for _, r := range rep.Results {
		out = append(out, r.Check)
	}
	out = append(out, rep.Passed...)
	return out
}

func TestDoctorResponseShape(t *testing.T) {
	s := newDoctorServer(t, doctorFixture(t))
	rec := getDoctor(t, s, "", nil)
	rep := decodeDoctor(t, rec)

	if !rep.RanAt.Equal(time.Date(2026, 11, 2, 8, 0, 30, 0, time.UTC)) {
		t.Errorf("ran_at = %v", rep.RanAt)
	}
	// hw.battery (HW-012) and net.dns (NET-013) are warnings, hw.smart (HW-008) is critical.
	if rep.Summary.OK != 3 || rep.Summary.Warning != 2 || rep.Summary.Critical != 1 || len(rep.Results) != 3 {
		t.Errorf("summary = %+v, results = %d", rep.Summary, len(rep.Results))
	}
	if !reflect.DeepEqual(rep.Passed, []string{"agt.config", "llm.router_health", "net.loopback_only"}) {
		t.Errorf("passed = %v", rep.Passed)
	}
	var bat doctor.Result
	for _, r := range rep.Results {
		if r.Check == "hw.battery" {
			bat = r
		}
	}
	if bat.Code != "LAI-HW-012" || bat.Severity != "warning" || bat.Title != "Running on battery" || bat.Details["battery_percent"] != float64(64) {
		t.Errorf("hw.battery = %+v", bat)
	}

	// The keys of agent.md section 3.8.27, exactly.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range raw {
		keys = append(keys, k)
	}
	if len(keys) != 5 {
		t.Errorf("keys = %v, want ran_at, duration_ms, summary, results, passed", keys)
	}
	var summary map[string]int
	if err := json.Unmarshal(raw["summary"], &summary); err != nil || len(summary) != 5 {
		t.Errorf("summary = %s (%v), want ok, info, warning, error, critical", raw["summary"], err)
	}
}

func TestDoctorFilters(t *testing.T) {
	s := newDoctorServer(t, doctorFixture(t))
	tests := []struct {
		name   string
		query  string
		want   []string
		status int
	}{
		{"area", "?area=LLM", []string{"llm.router_health"}, 200},
		{"area is case-insensitive", "?area=hw", []string{"hw.battery", "hw.smart"}, 200},
		{"repeatable", "?area=LLM&area=NET", []string{"llm.router_health", "net.dns", "net.loopback_only"}, 200},
		{"comma separated", "?area=LLM,AGT", []string{"agt.config", "llm.router_health"}, 200},
		{"fast", "?fast=1", []string{"agt.config", "hw.battery", "llm.router_health"}, 200},
		{"fast true", "?fast=true", []string{"agt.config", "hw.battery", "llm.router_health"}, 200},
		{"fast=0 is everything", "?fast=0", []string{"agt.config", "hw.battery", "hw.smart", "llm.router_health", "net.dns", "net.loopback_only"}, 200},
		{"fast and area", "?fast=1&area=HW", []string{"hw.battery"}, 200},
		{"an area with no checks is an empty report", "?area=DSK", nil, 200},
		{"unknown query keys are ignored", "?verbose=1", []string{"agt.config", "hw.battery", "hw.smart", "llm.router_health", "net.dns", "net.loopback_only"}, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rep := decodeDoctor(t, getDoctor(t, s, tc.query, nil))
			got := checksOf(rep)
			if len(got) != len(tc.want) {
				t.Fatalf("ran %v, want %v", got, tc.want)
			}
			have := map[string]bool{}
			for _, id := range got {
				have[id] = true
			}
			for _, id := range tc.want {
				if !have[id] {
					t.Errorf("%s did not run: %v", id, got)
				}
			}
		})
	}
}

func TestDoctorBadQueryIsAGT020(t *testing.T) {
	s := newDoctorServer(t, doctorFixture(t))
	for _, tc := range []struct{ query, param string }{
		{"?area=XYZ", "area"},
		{"?area=HW&area=nope", "area"},
		{"?fast=maybe", "fast"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			e := expectError(t, getDoctor(t, s, tc.query, nil), http.StatusBadRequest, CodeInvalidRequest)
			if e.Error.Details["param"] != tc.param {
				t.Errorf("details = %+v, want param %q", e.Error.Details, tc.param)
			}
		})
	}
}

func TestDoctorLocalization(t *testing.T) {
	s := newDoctorServer(t, doctorFixture(t))
	en := decodeDoctor(t, getDoctor(t, s, "?area=HW", nil))
	es := decodeDoctor(t, getDoctor(t, s, "?area=HW", map[string]string{"Accept-Language": "es-CR,es;q=0.9"}))
	if len(en.Results) != len(es.Results) || len(en.Results) == 0 {
		t.Fatalf("results: en %d, es %d", len(en.Results), len(es.Results))
	}
	for i := range en.Results {
		a, b := en.Results[i], es.Results[i]
		if a.Code != b.Code || a.Severity != b.Severity || a.Check != b.Check {
			t.Errorf("code, severity and check must not change: %+v vs %+v", a, b)
		}
		if a.Title == b.Title || a.Fix == b.Fix {
			t.Errorf("%s: not translated: %+v", a.Code, b)
		}
	}
	for _, r := range es.Results {
		if r.Code == "LAI-HW-012" && r.Title != "Funcionando con batería" {
			t.Errorf("title = %q", r.Title)
		}
	}

	// ui.language es without Accept-Language.
	s2, _ := newTestServer(t, func(o *Options) {
		o.Config = fixedConfig{lang: "es"}
		if err := RegisterDoctor(o.Registry, DoctorOptions{Registry: doctorFixture(t)}); err != nil {
			t.Fatal(err)
		}
	})
	ui := decodeDoctor(t, getDoctor(t, s2, "?area=HW", nil))
	for _, r := range ui.Results {
		if r.Code == "LAI-HW-012" && r.Title != "Funcionando con batería" {
			t.Errorf("ui.language es: title = %q", r.Title)
		}
	}
}

func TestDoctorCheckFailuresBecomeAGT099(t *testing.T) {
	reg := doctor.NewRegistry()
	_ = reg.Add(doctor.Check{ID: "llm.slow", Area: "LLM", Timeout: 20 * time.Millisecond, Run: func(ctx context.Context) []doctor.Result {
		<-ctx.Done()
		return nil
	}})
	_ = reg.Add(doctor.Check{ID: "net.panics", Area: "NET", Run: func(context.Context) []doctor.Result { panic("boom") }})
	_ = reg.Add(doctor.Check{ID: "agt.fine", Area: "AGT", Run: func(context.Context) []doctor.Result { return nil }})
	s := newDoctorServer(t, reg)
	rep := decodeDoctor(t, getDoctor(t, s, "", nil))
	if rep.Summary.OK != 1 || rep.Summary.Error != 2 {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	for _, r := range rep.Results {
		if r.Code != CodeInternal || r.Details["check"] != r.Check {
			t.Errorf("result = %+v", r)
		}
	}
}

func TestDoctorAuthAndRouteMetadata(t *testing.T) {
	s := newDoctorServer(t, doctorFixture(t))
	t.Run("anonymous is refused", func(t *testing.T) {
		rec := do(s.Handler(), req{target: "/api/v1/doctor"})
		expectError(t, rec, http.StatusUnauthorized, CodeUnauthenticated)
	})
	t.Run("a Portal session is refused", func(t *testing.T) {
		rec := do(s.Handler(), req{target: "/api/v1/doctor", header: map[string]string{"X-Test-Principal": "portal"}})
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("an operator over the socket is accepted", func(t *testing.T) {
		rec := do(s.Handler(), req{target: "/api/v1/doctor", header: map[string]string{"X-Test-Principal": "socket"}})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("POST is not allowed", func(t *testing.T) {
		rec := do(s.Handler(), req{method: "POST", target: "/api/v1/doctor", header: jsonHeaders(nil), body: "{}"})
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})

	rt := DoctorRoute(DoctorOptions{})
	if rt.Method != http.MethodGet || rt.Path() != "/api/v1/doctor" || !reflect.DeepEqual(rt.Auth, []Level{AuthOp}) || rt.Timeout != 60*time.Second {
		t.Errorf("route = %+v, want GET /doctor, auth op, 60 s (agent.md route table)", rt)
	}
	if rt.Mutating() {
		t.Error("GET /doctor must not be gated by the starting state")
	}
}

func TestRegisterDoctorTwiceFails(t *testing.T) {
	reg := NewRegistry()
	if err := RegisterDoctor(reg, DoctorOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := RegisterDoctor(reg, DoctorOptions{}); err == nil {
		t.Error("registering GET /doctor twice must fail")
	}
}

func TestDoctorWorksWhileStarting(t *testing.T) {
	s, _ := newTestServer(t, func(o *Options) {
		if err := RegisterDoctor(o.Registry, DoctorOptions{Registry: doctorFixture(t)}); err != nil {
			t.Fatal(err)
		}
	})
	s.SetStarting(true)
	if rec := getDoctor(t, s, "?fast=1", nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d while starting: %s", rec.Code, rec.Body.String())
	}
}
