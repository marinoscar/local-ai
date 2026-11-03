package modelsapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marinoscar/local-ai/agent/internal/api"
	"github.com/marinoscar/local-ai/agent/internal/doctor"
	"github.com/marinoscar/local-ai/agent/internal/modelsapi"
	"github.com/marinoscar/local-ai/agent/internal/router"
	"github.com/marinoscar/local-ai/agent/internal/statusapi"
)

func decode[T any](t testing.TB, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return v
}

func envelope(t testing.TB, body []byte) errorEnvelope {
	t.Helper()
	return decode[errorEnvelope](t, body)
}

func TestRoutesMetadata(t *testing.T) {
	routes := modelsapi.Routes(modelsapi.Deps{})
	type want struct {
		method, pattern string
		auth            api.Level
		timeout         time.Duration
	}
	wants := []want{
		{"GET", "/models", api.AuthOp, 5 * time.Second},
		{"GET", "/models/{id}", api.AuthOp, 5 * time.Second},
		{"POST", "/models/{id}/download", api.AuthOp, 10 * time.Second},
		{"DELETE", "/models/{id}", api.AuthAdmin, 60 * time.Second},
		{"GET", "/key", api.AuthOp, 5 * time.Second},
		{"POST", "/key/rotate", api.AuthAdmin, 15 * time.Minute},
	}
	if len(routes) != len(wants) {
		t.Fatalf("%d routes", len(routes))
	}
	for i, w := range wants {
		rt := routes[i]
		if rt.Method != w.method || rt.Pattern != w.pattern || rt.Timeout != w.timeout || len(rt.Auth) != 1 || rt.Auth[0] != w.auth || rt.GateExempt || rt.Handler == nil {
			t.Errorf("route %d = %+v", i, rt)
		}
		if rt.Mutating() != (w.method != "GET") {
			t.Errorf("route %d: mutating = %v", i, rt.Mutating())
		}
	}
}

func TestNewAndRegisterRefuseMissingDependencies(t *testing.T) {
	r := newRig(t, nil)
	cases := []struct {
		name string
		mod  func(*modelsapi.Deps)
	}{
		{"config", func(d *modelsapi.Deps) { d.Config = nil }},
		{"catalog", func(d *modelsapi.Deps) { d.Catalog = nil }},
		{"models", func(d *modelsapi.Deps) { d.Models = nil }},
		{"sync", func(d *modelsapi.Deps) { d.Sync = nil }},
		{"lock", func(d *modelsapi.Deps) { d.Lock = nil }},
		{"keys", func(d *modelsapi.Deps) { d.Keys = nil }},
		{"restarter", func(d *modelsapi.Deps) { d.Restarter = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := r.deps
			c.mod(&d)
			if _, err := modelsapi.New(d); err == nil {
				t.Error("New accepted it")
			}
			if err := modelsapi.Register(api.NewRegistry(), d); err == nil {
				t.Error("Register accepted it")
			}
		})
	}
	reg := api.NewRegistry()
	if err := modelsapi.Register(reg, r.deps); err != nil {
		t.Fatal(err)
	}
	if err := modelsapi.Register(reg, r.deps); err == nil {
		t.Error("registering twice must fail")
	}
}

func TestListModelsMergesCatalogWithLocalState(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.routerHas(router.ModelStatus{ID: idAlpha, Status: router.StatusLoaded, Aliases: []string{"alpha"}})

	rec := r.get("/models", "op")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	list := decode[modelsapi.ModelList](t, rec.Body.Bytes())
	if list.Default == nil || *list.Default != idAlpha || list.ModelsMax < 1 {
		t.Errorf("default %v, models_max %d", list.Default, list.ModelsMax)
	}
	byID := map[string]statusapi.ModelRow{}
	var order []string
	for _, m := range list.Models {
		byID[m.ID] = m
		order = append(order, m.ID)
	}
	if len(order) != len(r.cat.Models) {
		t.Fatalf("%d models, catalog has %d: %v", len(order), len(r.cat.Models), order)
	}
	for i, cm := range r.cat.Models {
		if order[i] != cm.ID {
			t.Errorf("model %d is %s, catalog order says %s", i, order[i], cm.ID)
		}
	}
	a := byID[idAlpha]
	if !a.Present || !a.Verified || !a.Loadable || a.LoadableReason != nil || a.Status != "loaded" || !a.Loaded || !a.Default {
		t.Errorf("alpha = %+v", a)
	}
	if len(a.Locations) != 1 || a.Locations[0].Root != rootMain || !a.Locations[0].Complete || len(a.Aliases) != 1 {
		t.Errorf("alpha locations/aliases = %+v %v", a.Locations, a.Aliases)
	}
	b := byID[idBeta]
	if b.Present || b.Loadable || b.LoadableReason == nil || b.LoadableReason.Code != doctor.CAT005 || b.Status != "absent" {
		t.Errorf("beta = %+v", b)
	}
	if w := byID[idWhisper]; w.Status != "n/a" {
		t.Errorf("whisper status = %q", w.Status)
	}
}

func TestListModelsFilters(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	ids := func(rec interface{ Bytes() []byte }) []string {
		var out []string
		for _, m := range decode[modelsapi.ModelList](t, rec.Bytes()).Models {
			out = append(out, m.ID)
		}
		return out
	}
	t.Run("loadable keeps only the models that would load", func(t *testing.T) {
		rec := r.get("/models?loadable=1", "op")
		if got := ids(rec.Body); len(got) != 1 || got[0] != idAlpha {
			t.Errorf("loadable models: %v", got)
		}
	})
	t.Run("loadable=0 and true", func(t *testing.T) {
		if got := ids(r.get("/models?loadable=0", "op").Body); len(got) != len(r.cat.Models) {
			t.Errorf("loadable=0: %v", got)
		}
		if got := ids(r.get("/models?loadable=true", "op").Body); len(got) != 1 {
			t.Errorf("loadable=true: %v", got)
		}
	})
	t.Run("engine", func(t *testing.T) {
		for engine, want := range map[string]string{"whisper": idWhisper, "piper": "piper-en-medium"} {
			got := ids(r.get("/models?engine="+engine, "op").Body)
			if len(got) != 1 || got[0] != want {
				t.Errorf("engine %s: %v", engine, got)
			}
		}
		for _, id := range ids(r.get("/models?engine=llama", "op").Body) {
			if id == idWhisper {
				t.Error("a whisper model in the llama list")
			}
		}
	})
	t.Run("both filters", func(t *testing.T) {
		if got := ids(r.get("/models?engine=whisper&loadable=1", "op").Body); len(got) != 0 {
			t.Errorf("absent whisper model listed as loadable: %v", got)
		}
	})
	for _, q := range []string{"loadable=maybe", "loadable=2", "engine=bogus", "engine=LLAMA"} {
		rec := r.get("/models?"+q, "op")
		if rec.Code != http.StatusBadRequest || envelope(t, rec.Body.Bytes()).Error.Code != doctor.AGT020 {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
}

func TestListModelsAuth(t *testing.T) {
	r := newRig(t, nil)
	for _, c := range []struct {
		principal string
		status    int
		code      string
	}{
		{"", 401, doctor.SEC001},
		{"portal", 401, doctor.SVC110},
		{"op", 200, ""},
		{"admin", 200, ""},
	} {
		rec := r.get("/models", c.principal)
		if rec.Code != c.status {
			t.Errorf("%q: status %d", c.principal, rec.Code)
		}
		if c.code != "" && envelope(t, rec.Body.Bytes()).Error.Code != c.code {
			t.Errorf("%q: %s", c.principal, rec.Body)
		}
	}
}

func TestListModelsInSpanish(t *testing.T) {
	r := newRig(t, nil)
	rec := do(t, r.h, call{target: "/models", principal: "op", header: map[string]string{"Accept-Language": "es"}})
	list := decode[modelsapi.ModelList](t, rec.Body.Bytes())
	for _, m := range list.Models {
		if m.ID == idAlpha && !strings.HasPrefix(m.Summary, "Asistente general") {
			t.Errorf("summary in Spanish = %q", m.Summary)
		}
	}
}

func TestListModelsWithoutAnyModel(t *testing.T) {
	r := newRig(t, nil)
	rec := r.get("/models?engine=piper&loadable=1", "op")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["models"]) != "[]" {
		t.Errorf("models = %s, want []", raw["models"])
	}
}

func TestGetModelPresentWithFilesAndPreset(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootDrive, idAlpha) // the only copy is on the drive: files point there
	rec := r.get("/models/"+idAlpha, "op")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	det := decode[modelsapi.ModelDetail](t, rec.Body.Bytes())
	if det.ID != idAlpha || !det.Present || !det.Default {
		t.Errorf("detail = %+v", det.ModelRow)
	}
	if len(det.Files) != 1 {
		t.Fatalf("files = %+v", det.Files)
	}
	f := det.Files[0]
	if f.Kind != "weights" || f.Name != "Model-Alpha-9B-Q4_K_M.gguf" || f.SizeBytes != 5870000000 ||
		f.Path != rootDrive+"/models/"+idAlpha+"/Model-Alpha-9B-Q4_K_M.gguf" || f.SHA256 != strings.Repeat("1", 64) {
		t.Errorf("file = %+v", f)
	}
	// The preset is the section the router has: the model's name, its file and the default flag.
	if !strings.HasPrefix(det.Preset, "["+idAlpha+"]\n") || !strings.Contains(det.Preset, f.Path) || !strings.HasSuffix(det.Preset, "load-on-startup = true\n") {
		t.Errorf("preset = %q", det.Preset)
	}
}

func TestGetModelWithSeveralFiles(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idBeta) // present, though it never fits this node
	det := decode[modelsapi.ModelDetail](t, r.get("/models/"+idBeta, "op").Body.Bytes())
	if len(det.Files) != 3 || det.Files[0].Name != "main/Model-Beta-27B-Q4_K_M.gguf" ||
		det.Files[0].Path != rootMain+"/models/"+idBeta+"/main/Model-Beta-27B-Q4_K_M.gguf" {
		t.Errorf("files = %+v", det.Files)
	}
	if det.Loadable || det.Preset != "" {
		t.Errorf("a model that does not fit has no router section: loadable %v preset %q", det.Loadable, det.Preset)
	}
}

func TestGetModelAbsentOrNotLlamaHasNoPresetAndNoPaths(t *testing.T) {
	r := newRig(t, nil)
	for _, id := range []string{idAlpha, idWhisper} {
		det := decode[modelsapi.ModelDetail](t, r.get("/models/"+id, "op").Body.Bytes())
		if det.Preset != "" || len(det.Files) == 0 || det.Files[0].Path != "" {
			t.Errorf("%s: preset %q files %+v", id, det.Preset, det.Files)
		}
	}
	r.has(rootMain, idWhisper)
	det := decode[modelsapi.ModelDetail](t, r.get("/models/"+idWhisper, "op").Body.Bytes())
	if det.Preset != "" || det.Files[0].Path != rootMain+"/models/"+idWhisper+"/ggml-small.bin" {
		t.Errorf("whisper: preset %q files %+v", det.Preset, det.Files)
	}
}

func TestGetModelPresetWhileTheRouterIsDown(t *testing.T) {
	r := newRig(t, nil)
	r.has(rootMain, idAlpha)
	r.rv.set(router.View{At: t0, Stale: true, Err: "connection refused"})
	det := decode[modelsapi.ModelDetail](t, r.get("/models/"+idAlpha, "op").Body.Bytes())
	if det.Loadable || det.LoadableReason == nil || det.LoadableReason.Code != doctor.LLM004 {
		t.Fatalf("detail = %+v", det.ModelRow)
	}
	if !strings.HasPrefix(det.Preset, "["+idAlpha+"]") {
		t.Errorf("a model that would load if the router answered still has its section: %q", det.Preset)
	}
}

func TestGetModelUnknownIs404(t *testing.T) {
	r := newRig(t, nil)
	rec := r.get("/models/no-such-model", "op")
	e := envelope(t, rec.Body.Bytes())
	if rec.Code != http.StatusNotFound || e.Error.Code != doctor.AGT021 || e.Error.Details["model"] != "no-such-model" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := r.get("/models/default", "op"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /models/default is a model lookup here: %d", rec.Code)
	}
}

func TestGetModelNeverCallsTheRouterOrTheSyncEngine(t *testing.T) {
	r := newRig(t, nil)
	r.get("/models", "op")
	r.get("/models/"+idAlpha, "op")
	if len(r.sync.downloadCalls())+len(r.sync.deleteCalls()) != 0 || r.restart.count() != 0 {
		t.Error("a read started work")
	}
}
